package filestore

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// ReconcileReport summarises a recovery pass.
type ReconcileReport struct {
	Examined   int64
	Completed  int64 // move had finished; database caught up
	RolledBack int64 // move had not started; nothing to do
	Duplicates int64 // both copies present; source removed after verifying
	Lost       int64 // neither copy present; book flagged for review
	Failed     int64
	Elapsed    time.Duration
}

// Reconcile replays every unfinished file operation.
//
// This runs at startup, and it is the reason the journal exists. A crash
// between "file moved" and "database updated" would otherwise leave the
// catalogue pointing at a path with nothing behind it, which looks to a user
// like the book was silently deleted.
//
// The four states below are exhaustive: for each journalled move, src and dst
// each either exist or do not.
func (s *Store) Reconcile(ctx context.Context) (*ReconcileReport, error) {
	start := time.Now()
	rep := &ReconcileReport{}

	rows, err := s.pool.Query(ctx, `
		SELECT id, book_id, src, dst, sha256, state
		FROM file_operations
		WHERE state IN ('pending','staged')
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}

	type op struct {
		id     int64
		bookID *int64
		src    string
		dst    string
		sum    []byte
		state  string
	}
	var ops []op
	for rows.Next() {
		var o op
		if err := rows.Scan(&o.id, &o.bookID, &o.src, &o.dst, &o.sum, &o.state); err != nil {
			rows.Close()
			return nil, err
		}
		ops = append(ops, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, o := range ops {
		rep.Examined++
		_, srcErr := os.Stat(o.src)
		_, dstErr := os.Stat(o.dst)
		srcExists, dstExists := srcErr == nil, dstErr == nil

		switch {
		case dstExists && !srcExists:
			// The move completed; only the bookkeeping is missing.
			if err := s.finishOp(ctx, o.id, o.bookID, o.dst); err != nil {
				s.log.Error("reconcile: finish", "op", o.id, "err", err)
				rep.Failed++
				continue
			}
			rep.Completed++

		case srcExists && !dstExists:
			// The move never happened. The database still points at src, so
			// nothing is broken -- just close the journal entry.
			if err := s.markOp(ctx, o.id, "failed",
				"move did not start before shutdown; source left in place"); err != nil {
				rep.Failed++
				continue
			}
			rep.RolledBack++

		case srcExists && dstExists:
			// Interrupted between copy and unlink. Keep the destination if it
			// verifies, otherwise trust the source and discard the partial.
			ok := true
			if o.sum != nil {
				got, err := hashFile(o.dst)
				ok = err == nil && hex.EncodeToString(got) == hex.EncodeToString(o.sum)
			}
			if !ok {
				s.log.Warn("reconcile: destination did not verify, discarding it",
					"op", o.id, "dst", o.dst)
				_ = os.Remove(o.dst)
				if err := s.markOp(ctx, o.id, "failed", "destination failed checksum"); err != nil {
					rep.Failed++
					continue
				}
				rep.RolledBack++
				continue
			}
			if err := os.Remove(o.src); err != nil {
				s.log.Warn("reconcile: could not remove source", "src", o.src, "err", err)
			}
			if err := s.finishOp(ctx, o.id, o.bookID, o.dst); err != nil {
				rep.Failed++
				continue
			}
			rep.Duplicates++

		default:
			// Neither copy exists. This is the only genuinely bad case, and it
			// needs a human, so flag the book rather than guess.
			s.log.Error("reconcile: both copies missing", "op", o.id, "src", o.src, "dst", o.dst)
			if o.bookID != nil {
				if _, err := s.pool.Exec(ctx, `
					UPDATE books SET needs_review = true,
					       review_reasons = array_append(review_reasons, 'file_lost_during_move')
					WHERE id=$1 AND NOT (review_reasons @> ARRAY['file_lost_during_move'])`,
					*o.bookID); err != nil {
					s.log.Error("reconcile: flag book", "err", err)
				}
			}
			if err := s.markOp(ctx, o.id, "failed", "neither source nor destination exists"); err != nil {
				rep.Failed++
				continue
			}
			rep.Lost++
		}
	}

	rep.Elapsed = time.Since(start)
	return rep, nil
}

// finishOp records a completed move and points the database at the new file.
func (s *Store) finishOp(ctx context.Context, opID int64, bookID *int64, dst string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if bookID != nil {
		rel, err := s.rel(dst)
		if err != nil {
			return err
		}
		dir, name := splitDir(rel)
		if _, err := tx.Exec(ctx, `UPDATE books SET path=$2 WHERE id=$1`, *bookID, dir); err != nil {
			return err
		}
		// Only a book file gets a filename row; sidecars like cover.jpg do not.
		if _, err := tx.Exec(ctx, `
			UPDATE book_files SET filename=$2
			WHERE book_id=$1 AND lower($2) LIKE '%.' || lower(format)`, *bookID, name); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE file_operations SET state='done', completed_at=now() WHERE id=$1`, opID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) markOp(ctx context.Context, opID int64, state, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE file_operations SET state=$2, error=$3, completed_at=now() WHERE id=$1`,
		opID, state, reason)
	return err
}

// rel converts an absolute path back to library-relative.
func (s *Store) rel(abs string) (string, error) {
	root := s.root
	if len(abs) <= len(root) || abs[:len(root)] != root {
		return "", fmt.Errorf("%q is not under the library root", abs)
	}
	return trimLeadingSep(abs[len(root):]), nil
}

func trimLeadingSep(p string) string {
	for len(p) > 0 && (p[0] == '/' || p[0] == '\\') {
		p = p[1:]
	}
	return p
}

func splitDir(rel string) (dir, name string) {
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == '/' || rel[i] == '\\' {
			return rel[:i], rel[i+1:]
		}
	}
	return "", rel
}
