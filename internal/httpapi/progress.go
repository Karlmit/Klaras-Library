package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// ReadingProgress is what the browser reader stores and restores.
type ReadingProgress struct {
	Status   string  `json:"status"`
	Percent  *int    `json:"percent,omitempty"`
	Location *string `json:"location,omitempty"`
}

func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	if s.guardAdult(w, r, id) {
		return
	}
	u := s.currentUser(r)

	var p ReadingProgress
	err = s.db.Pool.QueryRow(r.Context(), `
		SELECT status, progress_percent, web_location
		FROM reading_state WHERE user_id=$1 AND book_id=$2`, u.ID, id).
		Scan(&p.Status, &p.Percent, &p.Location)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, ReadingProgress{Status: "ReadyToRead"})
		return
	}
	if err != nil {
		s.fail(w, r, err, "get progress")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePutProgress records where the browser reader is.
//
// It writes web_location, never location_value: that belongs to the Kobo, and
// overwriting a device's position with a CFI it cannot parse would lose the
// reader's place on the device. Percent and status are shared on purpose, so
// finishing a book in one place is reflected in the other.
func (s *Server) handlePutProgress(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	if s.guardAdult(w, r, id) {
		return
	}
	u := s.currentUser(r)

	var in ReadingProgress
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	switch in.Status {
	case "ReadyToRead", "Reading", "Finished":
	case "":
		in.Status = "Reading"
	default:
		writeErr(w, http.StatusBadRequest, "unknown status")
		return
	}

	if _, err := s.db.Pool.Exec(r.Context(), `
		INSERT INTO reading_state (user_id, book_id, status, progress_percent,
		                           web_location, times_started_reading,
		                           last_time_started_reading, last_modified)
		VALUES ($1,$2,$3,$4,$5,
		        CASE WHEN $3 = 'Reading' THEN 1 ELSE 0 END,
		        CASE WHEN $3 = 'Reading' THEN now() ELSE NULL END,
		        now())
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			status           = EXCLUDED.status,
			progress_percent = COALESCE(EXCLUDED.progress_percent, reading_state.progress_percent),
			web_location     = COALESCE(EXCLUDED.web_location, reading_state.web_location),
			last_time_finished = CASE WHEN EXCLUDED.status = 'Finished'
			                          THEN now() ELSE reading_state.last_time_finished END,
			last_modified    = now()`,
		u.ID, id, in.Status, in.Percent, in.Location); err != nil {
		s.fail(w, r, err, "save progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
