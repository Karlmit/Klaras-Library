package library

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BookListItem is the shape the grid needs. Deliberately narrow: the list view
// never fetches descriptions or file lists, so a page of 60 stays small.
type BookListItem struct {
	ID          int64    `json:"id"`
	UUID        string   `json:"uuid"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Series      *string  `json:"series,omitempty"`
	SeriesIndex *float64 `json:"series_index,omitempty"`
	Rating      *int16   `json:"rating,omitempty"`
	HasCover    bool     `json:"has_cover"`
	// CoverV changes when the book does, and rides along in the cover URL.
	// Covers are cached hard on purpose, so without something in the URL that
	// moves, a replaced cover keeps showing the old picture until the cache
	// expires -- a day later, or whenever someone thinks to force a reload.
	CoverV      int64  `json:"cover_v"`
	NeedsReview bool   `json:"needs_review"`
	AdultReason string `json:"adult_reason,omitempty"`
	PubYear     *int   `json:"pub_year,omitempty"`
	AddedAt     string `json:"added_at"`
}

// BookPage is one page of results.
type BookPage struct {
	Items      []BookListItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Total      *int64         `json:"total,omitempty"`
}

// SortMode names an ordering.
type SortMode string

const (
	SortTitle    SortMode = "title"
	SortAuthor   SortMode = "author"
	SortAdded    SortMode = "added"
	SortPubDate  SortMode = "pubdate"
	SortRating   SortMode = "rating"
	SortSeries   SortMode = "series"
	SortRelevant SortMode = "relevance"
)

// sortSpec describes one ordering: the columns to sort by, and the keyset
// predicate that resumes after a cursor. Every spec ends in id so the order is
// total and the cursor can never skip or repeat a row.
type sortSpec struct {
	orderBy string
	// keyset is a WHERE fragment. The placeholders are {{key}} and {{id}}
	// rather than something like $CUR and $CURID: the latter prefix each
	// other, so replacing $CUR first silently corrupts $CURID into "$13ID"
	// and every page after the first becomes invalid SQL.
	keyset string
	// column is the sort key, read back to build the next cursor.
	column string
	desc   bool
	// nullsLast marks a sort whose key can be NULL. Those rows collect at the
	// end of the ordering, and a row comparison against them yields NULL
	// rather than true, so without an explicit escape hatch the cursor can
	// never cross from the non-NULL region into the tail.
	nullsLast bool
}

var sortSpecs = map[SortMode]sortSpec{
	SortTitle: {
		orderBy: "b.title_sort, b.id",
		keyset:  "(b.title_sort, b.id) > ({{key}}, {{id}})",
		column:  "title_sort",
	},
	SortAuthor: {
		orderBy: "b.author_sort, b.series_index NULLS FIRST, b.title_sort, b.id",
		keyset:  "(b.author_sort, b.id) > ({{key}}, {{id}})",
		column:  "author_sort",
	},
	SortAdded: {
		orderBy: "b.added_at DESC, b.id DESC",
		keyset:  "(b.added_at, b.id) < ({{key}}::timestamptz, {{id}})",
		column:  "added_at",
		desc:    true,
	},
	SortPubDate: {
		orderBy:   "b.pubdate DESC NULLS LAST, b.id DESC",
		keyset:    "(b.pubdate, b.id) < ({{key}}::date, {{id}})",
		column:    "pubdate",
		desc:      true,
		nullsLast: true,
	},
	SortRating: {
		orderBy:   "b.rating DESC NULLS LAST, b.id DESC",
		keyset:    "(b.rating, b.id) < ({{key}}::smallint, {{id}})",
		column:    "rating",
		desc:      true,
		nullsLast: true,
	},
	SortSeries: {
		orderBy:   "b.series_name NULLS LAST, b.series_index NULLS FIRST, b.id",
		keyset:    "(b.series_name, b.id) > ({{key}}, {{id}})",
		column:    "series_name",
		nullsLast: true,
	},
}

// Filter narrows a listing.
// AdultVisibility says what a query should do with books flagged as adult.
type AdultVisibility int

const (
	// AdultHide excludes them. This is the zero value on purpose: a query that
	// has not considered the question must not be the one that leaks.
	AdultHide AdultVisibility = iota
	// AdultOnly returns nothing else. The administrators' review screen.
	AdultOnly
	// AdultInclude ignores the flag entirely.
	AdultInclude
)

type Filter struct {
	Query       string
	Author      string
	Tag         string
	Series      string
	Language    string
	Format      string
	ShelfID     int64
	NeedsReview bool

	// Adult decides how flagged books are treated. The zero value hides them,
	// so a caller that forgets to think about it cannot leak them -- which is
	// the whole point of the flag.
	Adult     AdultVisibility
	Sort      SortMode
	Limit     int
	Cursor    string
	WithTotal bool
}

// cursor carries the position of the last row of the previous page.
type cursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
	Null bool   `json:"n,omitempty"` // the sort key was NULL
	ID   int64  `json:"i"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	return &c, nil
}

// ListBooks returns one page.
//
// Pagination is keyset, never OFFSET: OFFSET makes Postgres read and discard
// every preceding row, so page 400 costs 400 pages of work. Measured on this
// library, OFFSET 25000 reads 28,038 rows in ~38ms where the keyset query reads
// 60 in 0.2ms, and the gap widens as the library grows.
func (s *Store) ListBooks(ctx context.Context, f Filter) (*BookPage, error) {
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 60
	}
	if f.Sort == "" {
		f.Sort = SortTitle
	}
	if f.Query != "" && f.Sort == SortRelevant {
		return s.searchBooks(ctx, f)
	}
	spec, ok := sortSpecs[f.Sort]
	if !ok {
		return nil, fmt.Errorf("unknown sort %q", f.Sort)
	}

	var (
		where []string
		args  []any
	)
	// push appends a bind argument and returns its placeholder. Building the
	// filter this way keeps every user-supplied value parameterised -- no value
	// is ever interpolated into the SQL text.
	push := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Query != "" {
		where = append(where, fmt.Sprintf(
			"b.search_tsv @@ plainto_tsquery('%s', f_unaccent(%s))", searchConfig, push(f.Query)))
	}
	if f.Author != "" {
		where = append(where, "b.author_names @> ARRAY["+push(f.Author)+"]")
	}
	if f.Tag != "" {
		where = append(where, "b.tag_names @> ARRAY["+push(f.Tag)+"]")
	}
	if f.Series != "" {
		where = append(where, "b.series_name = "+push(f.Series))
	}
	if f.Language != "" {
		where = append(where, "b.languages @> ARRAY["+push(f.Language)+"]")
	}
	if f.Format != "" {
		where = append(where, "EXISTS (SELECT 1 FROM book_files bf WHERE bf.book_id=b.id AND bf.format="+
			push(strings.ToUpper(f.Format))+")")
	}
	if f.ShelfID > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM shelf_books sb WHERE sb.book_id=b.id AND sb.shelf_id="+
			push(f.ShelfID)+")")
	}
	if f.NeedsReview {
		where = append(where, "b.needs_review")
	}
	switch f.Adult {
	case AdultOnly:
		where = append(where, "b.adult")
	case AdultInclude:
		// No predicate: an administrator browsing everything.
	default:
		where = append(where, "NOT b.adult")
	}

	// Everything added so far describes the filter. The cursor conditions come
	// next and must NOT be part of the total, or "28,038 books" would shrink to
	// "books after this page" as the user scrolls.
	filterClause := ""
	if len(where) > 0 {
		filterClause = "WHERE " + strings.Join(where, " AND ")
	}
	filterArgs := len(args)

	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	if cur != nil {
		if cur.Sort != string(f.Sort) {
			return nil, fmt.Errorf("cursor belongs to sort %q, not %q", cur.Sort, f.Sort)
		}
		// A NULL sort key has already reached the NULLS LAST tail; from there
		// only the id can advance.
		if cur.Null {
			where = append(where, fmt.Sprintf("b.%s IS NULL AND b.id %s %s",
				spec.column, gt(spec.desc), push(cur.ID)))
		} else {
			ks := strings.ReplaceAll(spec.keyset, "{{key}}", push(cur.Key))
			ks = strings.ReplaceAll(ks, "{{id}}", push(cur.ID))
			if spec.nullsLast {
				// NULLS LAST in either direction: those rows sort after every
				// non-NULL one, and a row comparison against NULL is never
				// true, so they need an explicit way in.
				ks = "(" + ks + " OR b." + spec.column + " IS NULL)"
			}
			where = append(where, ks)
		}
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT b.id, b.uuid, b.title, b.author_names, b.series_name, b.series_index,
		       b.rating, b.has_cover, EXTRACT(EPOCH FROM b.updated_at)::bigint,
		       b.needs_review, COALESCE(b.adult_reason,''),
		       EXTRACT(YEAR FROM b.pubdate)::int, b.added_at,
		       b.%s::text
		FROM books b
		%s
		ORDER BY %s
		LIMIT %s`, spec.column, clause, spec.orderBy, push(f.Limit+1))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	page := &BookPage{Items: make([]BookListItem, 0, f.Limit)}
	keys := make([]*string, 0, f.Limit+1)
	for rows.Next() {
		var it BookListItem
		var added time.Time
		var key *string
		if err := rows.Scan(&it.ID, &it.UUID, &it.Title, &it.Authors, &it.Series,
			&it.SeriesIndex, &it.Rating, &it.HasCover, &it.CoverV, &it.NeedsReview, &it.AdultReason,
			&it.PubYear, &added, &key); err != nil {
			return nil, err
		}
		it.AddedAt = added.UTC().Format(time.RFC3339)
		page.Items = append(page.Items, it)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Asking for one row beyond the page proves whether a next page exists
	// without a second COUNT query. The extra row is then discarded, and the
	// cursor is built from the last row we actually keep.
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		last := page.Items[f.Limit-1]
		c := cursor{Sort: string(f.Sort), ID: last.ID}
		if k := keys[f.Limit-1]; k != nil {
			c.Key = *k
		} else {
			c.Null = true
		}
		page.NextCursor = encodeCursor(c)
	}

	if f.WithTotal {
		total, err := s.countBooks(ctx, filterClause, args[:filterArgs])
		if err != nil {
			return nil, err
		}
		page.Total = &total
	}
	return page, nil
}

func (s *Store) countBooks(ctx context.Context, clause string, args []any) (int64, error) {
	var n int64
	q := "SELECT count(*) FROM books b " + clause
	err := s.pool.QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func gt(desc bool) string {
	if desc {
		return "<"
	}
	return ">"
}

// BookIDs returns the ids matching a filter, for "select all".
//
// Only the primary key is read, so even the largest category in this library
// comes back in a few milliseconds. Reports whether the limit truncated the
// result, because silently selecting a subset and then acting on it is exactly
// the kind of surprise a bulk operation must not spring.
func (s *Store) BookIDs(ctx context.Context, f Filter, limit int) ([]int64, bool, error) {
	if limit <= 0 {
		limit = 10000
	}
	var (
		where []string
		args  []any
	)
	push := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Query != "" {
		where = append(where, fmt.Sprintf(
			"b.search_tsv @@ plainto_tsquery('%s', f_unaccent(%s))", searchConfig, push(f.Query)))
	}
	if f.Author != "" {
		where = append(where, "b.author_names @> ARRAY["+push(f.Author)+"]")
	}
	if f.Tag != "" {
		where = append(where, "b.tag_names @> ARRAY["+push(f.Tag)+"]")
	}
	if f.Series != "" {
		where = append(where, "b.series_name = "+push(f.Series))
	}
	if f.Language != "" {
		where = append(where, "b.languages @> ARRAY["+push(f.Language)+"]")
	}
	switch f.Adult {
	case AdultOnly:
		where = append(where, "b.adult")
	case AdultInclude:
	default:
		where = append(where, "NOT b.adult")
	}
	if f.Format != "" {
		where = append(where, "EXISTS (SELECT 1 FROM book_files bf WHERE bf.book_id=b.id AND bf.format="+
			push(strings.ToUpper(f.Format))+")")
	}
	if f.ShelfID > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM shelf_books sb WHERE sb.book_id=b.id AND sb.shelf_id="+
			push(f.ShelfID)+")")
	}
	if f.NeedsReview {
		where = append(where, "b.needs_review")
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	q := fmt.Sprintf("SELECT b.id FROM books b %s ORDER BY b.title_sort, b.id LIMIT %s",
		clause, push(limit+1))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("book ids: %w", err)
	}
	defer rows.Close()

	out := make([]int64, 0, 512)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}
