// Package devseed generates a synthetic library for benchmarking and for the
// query-plan regression tests.
//
// It exists in the shipped binary on purpose: the numbers that matter are the
// ones measured on the machine the library actually runs on, so `klaras
// dev-seed` can be pointed at a scratch database on the real NAS.
package devseed

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed seed.sql
var seedSQL string

// Counts summarises what was created.
type Counts struct {
	Books       int64
	Authors     int64
	BookAuthors int64
	BookTags    int64
	Elapsed     time.Duration
}

// Run replaces the contents of the library tables with nBooks synthetic books.
//
// This TRUNCATEs the library tables, so it must never be pointed at a real
// database; the caller is responsible for that check.
func Run(ctx context.Context, pool *pgxpool.Pool, nBooks int, log *slog.Logger) (*Counts, error) {
	if nBooks < 1 || nBooks > 5_000_000 {
		return nil, fmt.Errorf("books must be 1..5000000, got %d", nBooks)
	}
	sql := strings.ReplaceAll(seedSQL, "{{N_BOOKS}}", strconv.Itoa(nBooks))

	start := time.Now()
	log.Info("seeding synthetic library", "books", nBooks)

	// The script is a batch of statements with no bind parameters, so it goes
	// over the simple protocol in one round trip.
	if _, err := pool.Exec(ctx, sql); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}

	c := &Counts{Elapsed: time.Since(start)}
	err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM books),
		       (SELECT count(*) FROM authors),
		       (SELECT count(*) FROM book_authors),
		       (SELECT count(*) FROM book_tags)`).
		Scan(&c.Books, &c.Authors, &c.BookAuthors, &c.BookTags)
	if err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	// Plans are only meaningful against fresh statistics.
	if _, err := pool.Exec(ctx, "ANALYZE"); err != nil {
		return nil, fmt.Errorf("analyze: %w", err)
	}

	log.Info("seed complete",
		"books", c.Books, "authors", c.Authors,
		"book_authors", c.BookAuthors, "book_tags", c.BookTags,
		"elapsed", c.Elapsed.Round(time.Millisecond))
	return c, nil
}
