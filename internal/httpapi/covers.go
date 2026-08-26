package httpapi

import (
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Karlmit/Klaras-Library/internal/covers"
)

// handleCover serves a pre-generated thumbnail.
//
// The request path is a plain file read: the sizes are fixed and generated in
// the background, so nothing is decoded or resized here. That is the whole
// point -- resizing per request is one of the measured reasons calibre-web
// stalls on a large grid.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	if s.guardAdult(w, r, id) {
		return
	}
	sizeName := chi.URLParam(r, "size")
	if sizeName == "" {
		sizeName = "grid"
	}
	size, ok := covers.SizeByName(sizeName)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown size")
		return
	}

	info, err := s.lib.PathInfo(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "cover lookup")
		return
	}

	path, exists := s.covers.ThumbPath(info.UUID, size.Name)
	if !exists {
		// Generate on demand as a fallback -- a book added seconds ago may not
		// have been through the worker yet. The result is cached, so this
		// happens at most once per book per size.
		if err := s.covers.Generate(info.Path, info.UUID); err != nil {
			s.servePlaceholder(w, r, size.Width)
			return
		}
		path, exists = s.covers.ThumbPath(info.UUID, size.Name)
		if !exists {
			s.servePlaceholder(w, r, size.Width)
			return
		}
	}

	f, err := os.Open(path)
	if err != nil {
		s.servePlaceholder(w, r, size.Width)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.servePlaceholder(w, r, size.Width)
		return
	}

	// Covers are immutable for a given (book, size, mtime). An ETag lets the
	// browser skip the body on every revisit; the grid asks for 60 at a time.
	etag := `"` + strconv.FormatInt(st.ModTime().UnixNano(), 36) + "-" +
		strconv.FormatInt(st.Size(), 36) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", "image/jpeg")
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "cover.jpg", st.ModTime(), f)
}

func (s *Server) servePlaceholder(w http.ResponseWriter, r *http.Request, width int) {
	w.Header().Set("Content-Type", "image/jpeg")
	// Short cache: a cover may appear once the ingest pipeline catches up.
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Method == http.MethodHead {
		return
	}
	_ = covers.Placeholder(w, width)
}
