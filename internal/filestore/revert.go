package filestore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

// RevertReport summarises an undo pass.
type RevertReport struct {
	Candidates int64
	Reverted   int64
	Skipped    int64
	Failed     int64
	Elapsed    time.Duration
}

// Revert undoes completed moves, newest first.
//
// The journal records src and dst for every move, which makes a reorganize
// reversible as long as the entries are still there. This exists because
// reorganize is the one operation that touches the whole library at once, and
// the honest answer to "what if the layout is wrong" should be "run revert",
// not "restore from backup".
//
// Only moves made after `since` are considered, so one reorganize can be undone
// without disturbing the incremental moves that came before it.
func (s *Store) Revert(ctx context.Context, since time.Time, dryRun bool, out io.Writer, log *slog.Logger) (*RevertReport, error) {
	start := time.Now()
	rep := &RevertReport{}

	// Newest first: a book moved twice must be walked back in reverse order,
	// or the second undo would look for a file the first one already moved.
	rows, err := s.pool.Query(ctx, `
		SELECT id, book_id, src, dst
		FROM file_operations
		WHERE op = 'move' AND state = 'done' AND completed_at >= $1
		ORDER BY completed_at DESC, id DESC`, since)
	if err != nil {
		return nil, err
	}
	type op struct {
		id     int64
		bookID *int64
		src    string
		dst    string
	}
	var ops []op
	for rows.Next() {
		var o op
		if err := rows.Scan(&o.id, &o.bookID, &o.src, &o.dst); err != nil {
			rows.Close()
			return nil, err
		}
		ops = append(ops, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.Candidates = int64(len(ops))

	for _, o := range ops {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		// Undo means moving dst back to src.
		if _, err := os.Stat(o.dst); err != nil {
			// Nothing at the destination: already undone, or moved again since.
			rep.Skipped++
			continue
		}
		if _, err := os.Stat(o.src); err == nil {
			// Something is already back at the source; refuse to overwrite it.
			log.Warn("skipping revert, source path is occupied", "src", o.src)
			rep.Skipped++
			continue
		}
		if out != nil {
			fmt.Fprintf(out, "%s\n  -> %s\n", o.dst, o.src)
		}
		if dryRun {
			continue
		}

		if err := moveFile(o.dst, o.src, nil); err != nil {
			log.Error("revert failed", "op", o.id, "err", err)
			rep.Failed++
			continue
		}
		// Point the database back, and mark the journal entry undone so a second
		// revert does not try to walk it back again.
		if o.bookID != nil {
			rel, err := s.rel(o.src)
			if err == nil {
				dir, name := splitDir(rel)
				if _, err := s.pool.Exec(ctx, `UPDATE books SET path=$2 WHERE id=$1`,
					*o.bookID, dir); err != nil {
					log.Error("revert: update path", "book", *o.bookID, "err", err)
				}
				if _, err := s.pool.Exec(ctx, `
					UPDATE book_files SET filename=$2
					WHERE book_id=$1 AND lower($2) LIKE '%.' || lower(format)`,
					*o.bookID, name); err != nil {
					log.Error("revert: update filename", "book", *o.bookID, "err", err)
				}
			}
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE file_operations SET state='failed', error='reverted', completed_at=now()
			WHERE id=$1`, o.id); err != nil {
			log.Error("revert: close journal entry", "op", o.id, "err", err)
		}
		s.pruneEmpty(parentDir(o.dst))
		rep.Reverted++
		if rep.Reverted%500 == 0 {
			log.Info("reverting", "done", rep.Reverted, "of", rep.Candidates)
		}
	}

	rep.Elapsed = time.Since(start)
	return rep, nil
}

func parentDir(p string) string {
	dir, _ := splitDir(p)
	return dir
}
