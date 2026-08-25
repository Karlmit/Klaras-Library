package covers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// Backfill queues thumbnail generation for every book that has a cover on disk
// but no cached thumbnail yet.
//
// Books are streamed rather than loaded into a slice: the check is a stat() per
// book, and holding 28,000 rows in memory to do it would be pointless.
func Backfill(ctx context.Context, pool *pgxpool.Pool, svc *Service, q *jobs.Queue,
	force bool, log *slog.Logger) (queued, skipped int64, err error) {

	rows, err := pool.Query(ctx, `
		SELECT id, uuid::text, path FROM books
		WHERE has_cover
		ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	start := time.Now()
	for rows.Next() {
		if ctx.Err() != nil {
			return queued, skipped, ctx.Err()
		}
		var p ThumbnailPayload
		if err := rows.Scan(&p.BookID, &p.UUID, &p.Path); err != nil {
			return queued, skipped, err
		}
		if !force {
			if _, ok := svc.ThumbPath(p.UUID, Sizes[0].Name); ok {
				skipped++
				continue
			}
		}
		// Low priority: an interactive request that generates a cover on demand
		// must not queue behind a 28,000-book backfill.
		if err := q.Enqueue(ctx, jobs.KindThumbnail, p.UUID, p, 500); err != nil {
			return queued, skipped, fmt.Errorf("enqueue book %d: %w", p.BookID, err)
		}
		queued++
		if queued%2000 == 0 {
			log.Info("queueing covers", "queued", queued, "skipped", skipped,
				"elapsed", time.Since(start).Round(time.Second))
		}
	}
	return queued, skipped, rows.Err()
}
