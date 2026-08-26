package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleDownloadBook streams a book file to a signed-in browser.
//
// Kobo devices use their own endpoint under /kobo/{token}; this is the one the
// web UI links to, and it authenticates with the session cookie.
func (s *Server) handleDownloadBook(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	if s.guardAdult(w, r, id) {
		return
	}
	format := strings.ToUpper(chi.URLParam(r, "format"))

	info, err := s.lib.PathInfo(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "download lookup")
		return
	}

	var filename string
	for _, f := range info.Files {
		if strings.EqualFold(f.Format, format) {
			filename = f.Name
			break
		}
	}
	if filename == "" {
		writeErr(w, http.StatusNotFound, "this book has no "+format+" file")
		return
	}

	// Build the path through the same guard the file store uses, so a malformed
	// stored path cannot be turned into a traversal.
	abs, err := s.files.Abs(filepath.Join(info.Path, filename))
	if err != nil {
		s.log.Error("refusing unsafe download path", "book", id, "path", info.Path)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		s.log.Warn("book file missing on disk", "book", id, "path", abs)
		writeErr(w, http.StatusNotFound, "file missing on disk")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.fail(w, r, err, "stat book file")
		return
	}

	w.Header().Set("Content-Type", contentTypeFor(format))
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(filename))
	http.ServeContent(w, r, filename, st.ModTime(), f)
}

func contentTypeFor(format string) string {
	switch strings.ToUpper(format) {
	case "EPUB", "KEPUB":
		return "application/epub+zip"
	case "PDF":
		return "application/pdf"
	case "MOBI", "AZW3":
		return "application/x-mobipocket-ebook"
	case "CBZ":
		return "application/vnd.comicbook+zip"
	default:
		return "application/octet-stream"
	}
}
