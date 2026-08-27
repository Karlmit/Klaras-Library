package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Karlmit/Klaras-Library/internal/kepub"
	"github.com/Karlmit/Klaras-Library/internal/library"
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
		// A KEPUB is derivable rather than missing: kepubify makes one from the
		// EPUB in about a second, and the result is cached under the source's
		// hash, so asking for it is enough. Only books put on a Kobo shelf are
		// converted ahead of time -- converting all 28,000 would be tens of
		// gigabytes of files nobody reads.
		if strings.EqualFold(format, "KEPUB") {
			s.serveConvertedKepub(w, r, id, info)
			return
		}
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

	name := filename
	if strings.EqualFold(format, "KEPUB") {
		name = kepubDownloadName(filename)
	}
	w.Header().Set("Content-Type", contentTypeFor(format))
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// kepubDownloadName gives a KEPUB the name a Kobo needs.
//
// Calibre stores these as "Title - Author.kepub" and that is how they sit in
// the library, but a Kobo only treats a sideloaded file as a KEPUB when it ends
// .kepub.epub -- named anything else it is either read as an ordinary EPUB or
// not indexed at all. The file is unchanged; only what the browser calls it is.
func kepubDownloadName(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.EqualFold(filepath.Ext(base), ".kepub") {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base + ".kepub.epub"
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

// serveConvertedKepub makes a KEPUB from a book's EPUB and streams it.
//
// Synchronous on purpose. The conversion takes about a second, which is less
// than the download that follows it, so waiting is simpler and truer than a
// job plus a progress indicator plus somewhere to report that it failed.
func (s *Server) serveConvertedKepub(
	w http.ResponseWriter, r *http.Request, id int64, info *library.BookPathInfo,
) {
	var epub string
	for _, f := range info.Files {
		if strings.EqualFold(f.Format, "EPUB") {
			epub = f.Name
			break
		}
	}
	if epub == "" {
		writeErr(w, http.StatusNotFound, "this book has no EPUB to convert")
		return
	}

	src, err := s.files.Abs(filepath.Join(info.Path, epub))
	if err != nil {
		s.log.Error("refusing unsafe download path", "book", id, "path", info.Path)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	out, err := s.kepub.Convert(r.Context(), info.UUID, src)
	if err != nil {
		s.log.Warn("converting kepub for download", "book", id, "src", src, "err", err)
		// Three different problems needing three different answers: a missing
		// file is a library that has moved underneath us, a refusal is this
		// particular book, and a cache failure is this server. One message for
		// all three sends someone to look in the wrong place.
		switch {
		case errors.Is(err, kepub.ErrNoSource):
			writeErr(w, http.StatusNotFound,
				"the EPUB for this book is not on disk: "+epub)
		case errors.Is(err, kepub.ErrCache):
			writeErr(w, http.StatusInternalServerError,
				"converted, but it could not be saved; check the cache volume is writable")
		default:
			// The reason is worth showing: this is an editors-only endpoint on
			// a private server, and "it failed" is not something anyone can act on.
			writeErr(w, http.StatusUnprocessableEntity,
				"kepubify could not convert this EPUB: "+conversionReason(err))
		}
		return
	}

	f, err := os.Open(out)
	if err != nil {
		s.fail(w, r, err, "open converted kepub")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.fail(w, r, err, "stat converted kepub")
		return
	}

	name := kepubDownloadName(epub)
	w.Header().Set("Content-Type", contentTypeFor("KEPUB"))
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// conversionReason trims the wrapper off a kepubify error so the message reads
// as a sentence rather than as a stack of prefixes.
func conversionReason(err error) string {
	msg := err.Error()
	if _, rest, found := strings.Cut(msg, ": "); found {
		if _, tail, ok := strings.Cut(rest, ": "); ok {
			return tail
		}
		return rest
	}
	return msg
}
