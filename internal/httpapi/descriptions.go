package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Karlmit/Klaras-Library/internal/provider"
)

// descRunning guards the "run now" button: the nightly job and a hand-started
// run doing the same books at once would spend quota twice for one result.
var descRunning atomic.Bool

// handleDescriptionStatus answers "is it running, what has it done, and when
// will it be finished" without anyone reading a container log.
func (s *Server) handleDescriptionStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.lib.DescriptionStatusFor(r.Context())
	if err != nil {
		s.fail(w, r, err, "description status")
		return
	}
	st.GoogleEnabled = s.cfg.GoogleBooksKey != ""
	st.Running = descRunning.Load()
	writeJSON(w, http.StatusOK, st)
}

// handleDescriptionRun starts a pass now rather than waiting for tonight.
//
// Returns immediately: reading several thousand EPUBs takes minutes, and a
// request that hangs for minutes is a request that times out somewhere. The
// screen polls the status instead.
func (s *Server) handleDescriptionRun(w http.ResponseWriter, r *http.Request) {
	if !descRunning.CompareAndSwap(false, true) {
		writeErr(w, http.StatusConflict, "a run is already in progress")
		return
	}
	go func() {
		defer descRunning.Store(false)
		// Detached from the request: the run outlives the click that started it.
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()

		if _, err := s.lib.FillFromFiles(ctx, s.cfg.LibraryRoot, 1000000, false, s.log); err != nil {
			s.log.Warn("description run: files", "err", err)
		}
		if s.cfg.GoogleBooksKey == "" {
			return
		}
		set := provider.NewSetWithKey("swe", s.cfg.GoogleBooksKey)
		if _, err := s.lib.FillFromGoogle(ctx, set, s.cfg.DescriptionsPerDay, false, s.log); err != nil {
			s.log.Warn("description run: google", "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started"})
}
