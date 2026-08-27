// Package kepub converts EPUB files to Kobo's KEPUB format.
//
// Conversion runs in-process via pgaskin/kepubify rather than shelling out to a
// binary, which is the main reason this project is written in Go.
//
// Crucially, conversion never happens inside a sync request. calibre-web
// converts on demand while the Kobo waits, and the device gives up after about
// 30 seconds; here a background worker converts ahead of time into a cache and
// the sync response only advertises KEPUB once the file exists.
package kepub

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pgaskin/kepubify/v4/kepub"

	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// Service converts and caches KEPUB files.
type Service struct {
	libraryRoot string
	cacheDir    string
	conv        *kepub.Converter
}

// New builds a conversion service.
func New(libraryRoot, cacheDir string) *Service {
	return &Service{
		libraryRoot: libraryRoot,
		cacheDir:    filepath.Join(cacheDir, "kepub"),
		conv:        kepub.NewConverter(),
	}
}

// The three ways converting fails, kept apart because they need three
// different answers. "Could not be converted" covering all of them sends
// someone looking at a book when the fault is a read-only cache volume.
var (
	// ErrNoSource: the EPUB is missing from disk, or is not a readable zip.
	ErrNoSource = errors.New("source epub not found")
	// ErrConvert: kepubify refused the book's own contents.
	ErrConvert = errors.New("epub could not be converted")
	// ErrCache: the conversion could not be written. A fault on this side.
	ErrCache = errors.New("conversion cache not writable")
)

// cachePath keys the converted file on the source content hash, so an edited or
// replaced EPUB automatically misses the cache instead of serving a stale
// conversion. Two-level fan-out keeps directories small.
func (s *Service) cachePath(uuid, srcHash string) string {
	return filepath.Join(s.cacheDir, srcHash[:2], fmt.Sprintf("%s-%s.kepub.epub", uuid, srcHash[:16]))
}

// HashFile returns the SHA-256 of a file as hex.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Cached reports the cached KEPUB for a book, if one exists for the current
// source content.
func (s *Service) Cached(uuid, srcPath string) (string, bool) {
	hash, err := HashFile(srcPath)
	if err != nil {
		return "", false
	}
	p := s.cachePath(uuid, hash)
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p, true
	}
	return "", false
}

// Convert produces a KEPUB for one book and returns the cached path.
func (s *Service) Convert(ctx context.Context, uuid, srcPath string) (string, error) {
	st, err := os.Stat(srcPath)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoSource, srcPath)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%w: %s is a directory", ErrNoSource, srcPath)
	}

	hash, err := HashFile(srcPath)
	if err != nil {
		return "", err
	}
	out := s.cachePath(uuid, hash)
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, nil // already converted
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("%w: %v", ErrCache, err)
	}

	// Convert to a temporary file and rename into place, so a crash or a
	// concurrent reader never sees a partially written book.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*.kepub.epub")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCache, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// An EPUB is a zip, and Convert takes an fs.FS. Passing the zip reader
	// directly matters: kepubify special-cases it to preserve the original
	// file headers and compression, so unchanged entries are copied rather
	// than re-compressed.
	zr, err := zip.OpenReader(srcPath)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("%w: %s is not a readable epub: %v", ErrNoSource, filepath.Base(srcPath), err)
	}
	defer zr.Close()

	if err := s.conv.Convert(ctx, tmp, zr); err != nil {
		tmp.Close()
		// Some of this library's EPUBs carry characters XML forbids -- one had
		// two NUL bytes padding the end of its content.opf, after </package>,
		// which every reader ignores and no XML parser will accept. Rather than
		// declare the book unconvertible, strip them from a copy and try once
		// more. The book on disk is never touched.
		if out, n, ok := s.convertRepaired(ctx, uuid, srcPath, out); ok {
			slog.Info("converted after removing illegal XML characters",
				"book", filepath.Base(srcPath), "removed", n)
			return out, nil
		}
		return "", fmt.Errorf("%w: %s: %v", ErrConvert, filepath.Base(srcPath), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, out); err != nil {
		return "", fmt.Errorf("%w: %v", ErrCache, err)
	}
	return out, nil
}

// Payload is the job payload for a conversion.
type Payload struct {
	BookID  int64  `json:"book_id"`
	UUID    string `json:"uuid"`
	SrcPath string `json:"src_path"`
}

// Handler processes conversion jobs.
func (s *Service) Handler() jobs.Handler {
	return func(ctx context.Context, j *jobs.Job) error {
		var p Payload
		if err := j.Decode(&p); err != nil {
			return fmt.Errorf("%w: bad payload: %v", jobs.ErrPermanent, err)
		}
		_, err := s.Convert(ctx, p.UUID, p.SrcPath)
		if errors.Is(err, ErrNoSource) {
			// The file is gone; retrying will not bring it back.
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		}
		return err
	}
}
