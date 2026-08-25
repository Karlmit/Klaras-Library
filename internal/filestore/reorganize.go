package filestore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// ReorganizeReport summarises a whole-library relocation.
type ReorganizeReport struct {
	Examined  int64
	Planned   int64
	Applied   int64
	Unchanged int64
	Failed    int64
	Elapsed   time.Duration
}

// Reorganize brings the entire library into line with the path template.
//
// This is the one operation that touches every book, and it is deliberately a
// separate opt-in command rather than something the server does on its own:
// running it on this library renames roughly 28,000 directories, because
// Calibre transliterated every Swedish character out of the existing tree.
//
// With dryRun set, nothing is touched and the full plan is written to out for
// review first.
func (s *Store) Reorganize(ctx context.Context, dryRun bool, out io.Writer, log *slog.Logger) (*ReorganizeReport, error) {
	start := time.Now()
	rep := &ReorganizeReport{}

	rows, err := s.pool.Query(ctx, `SELECT id FROM books ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var w *bufio.Writer
	if out != nil {
		w = bufio.NewWriter(out)
		defer w.Flush()
	}

	for _, id := range ids {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		rep.Examined++

		plan, err := s.PlanFor(ctx, id)
		if err != nil {
			log.Warn("could not plan move", "book", id, "err", err)
			rep.Failed++
			continue
		}
		if plan.Empty() {
			rep.Unchanged++
			continue
		}
		rep.Planned++

		if w != nil {
			fmt.Fprintf(w, "book %d\n  from %s\n    to %s\n", id, plan.FromDir, plan.ToDir)
			for _, f := range plan.Files {
				if f.FromName != f.ToName {
					fmt.Fprintf(w, "       %s  ->  %s\n", f.FromName, f.ToName)
				}
			}
		}
		if dryRun {
			continue
		}

		if err := s.Apply(ctx, plan); err != nil {
			log.Error("move failed", "book", id, "err", err)
			rep.Failed++
			continue
		}
		rep.Applied++
		if rep.Applied%500 == 0 {
			log.Info("reorganising", "moved", rep.Applied, "of", rep.Planned,
				"elapsed", time.Since(start).Round(time.Second))
		}
	}

	rep.Elapsed = time.Since(start)
	return rep, nil
}
