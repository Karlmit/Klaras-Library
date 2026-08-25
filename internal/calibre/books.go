package calibre

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// calibreTimeLayouts covers the formats a Calibre TIMESTAMP column can reach us
// in. Two separate sources of variation:
//
//   - Calibre itself is inconsistent: some rows carry fractional seconds, some
//     a numeric offset, some neither.
//   - modernc.org/sqlite NORMALISES what it reads. The bytes on disk are
//     "2024-11-29 11:10:48.441849+00:00" but the driver returns
//     "2024-11-29T11:10:48.441849Z". Both forms must be listed here, or every
//     date in the library silently fails to parse and is dropped.
//
// RFC3339Nano must therefore come first: it is what we actually see in practice.
var calibreTimeLayouts = []string{
	time.RFC3339Nano,                   // driver-normalised: 2024-11-29T11:10:48.441849Z
	time.RFC3339,                       // driver-normalised, whole seconds
	"2006-01-02 15:04:05.999999-07:00", // raw Calibre
	"2006-01-02 15:04:05-07:00",
	"2006-01-02T15:04:05.999999-07:00",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// parseCalibreTime parses a Calibre timestamp, reporting false for NULL, junk,
// or the 0101-01-01 sentinel Calibre uses to mean "unknown".
func parseCalibreTime(v sql.NullString) (time.Time, bool) {
	if !v.Valid {
		return time.Time{}, false
	}
	s := strings.TrimSpace(v.String)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range calibreTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() <= calibreEpochYear {
				return time.Time{}, false // Calibre's "no date" sentinel
			}
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func copyBooks(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	// One row per book with everything scalar folded in. The LEFT JOINs are on
	// Calibre's own indexed link tables and this runs once, so the shape
	// matters far less than it would on a request path.
	rows, err := src.db.Query(`
		SELECT b.id,
		       b.title,
		       COALESCE(b.sort, ''),
		       COALESCE(b.author_sort, ''),
		       b.pubdate,
		       b.timestamp,
		       b.last_modified,
		       b.series_index,
		       b.path,
		       b.has_cover,
		       b.uuid,
		       bsl.series,
		       bpl.publisher,
		       c.text,
		       r.rating,
		       (SELECT group_concat(l.lang_code, ',')
		          FROM books_languages_link bll
		          JOIN languages l ON l.id = bll.lang_code
		         WHERE bll.book = b.id
		         ORDER BY bll.item_order)
		FROM books b
		LEFT JOIN books_series_link     bsl ON bsl.book = b.id
		LEFT JOIN books_publishers_link bpl ON bpl.book = b.id
		LEFT JOIN comments              c   ON c.book   = b.id
		LEFT JOIN books_ratings_link    brl ON brl.book = b.id
		LEFT JOIN ratings               r   ON r.id     = brl.rating`)
	if err != nil {
		return 0, err
	}

	cols := []string{
		"id", "calibre_id", "uuid", "title", "title_sort", "author_sort",
		"description", "series_id", "series_index", "publisher_id", "pubdate",
		"rating", "languages", "path", "has_cover", "added_at", "updated_at",
		"needs_review",
	}

	n, skipped, err := copyInto(ctx, tx, "books", cols, rows, func(r *sql.Rows) ([]any, error) {
		var (
			id                          int64
			title, titleSort, authorSrt string
			pubdate, added, modified    sql.NullString
			seriesIdx                   sql.NullFloat64
			path                        string
			hasCover                    sql.NullBool
			uuid                        sql.NullString
			seriesID, publisherID       sql.NullInt64
			comment                     sql.NullString
			rating                      sql.NullInt64
			langs                       sql.NullString
		)
		if err := r.Scan(&id, &title, &titleSort, &authorSrt, &pubdate, &added, &modified,
			&seriesIdx, &path, &hasCover, &uuid, &seriesID, &publisherID,
			&comment, &rating, &langs); err != nil {
			return nil, err
		}

		// A book without a UUID cannot be synced to a Kobo, and every book in a
		// healthy Calibre library has one. Flag rather than invent.
		u := strings.TrimSpace(uuid.String)
		if !uuid.Valid || u == "" {
			return nil, nil
		}

		title = strings.TrimSpace(title)
		if title == "" {
			title = "Unknown"
		}

		var pub any
		if t, ok := parseCalibreTime(pubdate); ok {
			pub = t
		}
		addedAt := time.Now().UTC()
		if t, ok := parseCalibreTime(added); ok {
			addedAt = t
		}
		updatedAt := addedAt
		if t, ok := parseCalibreTime(modified); ok {
			updatedAt = t
		}

		// series_index is NOT NULL in Calibre and defaults to 1.0 even for
		// books in no series; only keep it where a series actually applies.
		var idx any
		if seriesID.Valid && seriesIdx.Valid {
			idx = seriesIdx.Float64
		}

		var desc any
		if comment.Valid && strings.TrimSpace(comment.String) != "" {
			desc = comment.String
		}

		var rat any
		if rating.Valid && rating.Int64 > 0 {
			rat = int16(rating.Int64)
		}

		langList := []string{}
		if langs.Valid {
			for _, l := range strings.Split(langs.String, ",") {
				if l = strings.TrimSpace(l); l != "" {
					langList = append(langList, l)
				}
			}
		}

		var sid, pid any
		if seriesID.Valid {
			sid = seriesID.Int64
		}
		if publisherID.Valid {
			pid = publisherID.Int64
		}

		res.imported[id] = struct{}{}

		return []any{
			id, id, u, title, strings.TrimSpace(titleSort), strings.TrimSpace(authorSrt),
			desc, sid, idx, pid, pub,
			rat, langList, path, hasCover.Valid && hasCover.Bool, addedAt, updatedAt,
			false,
		}, nil
	})

	res.Books = n
	if skipped > 0 {
		res.Skipped["books_without_uuid"] = skipped
	}
	return n, err
}

func copyBookAuthors(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	// Calibre has no explicit author ordering column; its own convention is the
	// link table's rowid order, which is the order they were added.
	rows, err := src.db.Query(`
		SELECT bal.book, bal.author,
		       ROW_NUMBER() OVER (PARTITION BY bal.book ORDER BY bal.id) - 1
		FROM books_authors_link bal
		JOIN books   b ON b.id = bal.book
		JOIN authors a ON a.id = bal.author`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "book_authors",
		[]string{"book_id", "author_id", "position"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var book, author, pos int64
			if err := r.Scan(&book, &author, &pos); err != nil {
				return nil, err
			}
			if _, ok := res.imported[book]; !ok {
				return nil, nil
			}
			return []any{book, author, int32(pos)}, nil
		})
	res.BookAuthors = n
	return n, err
}

func copyBookTags(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	rows, err := src.db.Query(`
		SELECT btl.book, btl.tag
		FROM books_tags_link btl
		JOIN books b ON b.id = btl.book
		JOIN tags  t ON t.id = btl.tag`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "book_tags", []string{"book_id", "tag_id"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var book, tag int64
			if err := r.Scan(&book, &tag); err != nil {
				return nil, err
			}
			if _, ok := res.imported[book]; !ok {
				return nil, nil
			}
			return []any{book, tag}, nil
		})
	res.BookTags = n
	return n, err
}

func copyIdentifiers(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	// identifiers is (book, scheme) unique on our side; Calibre allows the same
	// pair twice in damaged libraries, so take one deterministically.
	rows, err := src.db.Query(`
		SELECT i.book, lower(i.type), i.val
		FROM identifiers i
		JOIN books b ON b.id = i.book
		WHERE i.id IN (SELECT min(id) FROM identifiers GROUP BY book, lower(type))`)
	if err != nil {
		return 0, err
	}
	n, skipped, err := copyInto(ctx, tx, "identifiers",
		[]string{"book_id", "scheme", "value"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var book int64
			var scheme, val string
			if err := r.Scan(&book, &scheme, &val); err != nil {
				return nil, err
			}
			scheme, val = strings.TrimSpace(scheme), strings.TrimSpace(val)
			if scheme == "" || val == "" {
				return nil, nil
			}
			if _, ok := res.imported[book]; !ok {
				return nil, nil
			}
			return []any{book, scheme, val}, nil
		})
	res.Identifiers = n
	if skipped > 0 {
		res.Skipped["empty_identifiers"] = skipped
	}
	return n, err
}

func copyBookFiles(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	// Calibre stores the basename without an extension; the on-disk filename is
	// name + "." + lower(format). Verified against the real library: KEPUB is
	// written as ".kepub", not ".kepub.epub".
	rows, err := src.db.Query(`
		SELECT d.book, upper(d.format), d.name, d.uncompressed_size
		FROM data d
		JOIN books b ON b.id = d.book`)
	if err != nil {
		return 0, err
	}
	n, skipped, err := copyInto(ctx, tx, "book_files",
		[]string{"book_id", "format", "filename", "size_bytes"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var book, size int64
			var format, name string
			if err := r.Scan(&book, &format, &name, &size); err != nil {
				return nil, err
			}
			format = strings.ToUpper(strings.TrimSpace(format))
			name = strings.TrimSpace(name)
			if format == "" || name == "" {
				return nil, nil
			}
			if _, ok := res.imported[book]; !ok {
				return nil, nil
			}
			return []any{book, format, name + "." + strings.ToLower(format), size}, nil
		})
	res.Files = n
	if skipped > 0 {
		res.Skipped["empty_files"] = skipped
	}
	return n, err
}
