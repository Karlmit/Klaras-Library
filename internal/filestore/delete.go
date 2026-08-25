package filestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// DeleteResult reports what removing a book's files did.
type DeleteResult struct {
	FilesRemoved int
	DirRemoved   bool
	Path         string
}

// DeleteBookFiles removes a book's directory from the managed tree.
//
// Every removal is journalled first, exactly like a move, so a crash midway
// leaves a record of what was being deleted rather than an unexplained gap.
//
// Only the book's own directory is touched, and only if it is genuinely inside
// the library root. Empty parents are pruned afterwards, which is what stops
// an author folder lingering after their last book goes.
func (s *Store) DeleteBookFiles(ctx context.Context, bookID int64, relDir string, filenames []string) (*DeleteResult, error) {
	res := &DeleteResult{Path: relDir}
	if relDir == "" {
		return res, nil
	}
	abs, err := s.Abs(relDir)
	if err != nil {
		return nil, err
	}

	unlock := s.lockBook(bookID)
	defer unlock()

	// Record intent before touching anything.
	var opID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO file_operations (book_id, op, src, state)
		VALUES ($1,'delete',$2,'pending') RETURNING id`, bookID, abs).Scan(&opID); err != nil {
		return nil, err
	}

	for _, name := range filenames {
		if name == "" {
			continue
		}
		p := filepath.Join(abs, name)
		if err := os.Remove(p); err == nil {
			res.FilesRemoved++
		} else if !os.IsNotExist(err) {
			if _, e := s.pool.Exec(ctx,
				`UPDATE file_operations SET state='failed', error=$2, completed_at=now() WHERE id=$1`,
				opID, err.Error()); e != nil {
				s.log.Error("could not record delete failure", "op", opID, "err", e)
			}
			return nil, fmt.Errorf("remove %s: %w", name, err)
		}
	}

	// Sidecars the library owns. Anything else in the directory is left alone
	// and the directory survives, which is the safe way round.
	for _, extra := range []string{"cover.jpg", "metadata.opf"} {
		if err := os.Remove(filepath.Join(abs, extra)); err == nil {
			res.FilesRemoved++
		}
	}

	if entries, err := os.ReadDir(abs); err == nil && len(entries) == 0 {
		if err := os.Remove(abs); err == nil {
			res.DirRemoved = true
			s.pruneEmpty(filepath.Dir(abs))
		}
	} else if err == nil {
		s.log.Warn("book directory still holds other files; leaving it in place",
			"book", bookID, "path", relDir, "remaining", len(entries))
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE file_operations SET state='done', completed_at=now() WHERE id=$1`, opID); err != nil {
		s.log.Error("could not close delete journal entry", "op", opID, "err", err)
	}
	return res, nil
}
