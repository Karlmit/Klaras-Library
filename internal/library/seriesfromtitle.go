package library

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SeriesPlanItem is one book the naming pass would change.
type SeriesPlanItem struct {
	BookID   int64
	OldTitle string
	NewTitle string
	Series   string
	Index    float64
	// Warn is set where the change is worth a second look rather than being
	// wrong: two books claiming the same number, mostly.
	Warn string
}

// SeriesCandidates are the series-looking prefixes found in book titles.
type SeriesCandidate struct {
	Prefix string
	Books  int
}

// seriesTitle matches the shape Calibre libraries fall into when a series was
// never recorded properly: "Isfolket 12 - Feber i blodet", sometimes with the
// full series name in front, sometimes with no subtitle at all.
//
// The number is the point. It is the only record of where the book sits, so it
// is read out and written to series_index in the same transaction that takes it
// out of the title -- never before, or a crash between the two loses the order
// of a forty-book series with no way to recover it but by hand.
var seriesTitle = regexp.MustCompile(
	`^(?i:sagan\s+om\s+)?(.+?)\s+(\d+(?:\.\d+)?)\s*(?:[-–—:.]\s*(.*))?$`)

// SeriesCandidatesFromTitles finds prefixes that look like an unrecorded series.
func (s *Store) SeriesCandidatesFromTitles(ctx context.Context, min int) ([]SeriesCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT title FROM books WHERE series_id IS NULL AND NOT adult`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if m := seriesTitle.FindStringSubmatch(strings.TrimSpace(t)); m != nil && m[3] != "" {
			counts[strings.TrimSpace(m[1])]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []SeriesCandidate{}
	for p, n := range counts {
		if n >= min {
			out = append(out, SeriesCandidate{Prefix: p, Books: n})
		}
	}
	return out, nil
}

// PlanSeriesFromTitle works out what naming a series would change.
//
// Nothing is written. The plan is printed and read first, because this renames
// books and moves their files, and a regular expression let loose on ten
// thousand titles is exactly the kind of thing that should be looked at before
// it runs rather than after.
func (s *Store) PlanSeriesFromTitle(
	ctx context.Context, prefix string, keepTitle bool,
) ([]SeriesPlanItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, title FROM books
		WHERE series_id IS NULL
		  AND title ~* ('^(sagan om )?' || $1 || '[[:space:]]+[0-9]')
		ORDER BY title`, regexp.QuoteMeta(prefix))
	if err != nil {
		return nil, fmt.Errorf("find series books: %w", err)
	}
	defer rows.Close()

	var plan []SeriesPlanItem
	seen := map[float64]string{}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		m := seriesTitle.FindStringSubmatch(strings.TrimSpace(title))
		if m == nil || !strings.EqualFold(strings.TrimSpace(m[1]), prefix) {
			continue
		}
		idx, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		sub := strings.TrimSpace(m[3])

		item := SeriesPlanItem{
			BookID: id, OldTitle: title, NewTitle: title,
			Series: prefix, Index: idx,
		}
		// A book with no subtitle keeps its title: "Isfolket 39" carries the
		// number and nothing else, and stripping it would leave the book
		// called "Isfolket" like the forty others.
		if sub != "" && !keepTitle {
			item.NewTitle = prefix + " - " + sub
		}
		if prev, dup := seen[idx]; dup {
			item.Warn = fmt.Sprintf("also numbered %g: %s", idx, prev)
		} else {
			seen[idx] = title
		}
		plan = append(plan, item)
	}
	return plan, rows.Err()
}

// ApplySeriesFromTitle writes a plan.
//
// One transaction per book, covering the series, the number and the title
// together. The number lives in the title and nowhere else until this runs, so
// the write that removes it must be the same write that records it.
func (s *Store) ApplySeriesFromTitle(ctx context.Context, plan []SeriesPlanItem) (int, error) {
	done := 0
	for _, it := range plan {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return done, err
		}

		var seriesID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO series (name) VALUES ($1)
			 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			it.Series).Scan(&seriesID); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return done, fmt.Errorf("book %d: %w", it.BookID, err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE books
			   SET series_id = $2, series_index = $3, title = $4,
			       title_sort = title_sort_of($4)
			 WHERE id = $1`,
			it.BookID, seriesID, it.Index, it.NewTitle); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return done, fmt.Errorf("book %d: %w", it.BookID, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return done, fmt.Errorf("book %d: %w", it.BookID, err)
		}
		done++
	}
	return done, nil
}
