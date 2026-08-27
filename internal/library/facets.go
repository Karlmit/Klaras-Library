package library

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Facet is one filterable value and how many books carry it.
type Facet struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// Facets is the sidebar payload.
type Facets struct {
	Authors    []Facet `json:"authors"`
	Tags       []Facet `json:"tags"`
	Languages  []Facet `json:"languages"`
	Series     []Facet `json:"series"`
	Formats    []Facet `json:"formats"`
	TotalBooks int64   `json:"total_books"`
	// Totals for the Browse buttons, which stand in for the lists that used to
	// be here. Counted rather than taken from the facet slice: that slice is
	// the top N, so its length is the page size, not the number of authors.
	AuthorsTotal int64  `json:"authors_total"`
	SeriesTotal  int64  `json:"series_total"`
	NeedsReview  int64  `json:"needs_review"`
	Adult        int64  `json:"adult"`
	RefreshedAt  string `json:"refreshed_at,omitempty"`
}

// Facets reads the materialised sidebar counts.
//
// Computed live these cost ~22ms each on this library, so a page load would
// spend most of its budget counting things that barely change. Reading the
// materialised view is a sub-millisecond index scan instead.
func (s *Store) Facets(ctx context.Context, limit int) (*Facets, error) {
	// The ceiling has to clear the whole category list: tidying them up means
	// seeing all 1,113, and silently returning the top 50 instead would look
	// like the rest had already been merged.
	if limit < 1 || limit > 5000 {
		limit = 50
	}
	out := &Facets{
		Authors: []Facet{}, Tags: []Facet{}, Languages: []Facet{},
		Series: []Facet{}, Formats: []Facet{},
	}
	rows, err := s.pool.Query(ctx, `
		SELECT kind, value, n FROM (
			SELECT kind, value, n,
			       row_number() OVER (PARTITION BY kind ORDER BY n DESC, value) AS rn
			FROM facet_counts
		) t
		WHERE rn <= $1 OR kind = '_total'
		ORDER BY kind, n DESC, value`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var kind, value string
		var n int64
		if err := rows.Scan(&kind, &value, &n); err != nil {
			return nil, err
		}
		f := Facet{Value: value, Count: n}
		switch kind {
		case "author":
			out.Authors = append(out.Authors, f)
		case "tag":
			out.Tags = append(out.Tags, f)
		case "language":
			out.Languages = append(out.Languages, f)
		case "series":
			out.Series = append(out.Series, f)
		case "format":
			out.Formats = append(out.Formats, f)
		case "_total":
			switch value {
			case "books":
				out.TotalBooks = n
			case "needs_review":
				out.NeedsReview = n
			case "adult":
				out.Adult = n
			case "authors":
				out.AuthorsTotal = n
			case "series":
				out.SeriesTotal = n
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var refreshed time.Time
	if err := s.pool.QueryRow(ctx, `SELECT refreshed_at FROM facet_state`).Scan(&refreshed); err == nil {
		out.RefreshedAt = refreshed.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// RefreshFacets rebuilds the materialised view if the library changed since the
// last refresh. Reports whether it did any work.
func (s *Store) RefreshFacets(ctx context.Context, force bool) (bool, error) {
	var lastMark time.Time
	var lastCount int64
	if err := s.pool.QueryRow(ctx,
		`SELECT watermark, book_count FROM facet_state`).Scan(&lastMark, &lastCount); err != nil {
		return false, err
	}

	// Three signals, and all three are needed. max(updated_at) catches edits,
	// count(*) catches deletions -- which lower the count without moving the
	// watermark -- and the visible count catches a book being hidden or
	// restored, which deliberately moves neither. Flagging must not touch
	// updated_at, because that drives Kobo sync; leaving it out of this check
	// meant the sidebar kept reporting the old totals and the Adult content
	// entry never appeared, because its count was still zero.
	var mark time.Time
	var count, visible int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(updated_at), 'epoch'::timestamptz),
		       count(*), count(*) FILTER (WHERE NOT adult)
		FROM books`).
		Scan(&mark, &count, &visible); err != nil {
		return false, err
	}
	var lastVisible int64
	if err := s.pool.QueryRow(ctx,
		`SELECT n FROM facet_counts WHERE kind='_total' AND value='books'`).
		Scan(&lastVisible); err != nil {
		lastVisible = -1 // never built, or mid-rebuild: refresh rather than guess
	}
	if !force && mark.Equal(lastMark) && count == lastCount && visible == lastVisible {
		return false, nil
	}

	// CONCURRENTLY keeps the sidebar readable while the new contents are built.
	// It cannot run inside a transaction block, hence the bare Exec.
	if _, err := s.pool.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY facet_counts`); err != nil {
		return false, fmt.Errorf("refresh facet_counts: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE facet_state SET refreshed_at=now(), watermark=$1, book_count=$2`,
		mark, count); err != nil {
		return false, err
	}
	return true, nil
}

// RunFacetRefresher keeps the counts current in the background.
func (s *Store) RunFacetRefresher(ctx context.Context, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			did, err := s.RefreshFacets(ctx, false)
			if err != nil && ctx.Err() == nil {
				log.Warn("facet refresh failed", "err", err)
			} else if did {
				log.Debug("facet counts refreshed")
			}
		}
	}
}
