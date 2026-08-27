package library

import (
	"context"
	"fmt"
)

// AuthorEntry is one card on the authors page.
type AuthorEntry struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Sort  string `json:"sort"`
	Books int    `json:"books"`
	// HasPortrait lets the grid draw an <img> only where there is one to draw.
	// Without it every card requests a picture that mostly does not exist, and
	// nine thousand cards means nine thousand pointless requests and a console
	// full of 404s.
	HasPortrait bool `json:"has_portrait"`
}

// Authors lists every author with a book count.
//
// The whole list at once, not a page of it. There are ten thousand here and
// the rows are tiny, so one response the browser can filter and sort instantly
// beats paging a grid someone is scrolling to find a name in.
func (s *Store) Authors(ctx context.Context) ([]AuthorEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.name, COALESCE(a.sort, a.name), count(*)::int,
		       bool_or(ap.filename IS NOT NULL)
		FROM authors a
		JOIN book_authors ba ON ba.author_id = a.id
		JOIN books b ON b.id = ba.book_id AND NOT b.adult
		LEFT JOIN author_portraits ap ON ap.author_id = a.id
		GROUP BY a.id, a.name, a.sort
		ORDER BY COALESCE(a.sort, a.name) COLLATE "`+sortCollation+`"`)
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	defer rows.Close()

	out := []AuthorEntry{}
	for rows.Next() {
		var a AuthorEntry
		if err := rows.Scan(&a.ID, &a.Name, &a.Sort, &a.Books, &a.HasPortrait); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SeriesEntry is one card on the series page.
type SeriesEntry struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Books int    `json:"books"`
	// Covers are the first few books in the series, for the fanned stack on
	// the card. A series is recognised by its covers, not by its name.
	Covers []SeriesCover `json:"covers"`
}

// SeriesCover is just enough to build a cover URL.
type SeriesCover struct {
	ID     int64 `json:"id"`
	CoverV int64 `json:"cover_v"`
}

// Series lists every series with a book count and a few covers each.
func (s *Store) Series(ctx context.Context) ([]SeriesEntry, error) {
	rows, err := s.pool.Query(ctx, `
		WITH ranked AS (
			SELECT b.series_id, b.id, b.has_cover,
			       EXTRACT(EPOCH FROM b.updated_at)::bigint AS cover_v,
			       row_number() OVER (PARTITION BY b.series_id
			                          ORDER BY b.series_index NULLS LAST, b.title_sort) AS rn
			FROM books b
			WHERE b.series_id IS NOT NULL AND NOT b.adult
		)
		SELECT s.id, s.name, count(*)::int,
		       COALESCE(array_agg(r.id ORDER BY r.rn)
		                FILTER (WHERE r.rn <= 4 AND r.has_cover), '{}') AS ids,
		       COALESCE(array_agg(r.cover_v ORDER BY r.rn)
		                FILTER (WHERE r.rn <= 4 AND r.has_cover), '{}') AS vs
		FROM series s
		JOIN ranked r ON r.series_id = s.id
		GROUP BY s.id, s.name
		ORDER BY s.name COLLATE "`+sortCollation+`"`)
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	defer rows.Close()

	out := []SeriesEntry{}
	for rows.Next() {
		var e SeriesEntry
		var ids, vs []int64
		if err := rows.Scan(&e.ID, &e.Name, &e.Books, &ids, &vs); err != nil {
			return nil, err
		}
		for i, id := range ids {
			c := SeriesCover{ID: id}
			if i < len(vs) {
				c.CoverV = vs[i]
			}
			e.Covers = append(e.Covers, c)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MergeTags renames categories, folding them into an existing one when the new
// name is already used, and removes them when the new name is empty.
//
// Done in one statement per step rather than book by book: "F" is on 6,706
// books here, and a round trip each would take minutes and hold a transaction
// open throughout.
func (s *Store) MergeTags(ctx context.Context, from []string, to string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var affected int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT bt.book_id)::int
		FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
		WHERE t.name = ANY($1)`, from).Scan(&affected); err != nil {
		return 0, err
	}

	if to != "" {
		var toID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO tags (name) VALUES ($1)
			 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
			to).Scan(&toID); err != nil {
			return 0, err
		}
		// ON CONFLICT DO NOTHING covers the books that already carry both
		// names -- the merge must not fail on the overlap.
		if _, err := tx.Exec(ctx, `
			INSERT INTO book_tags (book_id, tag_id)
			-- $2 is cast explicitly: a placeholder in a SELECT list has no
			-- surrounding expression to infer a type from, so Postgres reads
			-- it as text and the insert fails on the bigint column.
			SELECT DISTINCT bt.book_id, $2::bigint
			FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
			WHERE t.name = ANY($1)
			ON CONFLICT DO NOTHING`, from, toID); err != nil {
			return 0, err
		}
	}

	// Touch the books so their denormalised tag_names and search vector are
	// rebuilt, and so anything watching updated_at notices.
	if _, err := tx.Exec(ctx, `
		UPDATE books SET updated_at = now()
		WHERE id IN (SELECT bt.book_id FROM book_tags bt
		             JOIN tags t ON t.id = bt.tag_id WHERE t.name = ANY($1))`,
		from); err != nil {
		return 0, err
	}

	// Deleting the old tags takes their book_tags rows with them.
	if _, err := tx.Exec(ctx, `DELETE FROM tags WHERE name = ANY($1)`, from); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return affected, nil
}
