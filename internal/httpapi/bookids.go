package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// maxSelectAll bounds a "select everything matching" request.
//
// Generous enough for any real filter -- the largest category in this library
// is 6,706 books -- while stopping someone selecting all 28,000 and then
// deleting them with one mistaken click.
const maxSelectAll = 20000

// handleBookIDs returns just the ids matching a filter.
//
// This is what makes "select all" work on a filtered view. Returning ids alone
// keeps the response small (a few hundred kB at worst) and lets the existing
// bulk endpoints stay a simple list of ids, rather than growing a second way
// of expressing "which books" that could drift from the first.
func (s *Server) handleBookIDs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := library.Filter{
		Query:       strings.TrimSpace(q.Get("q")),
		Author:      q.Get("author"),
		Tag:         q.Get("tag"),
		Series:      q.Get("series"),
		Language:    q.Get("language"),
		Format:      q.Get("format"),
		NeedsReview: queryBool(r, "needs_review"),
	}
	if sh := q.Get("shelf"); sh != "" {
		if id, err := strconv.ParseInt(sh, 10, 64); err == nil {
			f.ShelfID = id
		}
	}

	ids, truncated, err := s.lib.BookIDs(r.Context(), f, maxSelectAll)
	if err != nil {
		s.fail(w, r, err, "book ids")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ids": ids, "count": len(ids), "truncated": truncated, "limit": maxSelectAll,
	})
}
