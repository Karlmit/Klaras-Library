package library

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// searchBooks ranks by relevance rather than a stable sort key.
//
// Two matchers, deliberately combined:
//
//   - full text over search_tsv, which is built with the Swedish configuration
//     and so folds inflection: "flicka" matches "Flickorna".
//   - trigram similarity, which catches typos and the irregular forms no
//     stemmer handles ("Böckerna" never reduces to "bok").
//
// Relevance ordering has no usable keyset cursor -- ranks are not unique and
// shift as the library changes -- so search pages by rank then id, and is
// capped. Nobody pages to result 3000 of a text search; they refine the query.
func (s *Store) searchBooks(ctx context.Context, f Filter) (*BookPage, error) {
	const maxSearchResults = 500

	limit := f.Limit
	offset := 0
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
		offset, _ = strconv.Atoi(c.Key)
		if offset < 0 || offset > maxSearchResults {
			offset = 0
		}
	}

	args := []any{f.Query, limit + 1, offset}
	q := fmt.Sprintf(`
		WITH q AS (SELECT plainto_tsquery('%[1]s', f_unaccent($1)) AS ts,
		                  f_unaccent($1)                          AS raw)
		SELECT b.id, b.uuid, b.title, b.author_names, b.series_name, b.series_index,
		       b.rating, b.has_cover, EXTRACT(EPOCH FROM b.updated_at)::bigint,
		       b.needs_review,
		       EXTRACT(YEAR FROM b.pubdate)::int, b.added_at,
		       GREATEST(
		         ts_rank(b.search_tsv, q.ts) * 4,
		         similarity(f_unaccent(b.title), q.raw) * 2,
		         similarity(f_unaccent(b.authors_flat), q.raw)
		       ) AS score
		FROM books b, q
		WHERE NOT b.adult
		  AND (b.search_tsv @@ q.ts
		   OR f_unaccent(b.title) %% q.raw
		   OR f_unaccent(b.authors_flat) %% q.raw)
		ORDER BY score DESC, b.title_sort, b.id
		LIMIT $2 OFFSET $3`, searchConfig)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	page := &BookPage{Items: make([]BookListItem, 0, limit)}
	for rows.Next() {
		var it BookListItem
		var added time.Time
		var score float32
		if err := rows.Scan(&it.ID, &it.UUID, &it.Title, &it.Authors, &it.Series,
			&it.SeriesIndex, &it.Rating, &it.HasCover, &it.CoverV, &it.NeedsReview,
			&it.PubYear, &added, &score); err != nil {
			return nil, err
		}
		it.AddedAt = added.UTC().Format(time.RFC3339)
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		if next := offset + limit; next < maxSearchResults {
			page.NextCursor = encodeCursor(cursor{
				Sort: string(SortRelevant),
				Key:  strconv.Itoa(next),
			})
		}
	}
	return page, nil
}

// Suggestion is one autocomplete hit.
type Suggestion struct {
	Kind  string `json:"kind"` // book | author | series | tag
	Value string `json:"value"`
	ID    int64  `json:"id,omitempty"`
}

// Suggest powers search-as-you-type across books, authors, series and tags.
func (s *Store) Suggest(ctx context.Context, term string, limit int) ([]Suggestion, error) {
	if limit < 1 || limit > 30 {
		limit = 10
	}
	if len([]rune(term)) < 2 {
		return []Suggestion{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		(SELECT 'book' AS kind, b.title AS value, b.id,
		        similarity(f_unaccent(b.title), f_unaccent($1)) AS sim
		   FROM books b WHERE NOT b.adult AND f_unaccent(b.title) % f_unaccent($1)
		   ORDER BY sim DESC, b.title_sort LIMIT $2)
		UNION ALL
		(SELECT 'author', a.name, a.id, similarity(f_unaccent(a.name), f_unaccent($1))
		   FROM authors a WHERE f_unaccent(a.name) % f_unaccent($1)
		   ORDER BY 4 DESC, a.sort LIMIT $2)
		UNION ALL
		(SELECT 'series', se.name, se.id, similarity(f_unaccent(se.name), f_unaccent($1))
		   FROM series se WHERE f_unaccent(se.name) % f_unaccent($1)
		   ORDER BY 4 DESC, se.sort LIMIT $2)
		UNION ALL
		(SELECT 'tag', t.name, t.id, similarity(f_unaccent(t.name), f_unaccent($1))
		   FROM tags t WHERE f_unaccent(t.name) % f_unaccent($1)
		   ORDER BY 4 DESC, t.name LIMIT $2)
		ORDER BY sim DESC
		LIMIT $2`, term, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Suggestion{}
	for rows.Next() {
		var sg Suggestion
		var sim float32
		if err := rows.Scan(&sg.Kind, &sg.Value, &sg.ID, &sim); err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}
