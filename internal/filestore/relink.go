package filestore

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MissingFile is a book_files row with nothing behind it on disk.
type MissingFile struct {
	BookID   int64
	Title    string
	Format   string
	Dir      string
	Filename string
	Size     int64
}

// Missing lists every recorded file that is not where the database says it is.
//
// Cheap enough to run over the whole library -- one stat per file -- and the
// only check that compares the database against the disk rather than against
// itself.
func (s *Store) Missing(ctx context.Context) ([]MissingFile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.title, f.format, b.path, f.filename, COALESCE(f.size_bytes, 0)
		FROM book_files f JOIN books b ON b.id = f.book_id
		ORDER BY b.id, f.format`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MissingFile
	for rows.Next() {
		var m MissingFile
		if err := rows.Scan(&m.BookID, &m.Title, &m.Format, &m.Dir, &m.Filename, &m.Size); err != nil {
			return nil, err
		}
		abs, err := s.Abs(filepath.Join(m.Dir, m.Filename))
		if err != nil {
			out = append(out, m)
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// RelinkReport summarises a repair run.
type RelinkReport struct {
	Missing    int
	Relinked   int
	Ambiguous  int
	Unresolved int
	Elapsed    time.Duration
}

// Relink points books back at files that are on disk under another name.
//
// A file can go missing from the database's point of view without being lost:
// Calibre truncates long filenames and may leave a trailing space before the
// extension that the imported name does not carry, so the recorded name and
// the real one differ by a character nobody can see. Reorganize used to move
// the book's row anyway, leaving the row describing a location the file had
// never reached.
//
// Matching is by size and extension, not by name -- the name is the thing that
// is wrong. A single candidate of exactly the right size is taken; anything
// ambiguous is reported and left alone, because guessing which file belongs to
// a book is how libraries get silently scrambled.
func (s *Store) Relink(ctx context.Context, dryRun bool, out io.Writer, log *slog.Logger) (*RelinkReport, error) {
	start := time.Now()
	rep := &RelinkReport{}

	missing, err := s.Missing(ctx)
	if err != nil {
		return nil, err
	}
	rep.Missing = len(missing)
	if len(missing) == 0 {
		rep.Elapsed = time.Since(start)
		return rep, nil
	}

	// One walk of the library, indexed by size and extension. Files already
	// accounted for by a book are excluded so a relink cannot steal one.
	claimed, err := s.claimedPaths(ctx)
	if err != nil {
		return nil, err
	}
	type cand struct{ rel string }
	index := map[string][]cand{}
	root := s.root
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable corner must not abort the sweep
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || claimed[rel] {
			return nil
		}
		key := fmt.Sprintf("%d|%s", info.Size(), strings.ToLower(filepath.Ext(p)))
		index[key] = append(index[key], cand{rel: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, m := range missing {
		if m.Size == 0 {
			rep.Unresolved++
			fmt.Fprintf(out, "book %d %q: %s has no recorded size; cannot match\n",
				m.BookID, m.Title, m.Filename)
			continue
		}
		ext := strings.ToLower(filepath.Ext(m.Filename))
		hits := index[fmt.Sprintf("%d|%s", m.Size, ext)]
		switch {
		case len(hits) == 0:
			rep.Unresolved++
			fmt.Fprintf(out, "book %d %q: no file of %d bytes with extension %s anywhere in the library\n",
				m.BookID, m.Title, m.Size, ext)
			continue
		case len(hits) > 1:
			rep.Ambiguous++
			fmt.Fprintf(out, "book %d %q: %d files match %d bytes %s; leaving alone\n",
				m.BookID, m.Title, len(hits), m.Size, ext)
			for _, h := range hits {
				fmt.Fprintf(out, "      %s\n", h.rel)
			}
			continue
		}

		found := hits[0].rel
		dir, name := filepath.Split(found)
		dir = strings.TrimSuffix(filepath.ToSlash(dir), "/")

		fmt.Fprintf(out, "book %d %q\n  %s/%s  (recorded, absent)\n  %s/%s  (found)\n",
			m.BookID, m.Title, m.Dir, m.Filename, dir, name)
		if dryRun {
			rep.Relinked++
			continue
		}

		// The book moves to wherever its file actually is. That may be an old
		// Calibre folder; a later reorganize will file it properly, and will
		// now succeed because the recorded name matches the disk.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE books SET path=$2 WHERE id=$1`, m.BookID, dir); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE book_files SET filename=$3 WHERE book_id=$1 AND format=$2`,
			m.BookID, m.Format, name); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		delete(index, fmt.Sprintf("%d|%s", m.Size, ext))
		rep.Relinked++
		log.Info("relinked", "book", m.BookID, "to", found)
	}

	rep.Elapsed = time.Since(start)
	return rep, nil
}

// claimedPaths is every library-relative file path a book already points at.
func (s *Store) claimedPaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.path, f.filename FROM book_files f JOIN books b ON b.id = f.book_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var dir, name string
		if err := rows.Scan(&dir, &name); err != nil {
			return nil, err
		}
		p := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(s.root, p)); err == nil {
			out[p] = true
		}
	}
	return out, rows.Err()
}
