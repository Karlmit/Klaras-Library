package httpapi

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/ingest"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// maxUploadBytes caps a single uploaded book. The largest file in a 28,000
// book library is a few hundred megabytes; this leaves room without letting a
// stray request fill the disk.
const maxUploadBytes = 512 << 20

// handleUploadBook accepts an ebook file through the browser.
//
// The heavy lifting is the ingest pipeline, not this handler: the upload is
// written to a temporary file and handed to the same code the watch folder
// uses, so it gets identical treatment -- content-hash dedupe, metadata read
// from the file itself, cover extracted, filed into the managed tree, KEPUB
// queued. Reimplementing any of that here would guarantee the two paths drift.
func (s *Server) handleUploadBook(w http.ResponseWriter, r *http.Request) {
	if s.ingest == nil {
		writeErr(w, http.StatusServiceUnavailable, "uploads are not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest,
			"could not read the upload; the file may be larger than 512 MB")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "no file was sent")
		return
	}

	type result struct {
		Filename string `json:"filename"`
		BookID   int64  `json:"book_id,omitempty"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(files))

	for _, fh := range files {
		res := result{Filename: fh.Filename}
		id, err := s.ingestOne(r, fh.Filename, fh)
		switch {
		case err == nil:
			res.BookID, res.Status = id, "imported"
		case strings.Contains(err.Error(), ingest.ErrDuplicate.Error()):
			res.BookID, res.Status = id, "duplicate"
		default:
			res.Status, res.Error = "failed", err.Error()
			s.log.Warn("upload failed", "file", fh.Filename, "err", err)
		}
		out = append(out, res)
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// ingestOne stages one uploaded file and runs it through the ingest pipeline.
func (s *Server) ingestOne(r *http.Request, name string, fh *multipart.FileHeader) (int64, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if !ingest.SupportedFormat(ext) {
		return 0, fmt.Errorf("%s is not a supported format", ext)
	}

	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()

	// Staged inside the ingest directory so the later move into the library is
	// a rename on the same filesystem rather than a copy of a large file.
	staging := filepath.Join(s.cfg.IngestDir, ".uploads")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(staging, "upload-*"+ext)
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	// Removed on every path: on failure it is rubbish, and on success the
	// pipeline has already moved it, so this is a no-op.
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}

	return s.ingest.Import(r.Context(), tmpName)
}

// handleReplaceCover swaps a book's cover image.
//
// Calibre keeps the cover as cover.jpg beside the book, and so do we, which is
// what keeps the tree readable by other tools. Whatever is uploaded is decoded
// and re-encoded as JPEG rather than trusted: it normalises PNG and WebP, and
// means a malformed file fails here instead of in every thumbnail worker.
func (s *Server) handleReplaceCover(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the image")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck

	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no image was sent")
		return
	}
	defer file.Close()

	info, err := s.lib.PathInfo(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "cover lookup")
		return
	}
	dir, err := s.files.Abs(info.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := covers.WriteSource(dir, file); err != nil {
		s.log.Warn("cover replace failed", "book", id, "err", err)
		writeErr(w, http.StatusBadRequest,
			"that file could not be read as an image")
		return
	}

	if _, err := s.db.Pool.Exec(r.Context(),
		`UPDATE books SET has_cover = true WHERE id = $1`, id); err != nil {
		s.fail(w, r, err, "mark cover")
		return
	}

	// Drop the stale thumbnails and rebuild, so the grid updates rather than
	// serving the old cover from cache until something else invalidates it.
	s.covers.Invalidate(info.UUID)
	if err := s.covers.Generate(info.Path, info.UUID); err != nil {
		// Not fatal: the source is in place and the worker will retry.
		_ = s.queue.Enqueue(r.Context(), jobs.KindThumbnail, info.UUID,
			covers.ThumbnailPayload{BookID: id, UUID: info.UUID, Path: info.Path}, 10)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cover replaced"})
}
