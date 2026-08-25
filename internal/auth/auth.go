// Package auth handles password hashing, sessions and role checks.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role values, ordered by capability.
const (
	RoleReader = "reader"
	RoleEditor = "editor"
	RoleAdmin  = "admin"
)

var roleRank = map[string]int{RoleReader: 1, RoleEditor: 2, RoleAdmin: 3}

// AtLeast reports whether have satisfies want.
func AtLeast(have, want string) bool { return roleRank[have] >= roleRank[want] }

// hashParams are deliberately stronger than argon2id's defaults on the memory
// axis: this is a self-hosted server with RAM to spare, and password hashing is
// the one place where being slow is the point.
var hashParams = &argon2id.Params{
	Memory:      64 * 1024, // 64 MiB
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

// Common errors.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrPasswordTooShort   = errors.New("password must be at least 10 characters")
	ErrInactive           = errors.New("account is disabled")
)

// MinPasswordLength is enforced on every password we set.
//
// Length beats composition rules: a long passphrase is both stronger and easier
// to remember than a short string with a digit bolted on, and composition rules
// mostly produce predictable substitutions.
const MinPasswordLength = 10

// HashPassword returns an argon2id encoded hash.
func HashPassword(plain string) (string, error) {
	if len([]rune(plain)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	return argon2id.CreateHash(plain, hashParams)
}

// User is an authenticated account.
type User struct {
	ID                    int64  `json:"id"`
	Username              string `json:"username"`
	Email                 string `json:"email,omitempty"`
	Role                  string `json:"role"`
	Locale                string `json:"locale"`
	PasswordResetRequired bool   `json:"password_reset_required"`
}

// Can reports whether the user holds at least the given role.
func (u *User) Can(role string) bool { return u != nil && AtLeast(u.Role, role) }

// Service performs authentication against the database.
type Service struct{ pool *pgxpool.Pool }

// NewService builds an auth service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Authenticate verifies a username and password.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		u      User
		hash   string
		active bool
		email  *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, email, password_hash, role, locale, is_active,
		       password_reset_required
		FROM users WHERE lower(username) = lower($1)`, strings.TrimSpace(username)).
		Scan(&u.ID, &u.Username, &email, &hash, &u.Role, &u.Locale, &active,
			&u.PasswordResetRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		// Spend comparable time on an unknown username so the response time
		// does not reveal which accounts exist.
		_, _ = argon2id.CreateHash(password, hashParams)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if email != nil {
		u.Email = *email
	}

	ok, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		// An unparseable hash is what imported users carry; treat it as a
		// failed login, not a server error.
		return nil, ErrInvalidCredentials
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if !active {
		return nil, ErrInactive
	}
	return &u, nil
}

// ByID loads a user, used to rehydrate a session.
func (s *Service) ByID(ctx context.Context, id int64) (*User, error) {
	var u User
	var email *string
	var active bool
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, email, role, locale, is_active, password_reset_required
		FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Username, &email, &u.Role, &u.Locale, &active, &u.PasswordResetRequired)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrInactive
	}
	if email != nil {
		u.Email = *email
	}
	return &u, nil
}

// SetPassword replaces a user's password and clears the reset requirement.
func (s *Service) SetPassword(ctx context.Context, userID int64, plain string) error {
	hash, err := HashPassword(plain)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE users SET password_hash=$2, password_reset_required=false, updated_at=now()
		WHERE id=$1`, userID, hash)
	return err
}

// CreateUser adds an account.
func (s *Service) CreateUser(ctx context.Context, username, email, password, role string) (*User, error) {
	if _, ok := roleRank[role]; !ok {
		return nil, fmt.Errorf("unknown role %q", role)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	var em any
	if e := strings.TrimSpace(email); e != "" {
		em = e
	}
	var u User
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1,$2,$3,$4)
		RETURNING id, username, role, locale, password_reset_required`,
		strings.TrimSpace(username), em, hash, role).
		Scan(&u.ID, &u.Username, &u.Role, &u.Locale, &u.PasswordResetRequired)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// NeedsSetup reports whether no usable admin account exists yet, which puts the
// server into first-run mode. Imported users all carry an unusable hash, so a
// freshly imported library still needs setup.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM users
		WHERE role='admin' AND is_active AND NOT password_reset_required`).Scan(&n)
	return n == 0, err
}

// NewKoboToken issues a Kobo sync token for a user.
//
// 16 random bytes, hex encoded, exactly as calibre-web does -- it appears in
// the URL path the device is configured with, so it is a bearer credential and
// must be unguessable.
func (s *Service) NewKoboToken(ctx context.Context, userID int64, label string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO kobo_auth_tokens (user_id, token, label) VALUES ($1,$2,$3)`,
		userID, token, label); err != nil {
		return "", err
	}
	return token, nil
}

// UserForKoboToken resolves a Kobo sync token to its owner and records use.
func (s *Service) UserForKoboToken(ctx context.Context, token string) (*User, error) {
	var u User
	var email *string
	err := s.pool.QueryRow(ctx, `
		UPDATE kobo_auth_tokens SET last_used_at = now()
		WHERE token = $1
		RETURNING user_id`, token).Scan(&u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	full, err := s.ByID(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	_ = email
	return full, nil
}

// SessionLifetime is how long a login lasts.
const SessionLifetime = 30 * 24 * time.Hour
