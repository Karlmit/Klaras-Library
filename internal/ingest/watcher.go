package ingest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/filestore"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
	"github.com/Karlmit/Klaras-Library/internal/kepub"
)

// supportedFormats are the extensions the watcher will pick up.
var supportedFormats = map[string]string{
	".epub": "EPUB", ".kepub": "KEPUB", ".pdf": "PDF",
	".mobi": "MOBI", ".azw3": "AZW3", ".cbz": "CBZ",
}

// Service imports files dropped into the ingest directory.
type Service struct {
	dir    string
	pool   *pgxpool.Pool
	files  *filestore.Store
	covers *covers.Service
	queue  *jobs.Queue
	log    *slog.Logger
}

// New builds the ingest service.
func New(dir string, pool *pgxpool.Pool, files *filestore.Store,
	cov *covers.Service, q *jobs.Queue, log *slog.Logger) *Service {
	return &Service{dir: dir, pool: pool, files: files, covers: cov, queue: q, log: log}
}

// Run watches the ingest directory until the context is cancelled.
//
// fsnotify plus a periodic sweep, not either alone: inotify does not fire for
// files written over SMB or NFS by another machine, which is exactly how books
// reach an Unraid share. The sweep is the reliable path; the watcher just makes
// the common case feel instant.
func (s *Service) Run(ctx context.Context, sweepEvery time.Duration) {
	if s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		s.log.Error("cannot create ingest directory", "dir", s.dir, "err", err)
		return
	}
	s.log.Info("watching for new books", "dir", s.dir)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Warn("filesystem notifications unavailable; using periodic sweep only", "err", err)
	} else {
		defer watcher.Close()
		if err := watcher.Add(s.dir); err != nil {
			s.log.Warn("cannot watch ingest directory", "err", err)
		}
	}

	sweep := time.NewTicker(sweepEvery)
	defer sweep.Stop()

	// Debounce: a large file arriving over SMB produces a stream of write
	// events, and importing it half-written would fail.
	var pending = map[string]time.Time{}
	debounce := time.NewTicker(2 * time.Second)
	defer debounce.Stop()

	var events chan fsnotify.Event
	var errs chan error
	if watcher != nil {
		events, errs = watcher.Events, watcher.Errors
	}

	for {
		select {
		case <-ctx.Done():
			return

		case ev := <-events:
			if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				pending[ev.Name] = time.Now()
			}

		case err := <-errs:
			if err != nil {
				s.log.Warn("watcher error", "err", err)
			}

		case <-debounce.C:
			now := time.Now()
			for p, seen := range pending {
				// Wait for quiet, then confirm the size has stopped growing.
				if now.Sub(seen) < 3*time.Second {
					continue
				}
				delete(pending, p)
				if s.ingestable(p) {
					s.importOne(ctx, p)
				}
			}

		case <-sweep.C:
			s.Sweep(ctx)
		}
	}
}

// ingestable reports whether a path is a finished file we should import.
//
// The directory check matters: moveAside creates failed/ and duplicates/ inside
// the watched directory, and the filesystem watcher reports those as new
// entries. Without this the service tries to import its own bookkeeping.
func (s *Service) ingestable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return false
	}
	if _, ok := supportedFormats[strings.ToLower(filepath.Ext(path))]; !ok {
		return false
	}
	return s.stable(path)
}

// stable reports whether a file has finished being written.
func (s *Service) stable(path string) bool {
	a, err := os.Stat(path)
	if err != nil {
		return false
	}
	time.Sleep(500 * time.Millisecond)
	b, err := os.Stat(path)
	if err != nil {
		return false
	}
	return a.Size() == b.Size() && a.Size() > 0
}

// Sweep imports everything currently sitting in the ingest directory.
func (s *Service) Sweep(ctx context.Context) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, ok := supportedFormats[strings.ToLower(filepath.Ext(e.Name()))]; !ok {
			continue
		}
		p := filepath.Join(s.dir, e.Name())
		if s.ingestable(p) {
			s.importOne(ctx, p)
		}
	}
}

// ErrDuplicate means the file is already in the library.
var ErrDuplicate = errors.New("already in the library")

// importOne imports a single file, logging rather than returning errors: the
// watcher must keep running whatever one bad file does.
func (s *Service) importOne(ctx context.Context, path string) {
	id, err := s.Import(ctx, path)
	switch {
	case errors.Is(err, ErrDuplicate):
		s.log.Info("skipping duplicate", "file", filepath.Base(path))
		s.moveAside(path, "duplicates")
	case err != nil:
		s.log.Error("ingest failed", "file", filepath.Base(path), "err", err)
		s.moveAside(path, "failed")
	default:
		s.log.Info("imported", "file", filepath.Base(path), "book", id)
	}
}

// moveAside parks a file that could not be imported, rather than deleting it or
// leaving it to be retried for ever.
func (s *Service) moveAside(path, sub string) {
	dir := filepath.Join(s.dir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	dst := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(dst); err == nil {
		dst = filepath.Join(dir, fmt.Sprintf("%d-%s", time.Now().Unix(), filepath.Base(path)))
	}
	if err := os.Rename(path, dst); err != nil {
		s.log.Warn("could not move file aside", "file", path, "err", err)
	}
}

// Import adds one file to the library and returns the new book id.
func (s *Service) Import(ctx context.Context, srcPath string) (int64, error) {
	ext := strings.ToLower(filepath.Ext(srcPath))
	format, ok := supportedFormats[ext]
	if !ok {
		return 0, fmt.Errorf("unsupported format %q", ext)
	}

	sum, err := hashFile(srcPath)
	if err != nil {
		return 0, err
	}

	// Content-hash dedupe, so the same book dropped twice under different
	// names is recognised.
	var existing int64
	err = s.pool.QueryRow(ctx,
		`SELECT book_id FROM book_files WHERE sha256=$1 LIMIT 1`, sum).Scan(&existing)
	if err == nil {
		return existing, ErrDuplicate
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	meta := &Metadata{Identifiers: map[string]string{}}
	if ext == ".epub" || ext == ".kepub" {
		if m, err := ReadEPUB(srcPath); err == nil {
			meta = m
		} else {
			s.log.Warn("could not read epub metadata; falling back to the filename",
				"file", filepath.Base(srcPath), "err", err)
		}
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	}
	if len(meta.Authors) == 0 {
		meta.Authors = []string{"Unknown"}
	}

	st, err := os.Stat(srcPath)
	if err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// A placeholder path is written first; the file store computes and applies
	// the real one once the row exists and its author is linked.
	var bookID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO books (uuid, title, description, languages, path, needs_review)
		VALUES (gen_random_uuid(), $1, NULLIF($2,''), $3, '_ingesting', false)
		RETURNING id`,
		meta.Title, meta.Description, langArray(meta.Language)).Scan(&bookID); err != nil {
		return 0, err
	}
	for i, name := range meta.Authors {
		var aid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO authors (name, sort) VALUES ($1, author_sort_of($1))
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, name).Scan(&aid); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO book_authors (book_id, author_id, position) VALUES ($1,$2,$3)`,
			bookID, aid, i); err != nil {
			return 0, err
		}
	}
	if meta.Series != "" {
		var sid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO series (name, sort) VALUES ($1,$1)
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, meta.Series).Scan(&sid); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE books SET series_id=$2, series_index=$3 WHERE id=$1`,
			bookID, sid, meta.SeriesIndex); err != nil {
			return 0, err
		}
	}
	if meta.Publisher != "" {
		var pid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO publishers (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, meta.Publisher).Scan(&pid); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE books SET publisher_id=$2 WHERE id=$1`, bookID, pid); err != nil {
			return 0, err
		}
	}
	if meta.PubDate != nil {
		if _, err := tx.Exec(ctx, `UPDATE books SET pubdate=$2 WHERE id=$1`, bookID, *meta.PubDate); err != nil {
			return 0, err
		}
	}
	for scheme, value := range meta.Identifiers {
		if _, err := tx.Exec(ctx,
			`INSERT INTO identifiers (book_id, scheme, value) VALUES ($1,$2,$3)
			 ON CONFLICT DO NOTHING`, bookID, scheme, value); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO book_files (book_id, format, filename, size_bytes, sha256)
		VALUES ($1,$2,$3,$4,$5)`,
		bookID, format, filepath.Base(srcPath), st.Size(), sum); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	// Put the file where the template says it belongs.
	//
	// Everything from here on happens after the row is committed, because the
	// path template is computed from the stored metadata. If any of it fails
	// the row must be removed again: a book whose file never arrived would sit
	// in the library at path '_ingesting', and its recorded content hash would
	// make every retry look like a duplicate.
	placed := false
	defer func() {
		if !placed {
			if _, err := s.pool.Exec(context.WithoutCancel(ctx),
				`DELETE FROM books WHERE id=$1`, bookID); err != nil {
				s.log.Error("could not roll back a failed ingest; "+
					"the library now holds a book with no file",
					"book", bookID, "err", err)
			}
		}
	}()

	plan, err := s.files.PlanFor(ctx, bookID)
	if err != nil {
		return 0, err
	}
	destDir, err := s.files.Abs(plan.ToDir)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", plan.ToDir, err)
	}
	destName := plan.Files[0].ToName
	if err := os.Rename(srcPath, filepath.Join(destDir, destName)); err != nil {
		// Cross-device: copy then remove.
		if err := copyFile(srcPath, filepath.Join(destDir, destName)); err != nil {
			return 0, err
		}
		_ = os.Remove(srcPath)
	}
	placed = true
	if _, err := s.pool.Exec(ctx,
		`UPDATE books SET path=$2 WHERE id=$1`, bookID, plan.ToDir); err != nil {
		return bookID, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE book_files SET filename=$2 WHERE book_id=$1`, bookID, destName); err != nil {
		return bookID, err
	}

	// Cover, then thumbnails and a KEPUB, all in the background.
	var uuid string
	if err := s.pool.QueryRow(ctx, `SELECT uuid::text FROM books WHERE id=$1`, bookID).
		Scan(&uuid); err == nil {
		if meta.CoverPath != "" {
			s.saveCover(ctx, bookID, filepath.Join(destDir, destName), meta.CoverPath, destDir)
		}
		_ = s.queue.Enqueue(ctx, jobs.KindThumbnail, uuid,
			covers.ThumbnailPayload{BookID: bookID, UUID: uuid, Path: plan.ToDir}, 100)
		if format == "EPUB" {
			_ = s.queue.Enqueue(ctx, jobs.KindKepub, uuid,
				kepub.Payload{BookID: bookID, UUID: uuid,
					SrcPath: filepath.Join(destDir, destName)}, 100)
		}
	}
	return bookID, nil
}

func (s *Service) saveCover(ctx context.Context, bookID int64, epubPath, coverInZip, destDir string) {
	out, err := os.Create(filepath.Join(destDir, "cover.jpg"))
	if err != nil {
		return
	}
	defer out.Close()
	if err := ExtractCover(epubPath, coverInZip, out); err != nil {
		s.log.Debug("no cover extracted", "book", bookID, "err", err)
		return
	}
	if _, err := s.pool.Exec(ctx, `UPDATE books SET has_cover=true WHERE id=$1`, bookID); err != nil {
		s.log.Warn("could not record cover", "book", bookID, "err", err)
	}
}

func langArray(l string) []string {
	if l == "" {
		return []string{}
	}
	return []string{l}
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
