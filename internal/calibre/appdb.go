package calibre

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppDB is calibre-web's own state database (app.db), which is separate from
// Calibre's metadata.db. Shelves, their Kobo sync flags, users, Kobo auth
// tokens and reading state all live here, not in the library.
type AppDB struct {
	db *sql.DB
}

// OpenAppDB opens calibre-web's app.db read-only.
func OpenAppDB(path string) (*AppDB, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	return &AppDB{db: db}, nil
}

// Close releases the database.
func (a *AppDB) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

// unusablePasswordHash is stored for imported users. It is not a valid argon2id
// encoding, so verification always fails; combined with password_reset_required
// this forces a reset without ever leaving an account open.
const unusablePasswordHash = "!imported-no-password"

// calibre-web role bitmask.
const (
	cwRoleAdmin     = 1
	cwRoleUpload    = 4
	cwRoleEdit      = 8
	cwRoleAnonymous = 32
)

// AppResult reports what the app.db import did.
type AppResult struct {
	Users       int64
	Shelves     int64
	ShelfBooks  int64
	KoboTokens  int64
	ReadStates  int64
	SkippedUser int64
	OrphanLinks int64
	Elapsed     time.Duration
	Committed   bool
}

// mapRole converts calibre-web's bitmask to our three roles.
func mapRole(mask int64) (role string, skip bool) {
	if mask&cwRoleAnonymous != 0 {
		// calibre-web's built-in Guest, which exists only to back its
		// anonymous-browsing feature. Not a real account.
		return "", true
	}
	switch {
	case mask&cwRoleAdmin != 0:
		return "admin", false
	case mask&(cwRoleEdit|cwRoleUpload) != 0:
		return "editor", false
	default:
		return "reader", false
	}
}

// ImportAppDB brings calibre-web's users, shelves and Kobo state across.
//
// Must run after ImportLibrary: shelf and reading-state rows reference book
// ids, and any that point at a book the library import did not produce are
// dropped and counted rather than failing the whole import.
func ImportAppDB(ctx context.Context, pool *pgxpool.Pool, app *AppDB, opts Options, log *slog.Logger) (*AppResult, error) {
	start := time.Now()
	res := &AppResult{}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var books int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM books`).Scan(&books); err != nil {
		return nil, err
	}
	if books == 0 {
		return nil, fmt.Errorf("library is empty; run the library import before the app.db import")
	}

	if opts.Purge {
		if _, err := tx.Exec(ctx, `TRUNCATE users RESTART IDENTITY CASCADE`); err != nil {
			return nil, fmt.Errorf("purge users: %w", err)
		}
	}

	if err := importUsers(ctx, tx, app, res, log); err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}
	if err := importShelves(ctx, tx, app, res, log); err != nil {
		return nil, fmt.Errorf("shelves: %w", err)
	}
	if err := importKoboTokens(ctx, tx, app, res); err != nil {
		return nil, fmt.Errorf("kobo tokens: %w", err)
	}
	if err := importReadingState(ctx, tx, app, res); err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}

	for _, t := range []string{"users", "shelves"} {
		q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s','id'),
			GREATEST((SELECT COALESCE(max(id),0) FROM %s), 1))`, t, t)
		if _, err := tx.Exec(ctx, q); err != nil {
			return nil, err
		}
	}

	res.Elapsed = time.Since(start)
	if opts.DryRun {
		log.Warn("dry run: rolling back app.db import")
		return res, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Committed = true
	res.Elapsed = time.Since(start)
	return res, nil
}

func importUsers(ctx context.Context, tx pgx.Tx, app *AppDB, res *AppResult, log *slog.Logger) error {
	rows, err := app.db.Query(`SELECT id, name, email, role, COALESCE(locale,'en') FROM user`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, role int64
		var name string
		var email sql.NullString
		var locale string
		if err := rows.Scan(&id, &name, &email, &role, &locale); err != nil {
			return err
		}
		mapped, skip := mapRole(role)
		if skip {
			res.SkippedUser++
			log.Info("skipping calibre-web anonymous account", "name", name)
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			res.SkippedUser++
			continue
		}
		var em any
		if email.Valid && strings.TrimSpace(email.String) != "" {
			em = strings.TrimSpace(email.String)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, calibreweb_id, username, email, password_hash, role,
			                   locale, password_reset_required)
			VALUES ($1,$1,$2,$3,$4,$5,$6,true)`,
			id, name, em, unusablePasswordHash, mapped, locale); err != nil {
			return fmt.Errorf("user %q: %w", name, err)
		}
		res.Users++
	}
	return rows.Err()
}

func importShelves(ctx context.Context, tx pgx.Tx, app *AppDB, res *AppResult, log *slog.Logger) error {
	rows, err := app.db.Query(`
		SELECT s.id, s.uuid, s.name, COALESCE(s.is_public,0), s.user_id,
		       COALESCE(s.kobo_sync,0), s.created, s.last_modified
		FROM shelf s
		JOIN user u ON u.id = s.user_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type shelf struct {
		id, userID         int64
		uuid, name         string
		isPublic, koboSync bool
		created, lastMod   time.Time
	}
	var shelves []shelf

	for rows.Next() {
		var s shelf
		var uuid, name sql.NullString
		var pub, kobo int64
		var created, lastMod sql.NullString
		if err := rows.Scan(&s.id, &uuid, &name, &pub, &s.userID, &kobo, &created, &lastMod); err != nil {
			return err
		}
		s.name = strings.TrimSpace(name.String)
		if s.name == "" {
			continue
		}
		s.uuid = strings.TrimSpace(uuid.String)
		s.isPublic, s.koboSync = pub != 0, kobo != 0
		s.created, _ = parseCalibreTime(created)
		if s.created.IsZero() {
			s.created = time.Now().UTC()
		}
		s.lastMod, _ = parseCalibreTime(lastMod)
		if s.lastMod.IsZero() {
			s.lastMod = s.created
		}
		shelves = append(shelves, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, s := range shelves {
		// A shelf whose owner was skipped (Guest) has nowhere to live.
		var ownerExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, s.userID).Scan(&ownerExists); err != nil {
			return err
		}
		if !ownerExists {
			log.Warn("skipping shelf whose owner was not imported", "shelf", s.name)
			continue
		}
		var uuidArg any
		if s.uuid != "" {
			uuidArg = s.uuid
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO shelves (id, calibreweb_id, uuid, user_id, name, is_public,
			                     kobo_sync, created_at, updated_at)
			VALUES ($1,$1,COALESCE($2::uuid, gen_random_uuid()),$3,$4,$5,$6,$7,$8)`,
			s.id, uuidArg, s.userID, s.name, s.isPublic, s.koboSync, s.created, s.lastMod); err != nil {
			return fmt.Errorf("shelf %q: %w", s.name, err)
		}
		res.Shelves++
	}

	// Shelf membership. The JOIN against books silently drops links pointing at
	// books that are not in the library; count them so the drop is visible.
	linkRows, err := app.db.Query(`
		SELECT bsl.shelf, bsl.book_id, COALESCE(bsl."order",0), bsl.date_added
		FROM book_shelf_link bsl`)
	if err != nil {
		return err
	}
	defer linkRows.Close()

	for linkRows.Next() {
		var shelfID, bookID, order int64
		var added sql.NullString
		if err := linkRows.Scan(&shelfID, &bookID, &order, &added); err != nil {
			return err
		}
		at, ok := parseCalibreTime(added)
		if !ok {
			at = time.Now().UTC()
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO shelf_books (shelf_id, book_id, position, added_at)
			SELECT $1,$2,$3,$4
			WHERE EXISTS (SELECT 1 FROM shelves WHERE id=$1)
			  AND EXISTS (SELECT 1 FROM books   WHERE id=$2)
			ON CONFLICT DO NOTHING`, shelfID, bookID, int32(order), at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			res.OrphanLinks++
			continue
		}
		res.ShelfBooks++
	}
	return linkRows.Err()
}

func importKoboTokens(ctx context.Context, tx pgx.Tx, app *AppDB, res *AppResult) error {
	// token_type 1 is calibre-web's Kobo sync token; other types are unrelated.
	rows, err := app.db.Query(`
		SELECT auth_token, user_id FROM remote_auth_token WHERE token_type = 1`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var token string
		var userID int64
		if err := rows.Scan(&token, &userID); err != nil {
			return err
		}
		if strings.TrimSpace(token) == "" {
			continue
		}
		// Carrying the token across means the Kobo keeps working after only a
		// hostname change -- no re-pairing, no full resync.
		tag, err := tx.Exec(ctx, `
			INSERT INTO kobo_auth_tokens (user_id, token, label)
			SELECT $1,$2,'imported from calibre-web'
			WHERE EXISTS (SELECT 1 FROM users WHERE id=$1)
			ON CONFLICT (token) DO NOTHING`, userID, token)
		if err != nil {
			return err
		}
		res.KoboTokens += tag.RowsAffected()
	}
	return rows.Err()
}

func importReadingState(ctx context.Context, tx pgx.Tx, app *AppDB, res *AppResult) error {
	// calibre-web splits this across four tables; ours is one row per
	// (user, book), matching Kobo's own ReadingState object.
	rows, err := app.db.Query(`
		SELECT rs.user_id, rs.book_id, rs.last_modified, rs.priority_timestamp,
		       bm.progress_percent, bm.content_source_progress_percent,
		       bm.location_value, bm.location_type, bm.location_source,
		       st.spent_reading_minutes, st.remaining_time_minutes,
		       brl.read_status, brl.times_started_reading, brl.last_time_started_reading
		FROM kobo_reading_state rs
		LEFT JOIN kobo_bookmark   bm  ON bm.kobo_reading_state_id = rs.id
		LEFT JOIN kobo_statistics st  ON st.kobo_reading_state_id = rs.id
		LEFT JOIN book_read_link  brl ON brl.book_id = rs.book_id AND brl.user_id = rs.user_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var userID, bookID int64
		var lastMod, prio sql.NullString
		var progress, srcProgress sql.NullFloat64
		var locVal, locType, locSrc sql.NullString
		var spent, remaining sql.NullInt64
		var readStatus, timesStarted sql.NullInt64
		var lastStarted sql.NullString

		if err := rows.Scan(&userID, &bookID, &lastMod, &prio,
			&progress, &srcProgress, &locVal, &locType, &locSrc,
			&spent, &remaining, &readStatus, &timesStarted, &lastStarted); err != nil {
			return err
		}

		// calibre-web: 0 unread, 1 finished, 2 in progress.
		status := "ReadyToRead"
		switch readStatus.Int64 {
		case 1:
			status = "Finished"
		case 2:
			status = "Reading"
		}

		lm, ok := parseCalibreTime(lastMod)
		if !ok {
			lm = time.Now().UTC()
		}
		pt, ok := parseCalibreTime(prio)
		if !ok {
			pt = lm
		}
		var started any
		if t, ok := parseCalibreTime(lastStarted); ok {
			started = t
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO reading_state (
				user_id, book_id, status,
				progress_percent, content_source_progress_percent,
				location_value, location_type, location_source,
				spent_reading_minutes, remaining_time_minutes,
				times_started_reading, last_time_started_reading,
				priority_timestamp, last_modified)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14
			WHERE EXISTS (SELECT 1 FROM users WHERE id=$1)
			  AND EXISTS (SELECT 1 FROM books WHERE id=$2)
			ON CONFLICT (user_id, book_id) DO NOTHING`,
			userID, bookID, status,
			nullInt32(progress), nullInt32(srcProgress),
			nullStr(locVal), nullStr(locType), nullStr(locSrc),
			nullInt(spent), nullInt(remaining),
			int32(timesStarted.Int64), started, pt, lm)
		if err != nil {
			return fmt.Errorf("reading state user=%d book=%d: %w", userID, bookID, err)
		}
		res.ReadStates += tag.RowsAffected()
	}
	return rows.Err()
}

func nullInt32(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return int32(v.Float64)
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return int32(v.Int64)
}

func nullStr(v sql.NullString) any {
	if !v.Valid || v.String == "" {
		return nil
	}
	return v.String
}
