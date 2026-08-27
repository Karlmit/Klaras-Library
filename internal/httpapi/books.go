package httpapi

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// parseBookFilter reads the filter half of a books query.
//
// Shared by the grid and by select-all, and shared deliberately rather than
// written twice. When they were parsed separately, select-all was missed when
// the adult filter was added: it went on returning 20,000 ordinary books while
// the grid showed 1,860 flagged ones, and the next button along from select-all
// is Delete. Two parsers for one question will disagree eventually; the only
// question is which of them is holding the delete button when it happens.
//
// Paging and sorting are not here: they belong to the grid, and select-all
// takes every match by definition.
func (s *Server) parseBookFilter(r *http.Request) library.Filter {
	q := r.URL.Query()
	f := library.Filter{
		Query:       strings.TrimSpace(q.Get("q")),
		Author:      q.Get("author"),
		Tag:         q.Get("tag"),
		Series:      q.Get("series"),
		Language:    q.Get("language"),
		Format:      q.Get("format"),
		NeedsReview: queryBool(r, "needs_review"),
		Adult:       s.adultVisibility(r),
	}
	if sh := q.Get("shelf"); sh != "" {
		if id, err := strconv.ParseInt(sh, 10, 64); err == nil {
			f.ShelfID = id
		}
	}
	return f
}

// handleListBooks serves the grid.
func (s *Server) handleListBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	f := s.parseBookFilter(r)
	f.Sort = library.SortMode(q.Get("sort"))
	f.Limit = queryInt(r, "limit", 60)
	f.Cursor = q.Get("cursor")
	f.WithTotal = queryBool(r, "total")

	// A text query defaults to relevance ordering; anything else keeps its
	// stable keyset sort.
	if f.Sort == "" {
		if f.Query != "" {
			f.Sort = library.SortRelevant
		} else {
			f.Sort = library.SortTitle
		}
	}

	page, err := s.lib.ListBooks(r.Context(), f)
	if err != nil {
		if strings.Contains(err.Error(), "cursor") || strings.Contains(err.Error(), "unknown sort") {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.fail(w, r, err, "list books")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleGetBook serves the detail view.
func (s *Server) handleGetBook(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	if s.guardAdult(w, r, id) {
		return
	}
	var userID int64
	if u := s.currentUser(r); u != nil {
		userID = u.ID
	}
	b, err := s.lib.GetBook(r.Context(), id, userID)
	if err != nil {
		s.fail(w, r, err, "get book")
		return
	}
	// A KEPUB may exist in the conversion cache without being a file of the
	// book's own -- that is what happens to everything on a Kobo shelf. Saying
	// "not converted" for those would make the marker lie to the one person it
	// is there for.
	if hasFormat(b.Files, "EPUB") && !hasFormat(b.Files, "KEPUB") {
		if src, err := s.files.Abs(filepath.Join(b.Path, formatFile(b.Files, "EPUB"))); err == nil {
			if _, ok := s.kepub.Cached(b.UUID, src); ok {
				b.KepubReady = true
			}
		}
	}

	// Read from the cover itself: what the grid serves is a 200px thumbnail, and
	// quoting its size when comparing against a candidate would be misleading.
	if b.HasCover {
		if cw, ch, ok := s.covers.SourceSize(b.Path); ok {
			b.CoverW, b.CoverH = cw, ch
		}
	}
	writeJSON(w, http.StatusOK, b)
}

// handleFacets serves the sidebar.
func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request) {
	f, err := s.lib.Facets(r.Context(), queryInt(r, "limit", 50))
	if err != nil {
		s.fail(w, r, err, "facets")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleSuggest serves search-as-you-type.
func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	out, err := s.lib.Suggest(r.Context(), r.URL.Query().Get("q"), queryInt(r, "limit", 10))
	if err != nil {
		s.fail(w, r, err, "suggest")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

func hasFormat(files []library.BookFile, format string) bool {
	for _, f := range files {
		if strings.EqualFold(f.Format, format) {
			return true
		}
	}
	return false
}

func formatFile(files []library.BookFile, format string) string {
	for _, f := range files {
		if strings.EqualFold(f.Format, format) {
			return f.Name
		}
	}
	return ""
}
