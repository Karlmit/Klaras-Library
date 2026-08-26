package httpapi

import (
	"encoding/json"
	"net/http"
)

// handleDiscoverDeck hands out the next few candidates.
func (s *Server) handleDiscoverDeck(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	cards, err := s.lib.DiscoverDeck(r.Context(), u.ID, queryInt(r, "limit", 8),
		s.adultVisibility(r))
	if err != nil {
		s.fail(w, r, err, "discover deck")
		return
	}
	stats, err := s.lib.DiscoverStatsFor(r.Context(), u.ID, s.adultVisibility(r))
	if err != nil {
		s.fail(w, r, err, "discover stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards, "stats": stats})
}

// handleDiscoverDecide records a keep, a pass, or a change of mind.
func (s *Server) handleDiscoverDecide(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var in struct {
		BookID int64  `json:"book_id"`
		Action string `json:"action"` // keep | pass | undo
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if in.BookID == 0 {
		writeErr(w, http.StatusBadRequest, "no book given")
		return
	}

	var err error
	switch in.Action {
	case "keep":
		err = s.lib.DiscoverKeep(r.Context(), u.ID, in.BookID)
	case "pass":
		err = s.lib.DiscoverPass(r.Context(), u.ID, in.BookID)
	case "undo":
		err = s.lib.DiscoverUndo(r.Context(), u.ID, in.BookID)
	default:
		writeErr(w, http.StatusBadRequest, "action must be keep, pass or undo")
		return
	}
	if err != nil {
		s.fail(w, r, err, "discover decide")
		return
	}
	stats, err := s.lib.DiscoverStatsFor(r.Context(), u.ID, s.adultVisibility(r))
	if err != nil {
		s.fail(w, r, err, "discover stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}
