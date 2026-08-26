package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store performs journalled file operations on the managed tree.
//
// Every move is written to file_operations BEFORE the filesystem is touched and
// marked done in the same transaction that updates books.path. A crash at any
// point leaves a row the reconciler can finish or roll forward, so the database
// and the disk can never quietly disagree.
type Store struct {
	root     string
	template Template
	pool     *pgxpool.Pool
	log      *slog.Logger

	// One book at a time. Two concurrent edits to the same book would
	// otherwise race on the same directory.
	locks sync.Map // book id -> *sync.Mutex
}

// New builds a file store rooted at the library directory.
func New(root string, tpl Template, pool *pgxpool.Pool, log *slog.Logger) *Store {
	return &Store{root: root, template: tpl, pool: pool, log: log}
}

// Template exposes the configured layout.
func (s *Store) Template() Template { return s.template }

// Root exposes the library root.
func (s *Store) Root() string { return s.root }

func (s *Store) lockBook(id int64) func() {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Abs turns a library-relative path into an absolute one, refusing anything
// that would escape the root.
func (s *Store) Abs(rel string) (string, error) {
	if !IsSafeRelative(rel) {
		return "", fmt.Errorf("unsafe relative path %q", rel)
	}
	abs := filepath.Join(s.root, rel)
	// Join already cleans, but verify the result really is under the root:
	// a symlinked component could still point elsewhere.
	if !strings.HasPrefix(abs+string(filepath.Separator),
		filepath.Clean(s.root)+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the library root", rel)
	}
	return abs, nil
}

// Plan is a set of file moves for one book.
type Plan struct {
	BookID  int64
	FromDir string // relative
	ToDir   string // relative
	Files   []FileMove
}

// FileMove is one file within a plan.
type FileMove struct {
	Format   string
	FromName string
	ToName   string
}

// Empty reports whether the plan would change nothing.
func (p Plan) Empty() bool {
	if p.FromDir != p.ToDir {
		return false
	}
	for _, f := range p.Files {
		if f.FromName != f.ToName {
			return false
		}
	}
	return true
}

// PlanFor computes where a book's files should live.
func (s *Store) PlanFor(ctx context.Context, bookID int64) (*Plan, error) {
	var (
		m       Meta
		fromDir string
		series  *string
		year    *int
	)
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.title, b.author_sort, b.series_name, b.series_index, b.path,
		       EXTRACT(YEAR FROM b.pubdate)::int
		FROM books b WHERE b.id=$1`, bookID).
		Scan(&m.ID, &m.Title, &m.AuthorSort, &series, &m.SeriesIndex, &fromDir, &year)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("book %d not found", bookID)
	}
	if err != nil {
		return nil, err
	}
	if series != nil {
		m.Series = *series
	}
	if year != nil {
		m.Year = *year
	}

	// author_sort is trigger-maintained and empty only when a book has no
	// author at all; fall back rather than filing it under "Unknown".
	if m.AuthorSort == "" {
		var names []string
		if err := s.pool.QueryRow(ctx, `SELECT author_names FROM books WHERE id=$1`, bookID).
			Scan(&names); err == nil && len(names) > 0 {
			m.AuthorSort = names[0]
		}
	}

	plan := &Plan{BookID: bookID, FromDir: fromDir, ToDir: s.template.Dir(m)}

	rows, err := s.pool.Query(ctx,
		`SELECT format, filename FROM book_files WHERE book_id=$1 ORDER BY format`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	base := s.template.FileBase(m)
	for rows.Next() {
		var format, name string
		if err := rows.Scan(&format, &name); err != nil {
			return nil, err
		}
		ext := filepath.Ext(name)
		if ext == "" {
			ext = "." + strings.ToLower(format)
		}
		plan.Files = append(plan.Files, FileMove{
			Format: format, FromName: name, ToName: base + ext,
		})
	}
	return plan, rows.Err()
}

// Apply executes a plan, journalling every step.
func (s *Store) Apply(ctx context.Context, p *Plan) error {
	if p.Empty() {
		return nil
	}
	unlock := s.lockBook(p.BookID)
	defer unlock()

	fromAbs, err := s.Abs(p.FromDir)
	if err != nil {
		return err
	}
	toDir := p.ToDir
	toAbs, err := s.Abs(toDir)
	if err != nil {
		return err
	}

	// Two different books can render to the same directory -- the same title by
	// the same author, which this library has 337 of. Disambiguate rather than
	// merging them into one folder.
	if fromAbs != toAbs {
		toDir, toAbs, err = s.uniqueDir(ctx, toDir, p.BookID)
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(toAbs, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", toDir, err)
	}

	for i := range p.Files {
		f := &p.Files[i]
		src := filepath.Join(fromAbs, f.FromName)
		dst := filepath.Join(toAbs, f.ToName)
		if src == dst {
			continue
		}
		if err := s.journalledMove(ctx, p.BookID, src, dst); err != nil {
			return err
		}
	}

	// Covers and Calibre's metadata sidecar travel with the book.
	for _, extra := range []string{"cover.jpg", "metadata.opf"} {
		src := filepath.Join(fromAbs, extra)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := s.journalledMove(ctx, p.BookID, src, filepath.Join(toAbs, extra)); err != nil {
			s.log.Warn("could not move sidecar", "file", extra, "book", p.BookID, "err", err)
		}
	}

	// Commit the new location together with the filenames: a reader must never
	// see a path that points at a file that has already moved.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`UPDATE books SET path=$2 WHERE id=$1`, p.BookID, toDir); err != nil {
		return err
	}
	for _, f := range p.Files {
		if _, err := tx.Exec(ctx,
			`UPDATE book_files SET filename=$3 WHERE book_id=$1 AND format=$2`,
			p.BookID, f.Format, f.ToName); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE file_operations SET state='done', completed_at=now()
		WHERE book_id=$1 AND state='staged'`, p.BookID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Only now is it safe to tidy up; before the commit the old directory is
	// still what the database points at.
	s.pruneEmpty(fromAbs)
	return nil
}

// uniqueDir appends a disambiguating suffix if another book already owns the
// target directory.
func (s *Store) uniqueDir(ctx context.Context, dir string, bookID int64) (string, string, error) {
	resolved, err := s.resolveDir(ctx, dir, bookID, nil)
	if err != nil {
		return "", "", err
	}
	abs, err := s.Abs(resolved)
	return resolved, abs, err
}

// resolveDir decides the directory a book will actually be filed in.
//
// Two different books can render to the same path -- the same title by the same
// author, which this library has several hundred of -- and merging them into
// one folder would put two books' files side by side under names that collide.
//
// claimed lets a caller resolve a whole run's worth of moves without performing
// them, by recording what earlier books in the same run have taken. Passing nil
// consults only the database, which is correct when moves are being applied for
// real: each one commits its new path before the next is planned. PreviewDir
// passes a map so that a dry run reports the same directories the real run will
// choose -- otherwise the plan an operator reviews is not the plan that runs.
func (s *Store) resolveDir(ctx context.Context, dir string, bookID int64, claimed map[string]int64) (string, error) {
	// Case-insensitively, because the tree has to survive being copied to a
	// filesystem that does not distinguish "Hjärnstark" from "HJÄRNSTARK".
	// Backed by books_path_lower_idx; without it this is a sequential scan
	// performed once per book.
	key := strings.ToLower(dir)

	taken := false
	if owner, ok := claimed[key]; ok && owner != bookID {
		taken = true
	}
	if !taken {
		var owner int64
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM books WHERE lower(path)=lower($1) AND id<>$2`, dir, bookID).
			Scan(&owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return "", err
		default:
			taken = true
		}
	}
	if taken {
		// The id is stable and unique, which is exactly what Calibre uses too.
		dir = fmt.Sprintf("%s (%d)", dir, bookID)
	}
	if claimed != nil {
		claimed[strings.ToLower(dir)] = bookID
	}
	return dir, nil
}

// PreviewDir resolves where a book would be filed, without moving anything.
//
// claimed carries state across a run and is mutated; start with an empty map.
func (s *Store) PreviewDir(ctx context.Context, dir string, bookID int64, claimed map[string]int64) (string, error) {
	return s.resolveDir(ctx, dir, bookID, claimed)
}

// journalledMove records intent, performs the move, and marks it staged.
func (s *Store) journalledMove(ctx context.Context, bookID int64, src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to move. Record it so the gap is visible rather than silent.
			s.log.Warn("source file missing, skipping", "book", bookID, "src", src)
			return nil
		}
		return err
	}

	sum, err := hashFile(src)
	if err != nil {
		return err
	}

	var opID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO file_operations (book_id, op, src, dst, sha256, size_bytes, state)
		VALUES ($1,'move',$2,$3,$4,$5,'pending')
		RETURNING id`, bookID, src, dst, sum, st.Size()).Scan(&opID); err != nil {
		return err
	}

	if err := moveFile(src, dst, sum); err != nil {
		if _, e := s.pool.Exec(ctx,
			`UPDATE file_operations SET state='failed', error=$2, completed_at=now() WHERE id=$1`,
			opID, err.Error()); e != nil {
			s.log.Error("could not record move failure", "op", opID, "err", e)
		}
		return err
	}

	_, err = s.pool.Exec(ctx, `UPDATE file_operations SET state='staged' WHERE id=$1`, opID)
	return err
}

// moveFile relocates a file safely.
//
// Same-filesystem is a rename, which is atomic. Across filesystems it becomes
// copy, fsync, rename, verify, and only then unlink the source -- so an
// interrupted move always leaves at least one complete copy.
func moveFile(src, dst string, wantSum []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	tmp := dst + ".partial"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	// Verify before destroying the original.
	gotSum, err := hashFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if wantSum != nil && hex.EncodeToString(gotSum) != hex.EncodeToString(wantSum) {
		os.Remove(tmp)
		return fmt.Errorf("copy of %s did not verify; source left untouched", filepath.Base(src))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
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

// pruneEmpty removes a now-empty book directory and any empty parents, without
// ever climbing above the library root.
func (s *Store) pruneEmpty(dir string) {
	root := filepath.Clean(s.root)
	for {
		clean := filepath.Clean(dir)
		if clean == root || !strings.HasPrefix(clean+string(filepath.Separator),
			root+string(filepath.Separator)) {
			return
		}
		entries, err := os.ReadDir(clean)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(clean); err != nil {
			return
		}
		dir = filepath.Dir(clean)
	}
}
