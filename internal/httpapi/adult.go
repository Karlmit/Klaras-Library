package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// maySeeAdult reports whether this request may see books flagged as adult.
//
// Administrators only, by decision: the flag exists so a shared household
// library does not put erotica in front of everyone, and there is no separate
// permission for it. If that changes, this is the one place to change.
func (s *Server) maySeeAdult(r *http.Request) bool {
	u := s.currentUser(r)
	return u != nil && u.Role == "admin"
}

// adultVisibility resolves the "adult" query parameter against the caller's
// role.
//
// A non-admin always gets AdultHide, whatever they ask for. Building the answer
// from the role rather than from the request is the difference between a filter
// and a permission.
func (s *Server) adultVisibility(r *http.Request) library.AdultVisibility {
	if !s.maySeeAdult(r) {
		return library.AdultHide
	}
	switch r.URL.Query().Get("adult") {
	case "only":
		return library.AdultOnly
	case "include":
		return library.AdultInclude
	default:
		return library.AdultHide
	}
}

// guardAdult returns true when the request must not see this book.
//
// Direct access by id is the hole a list filter leaves open: hiding a book from
// the grid means nothing if its download URL still works. Answering 404 rather
// than 403 keeps the flag itself private -- a reader learns nothing about what
// the library holds.
func (s *Server) guardAdult(w http.ResponseWriter, r *http.Request, id int64) bool {
	if s.maySeeAdult(r) {
		return false
	}
	var adult bool
	if err := s.db.Pool.QueryRow(r.Context(),
		`SELECT adult FROM books WHERE id=$1`, id).Scan(&adult); err != nil {
		return false // no such book: let the handler produce its own 404
	}
	if adult {
		writeErr(w, http.StatusNotFound, "not found")
		return true
	}
	return false
}

// handleSetAdult flags or clears one book. Administrators only.
func (s *Server) handleSetAdult(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	var in struct {
		Adult bool `json:"adult"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if err := s.lib.SetAdult(r.Context(), id, in.Adult); err != nil {
		s.fail(w, r, err, "set adult flag")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "adult": in.Adult})
}

// handleSetAdultMany is the review screen's bulk action.
func (s *Server) handleSetAdultMany(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs   []int64 `json:"ids"`
		Adult bool    `json:"adult"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	n, err := s.lib.SetAdultMany(r.Context(), in.IDs, in.Adult)
	if err != nil {
		s.fail(w, r, err, "set adult flags")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": n, "adult": in.Adult})
}
