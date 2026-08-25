package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Karlmit/Klaras-Library/internal/auth"
)

// Shelf is a user's collection of books.
type Shelf struct {
	ID        int64  `json:"id"`
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	IsPublic  bool   `json:"is_public"`
	KoboSync  bool   `json:"kobo_sync"`
	BookCount int64  `json:"book_count"`
	Owner     string `json:"owner"`
	Mine      bool   `json:"mine"`
	// KoboSubscribed is set on a shelf someone else owns that this user has
	// chosen to receive on their own device.
	KoboSubscribed bool `json:"kobo_subscribed"`
}

func (s *Server) handleListShelves(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT sh.id, sh.uuid::text, sh.name, sh.is_public, sh.kobo_sync,
		       (SELECT count(*) FROM shelf_books sb WHERE sb.shelf_id = sh.id),
		       us.username, (sh.user_id = $1) AS mine,
		       (sub.user_id IS NOT NULL) AS kobo_subscribed
		FROM shelves sh
		JOIN users us ON us.id = sh.user_id
		LEFT JOIN shelf_kobo_subscriptions sub
		       ON sub.shelf_id = sh.id AND sub.user_id = $1
		WHERE sh.user_id = $1 OR sh.is_public
		ORDER BY mine DESC, sh.position, lower(sh.name)`, u.ID)
	if err != nil {
		s.fail(w, r, err, "list shelves")
		return
	}
	defer rows.Close()

	out := []Shelf{}
	for rows.Next() {
		var sh Shelf
		if err := rows.Scan(&sh.ID, &sh.UUID, &sh.Name, &sh.IsPublic, &sh.KoboSync,
			&sh.BookCount, &sh.Owner, &sh.Mine, &sh.KoboSubscribed); err != nil {
			s.fail(w, r, err, "scan shelf")
			return
		}
		out = append(out, sh)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shelves": out})
}

func (s *Server) handleCreateShelf(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var in struct {
		Name     string `json:"name"`
		IsPublic bool   `json:"is_public"`
		KoboSync bool   `json:"kobo_sync"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	var sh Shelf
	err := s.db.Pool.QueryRow(r.Context(), `
		INSERT INTO shelves (user_id, name, is_public, kobo_sync)
		VALUES ($1,$2,$3,$4)
		RETURNING id, uuid::text, name, is_public, kobo_sync`,
		u.ID, in.Name, in.IsPublic, in.KoboSync).
		Scan(&sh.ID, &sh.UUID, &sh.Name, &sh.IsPublic, &sh.KoboSync)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			writeErr(w, http.StatusConflict, "you already have a shelf with that name")
			return
		}
		s.fail(w, r, err, "create shelf")
		return
	}
	sh.Mine, sh.Owner = true, u.Username
	writeJSON(w, http.StatusCreated, sh)
}

// ownsShelf reports whether the user may modify a shelf.
func (s *Server) ownsShelf(r *http.Request, shelfID int64) (bool, error) {
	u := s.currentUser(r)
	var ownerID int64
	err := s.db.Pool.QueryRow(r.Context(),
		`SELECT user_id FROM shelves WHERE id=$1`, shelfID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// An admin can tidy anyone's shelves; otherwise only the owner.
	return ownerID == u.ID || u.Can(auth.RoleAdmin), nil
}

func (s *Server) handleUpdateShelf(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad shelf id")
		return
	}
	ok, err := s.ownsShelf(r, id)
	if err != nil {
		s.fail(w, r, err, "shelf ownership")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "not your shelf")
		return
	}

	var in struct {
		Name     *string `json:"name"`
		IsPublic *bool   `json:"is_public"`
		KoboSync *bool   `json:"kobo_sync"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	// updated_at drives Kobo collection sync, so touching it here is what makes
	// a renamed shelf reach the device.
	if _, err := s.db.Pool.Exec(r.Context(), `
		UPDATE shelves SET
			name       = COALESCE($2, name),
			is_public  = COALESCE($3, is_public),
			kobo_sync  = COALESCE($4, kobo_sync),
			updated_at = now()
		WHERE id = $1`, id, in.Name, in.IsPublic, in.KoboSync); err != nil {
		s.fail(w, r, err, "update shelf")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteShelf(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad shelf id")
		return
	}
	ok, err := s.ownsShelf(r, id)
	if err != nil {
		s.fail(w, r, err, "shelf ownership")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "not your shelf")
		return
	}

	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err, "begin")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	// Leave a tombstone before deleting: once the row is gone there is nothing
	// left to tell the device the collection disappeared.
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO deleted_shelves (uuid, user_id)
		SELECT uuid, user_id FROM shelves WHERE id=$1
		ON CONFLICT (uuid) DO UPDATE SET deleted_at = now()`, id); err != nil {
		s.fail(w, r, err, "tombstone shelf")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM shelves WHERE id=$1`, id); err != nil {
		s.fail(w, r, err, "delete shelf")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleShelfBooks(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad shelf id")
		return
	}
	ok, err := s.ownsShelf(r, id)
	if err != nil {
		s.fail(w, r, err, "shelf ownership")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "not your shelf")
		return
	}

	var in struct {
		Add    []int64 `json:"add"`
		Remove []int64 `json:"remove"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err, "begin")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	if len(in.Add) > 0 {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO shelf_books (shelf_id, book_id)
			SELECT $1, b.id FROM books b WHERE b.id = ANY($2)
			ON CONFLICT DO NOTHING`, id, in.Add); err != nil {
			s.fail(w, r, err, "add to shelf")
			return
		}
		// Removing then re-adding a book must clear the old tombstone, or the
		// device would be told to drop a book that is back on the shelf.
		if _, err := tx.Exec(r.Context(),
			`DELETE FROM shelf_book_removals WHERE shelf_id=$1 AND book_id = ANY($2)`,
			id, in.Add); err != nil {
			s.fail(w, r, err, "clear removals")
			return
		}
	}
	if len(in.Remove) > 0 {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO shelf_book_removals (shelf_id, book_id, user_id)
			SELECT $1, unnest($2::bigint[]), (SELECT user_id FROM shelves WHERE id=$1)
			ON CONFLICT (shelf_id, book_id) DO UPDATE SET removed_at = now()`,
			id, in.Remove); err != nil {
			s.fail(w, r, err, "tombstone removals")
			return
		}
		if _, err := tx.Exec(r.Context(),
			`DELETE FROM shelf_books WHERE shelf_id=$1 AND book_id = ANY($2)`,
			id, in.Remove); err != nil {
			s.fail(w, r, err, "remove from shelf")
			return
		}
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE shelves SET updated_at = now() WHERE id=$1`, id); err != nil {
		s.fail(w, r, err, "touch shelf")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err, "commit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "updated", "added": len(in.Add), "removed": len(in.Remove),
	})
}

// handleKoboSubscribe opts this user's devices into a shelf someone else owns.
//
// Deliberately separate from the owner's kobo_sync flag: that one means "send
// this to MY devices", so toggling it on someone else's behalf would either do
// nothing or change what their reader receives. A subscription belongs to the
// subscriber alone.
func (s *Server) handleKoboSubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad shelf id")
		return
	}
	u := s.currentUser(r)

	var isPublic, mine bool
	if err := s.db.Pool.QueryRow(r.Context(),
		`SELECT is_public, user_id = $2 FROM shelves WHERE id = $1`, id, u.ID).
		Scan(&isPublic, &mine); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "no such shelf")
			return
		}
		s.fail(w, r, err, "shelf lookup")
		return
	}
	if mine {
		writeErr(w, http.StatusBadRequest,
			"this is your own shelf; use the Sync to Kobo toggle instead")
		return
	}
	if !isPublic {
		writeErr(w, http.StatusForbidden, "that shelf is not shared with you")
		return
	}

	if r.Method == http.MethodDelete {
		if _, err := s.db.Pool.Exec(r.Context(),
			`DELETE FROM shelf_kobo_subscriptions WHERE user_id=$1 AND shelf_id=$2`,
			u.ID, id); err != nil {
			s.fail(w, r, err, "unsubscribe")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
		return
	}

	if _, err := s.db.Pool.Exec(r.Context(),
		`INSERT INTO shelf_kobo_subscriptions (user_id, shelf_id) VALUES ($1,$2)
		 ON CONFLICT DO NOTHING`, u.ID, id); err != nil {
		s.fail(w, r, err, "subscribe")
		return
	}
	// Touch the shelf so the next sync sees it as changed and sends the
	// collection, rather than waiting for an unrelated edit.
	if _, err := s.db.Pool.Exec(r.Context(),
		`UPDATE shelves SET updated_at = now() WHERE id = $1`, id); err != nil {
		s.log.Warn("could not touch shelf after subscribe", "shelf", id, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "subscribed"})
}

// handleKoboToken issues a sync token for the current user.
func (s *Server) handleKoboToken(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var in struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&in)

	token, err := s.auth.NewKoboToken(r.Context(), u.ID, in.Label)
	if err != nil {
		s.fail(w, r, err, "create kobo token")
		return
	}
	base := strings.TrimRight(s.cfg.ExternalURL, "/")
	if base == "" {
		base = "https://YOUR-PUBLIC-URL"
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"token": token,
		// This exact string goes into the device's api_store setting.
		"api_store_url": base + "/kobo/" + token,
	})
}

func (s *Server) handleListKoboTokens(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	rows, err := s.db.Pool.Query(r.Context(), `
		SELECT id, label, created_at, last_used_at FROM kobo_auth_tokens
		WHERE user_id=$1 ORDER BY created_at`, u.ID)
	if err != nil {
		s.fail(w, r, err, "list kobo tokens")
		return
	}
	defer rows.Close()

	type tok struct {
		ID         int64   `json:"id"`
		Label      string  `json:"label"`
		CreatedAt  string  `json:"created_at"`
		LastUsedAt *string `json:"last_used_at"`
	}
	out := []tok{}
	for rows.Next() {
		var (
			t       tok
			created time.Time
			used    *time.Time
		)
		if err := rows.Scan(&t.ID, &t.Label, &created, &used); err != nil {
			s.fail(w, r, err, "scan token")
			return
		}
		t.CreatedAt = created.UTC().Format(time.RFC3339)
		if used != nil {
			formatted := used.UTC().Format(time.RFC3339)
			t.LastUsedAt = &formatted
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, err, "list kobo tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}
