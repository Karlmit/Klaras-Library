package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Karlmit/Klaras-Library/internal/auth"
)

// UserSummary is an account as the admin screen sees it.
type UserSummary struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email,omitempty"`
	Role          string `json:"role"`
	IsActive      bool   `json:"is_active"`
	NeedsPassword bool   `json:"needs_password"`
	Shelves       int64  `json:"shelves"`
	KoboTokens    int64  `json:"kobo_tokens"`
	CreatedAt     string `json:"created_at"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT u.id, u.username, u.email, u.role, u.is_active, u.password_reset_required,
		       (SELECT count(*) FROM shelves sh WHERE sh.user_id = u.id),
		       (SELECT count(*) FROM kobo_auth_tokens k WHERE k.user_id = u.id),
		       u.created_at
		FROM users u ORDER BY u.id`)
	if err != nil {
		s.fail(w, r, err, "list users")
		return
	}
	defer rows.Close()

	out := []UserSummary{}
	for rows.Next() {
		var u UserSummary
		var email *string
		var created time.Time
		if err := rows.Scan(&u.ID, &u.Username, &email, &u.Role, &u.IsActive,
			&u.NeedsPassword, &u.Shelves, &u.KoboTokens, &created); err != nil {
			s.fail(w, r, err, "scan user")
			return
		}
		if email != nil {
			u.Email = *email
		}
		u.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, err, "list users")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleSetUserPassword lets an admin set someone else's password.
//
// This is what makes an imported account usable at all. Users migrated from
// calibre-web carry a hash we cannot verify, so they can never log in, and the
// change-your-own-password screen is only reachable once you have. Without an
// admin able to break that circle, every imported account is dead.
func (s *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad user id")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if err := s.auth.SetPassword(r.Context(), id, in.Password); err != nil {
		if err == auth.ErrPasswordTooShort {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.fail(w, r, err, "set user password")
		return
	}
	s.log.Info("admin set a user's password", "target_user", id,
		"by", s.currentUser(r).Username)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password set"})
}

// handleUpdateUser changes a user's role or whether they are active.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad user id")
		return
	}
	var in struct {
		Role     *string `json:"role"`
		IsActive *bool   `json:"is_active"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if in.Role != nil {
		switch *in.Role {
		case auth.RoleAdmin, auth.RoleEditor, auth.RoleReader:
		default:
			writeErr(w, http.StatusBadRequest, "unknown role")
			return
		}
	}

	// Refuse to remove the last usable admin, which would lock everyone out of
	// their own library with no way back except the CLI.
	me := s.currentUser(r)
	if id == me.ID && ((in.Role != nil && *in.Role != auth.RoleAdmin) ||
		(in.IsActive != nil && !*in.IsActive)) {
		var others int64
		if err := s.db.Pool.QueryRow(r.Context(), `
			SELECT count(*) FROM users
			WHERE id <> $1 AND role='admin' AND is_active AND NOT password_reset_required`,
			id).Scan(&others); err != nil {
			s.fail(w, r, err, "count admins")
			return
		}
		if others == 0 {
			writeErr(w, http.StatusConflict,
				"you are the only usable administrator; promote someone else first")
			return
		}
	}

	if _, err := s.db.Pool.Exec(r.Context(), `
		UPDATE users SET role = COALESCE($2, role),
		                 is_active = COALESCE($3, is_active),
		                 updated_at = now()
		WHERE id = $1`, id, in.Role, in.IsActive); err != nil {
		s.fail(w, r, err, "update user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
