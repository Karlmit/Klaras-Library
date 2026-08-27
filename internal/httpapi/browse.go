package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// handleAuthors lists every author with a book count.
func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	out, err := s.lib.Authors(r.Context())
	if err != nil {
		s.fail(w, r, err, "list authors")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": out})
}

// handleSeries lists every series with a book count and a few cover ids.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	out, err := s.lib.Series(r.Context())
	if err != nil {
		s.fail(w, r, err, "list series")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": out})
}

// handleAuthorPortrait serves an author's picture, fetching it the first time.
//
// 404 is an ordinary answer here rather than a failure: most authors in a
// library this size are not on Wikidata, and the grid draws its own initials
// when there is no photograph.
func (s *Server) handleAuthorPortrait(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	path, err := s.lib.PortraitPath(r.Context(), s.cfg.CacheDir, id)
	if errors.Is(err, library.ErrNoPortrait) {
		// Cached: the miss is permanent until something re-runs the lookup, so
		// the browser should not ask again on the next scroll either.
		w.Header().Set("Cache-Control", "private, max-age=86400")
		writeErr(w, http.StatusNotFound, "no portrait")
		return
	}
	if err != nil {
		s.fail(w, r, err, "author portrait")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}

// handleMergeTags renames a category, folding it into an existing one if the
// new name is already in use.
//
// The library has 1,113 categories, many of them the same thing written twice
// -- "F" on 6,706 books and "Fiction" on others. Fixing that by selecting every
// book and editing them one bulk at a time is the long way round: the category
// itself is what is wrong, so it is the category that gets renamed.
func (s *Server) handleMergeTags(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From []string `json:"from"`
		To   string   `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	to := strings.TrimSpace(in.To)
	from := make([]string, 0, len(in.From))
	for _, f := range in.From {
		if f = strings.TrimSpace(f); f != "" && f != to {
			from = append(from, f)
		}
	}
	if len(from) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to rename")
		return
	}
	if to == "" {
		// Renaming to nothing is how a category is deleted, which is a real
		// need for the junk ones -- but it must be asked for explicitly.
		if !queryBool(r, "delete") {
			writeErr(w, http.StatusBadRequest, "give a new name, or pass delete=true to remove it")
			return
		}
	}

	n, err := s.lib.MergeTags(r.Context(), from, to)
	if err != nil {
		s.fail(w, r, err, "merge categories")
		return
	}
	// The category list is a materialised view refreshed every half minute, so
	// without this the panel reloads and still shows the names it has just
	// merged away -- which reads as the merge having failed. A refresh here
	// costs a moment and makes the answer honest.
	if _, err := s.lib.RefreshFacets(r.Context(), true); err != nil {
		s.log.Warn("facet refresh after merge failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "books": n})
}
