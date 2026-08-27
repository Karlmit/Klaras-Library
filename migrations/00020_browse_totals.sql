-- +goose Up
-- The sidebar no longer lists authors and series; it links to pages for them,
-- and those links carry a total. The facet slice cannot supply it -- that slice
-- is the top N by book count, so its length is the page size rather than the
-- number of authors in the library.

DROP MATERIALIZED VIEW IF EXISTS facet_counts;

-- +goose StatementBegin
CREATE MATERIALIZED VIEW facet_counts AS
    SELECT 'author'::text AS kind, a AS value, count(*)::bigint AS n
      FROM books, unnest(author_names) a WHERE NOT adult GROUP BY a
UNION ALL
    SELECT 'tag', t, count(*)::bigint
      FROM books, unnest(tag_names) t WHERE NOT adult GROUP BY t
UNION ALL
    SELECT 'language', l, count(*)::bigint
      FROM books, unnest(languages) l WHERE NOT adult GROUP BY l
UNION ALL
    SELECT 'series', series_name, count(*)::bigint
      FROM books WHERE series_name IS NOT NULL AND NOT adult GROUP BY series_name
UNION ALL
    SELECT 'format', f.format, count(DISTINCT f.book_id)::bigint
      FROM book_files f JOIN books b ON b.id = f.book_id
     WHERE NOT b.adult GROUP BY f.format
UNION ALL
    SELECT '_total', 'books', count(*)::bigint FROM books WHERE NOT adult
UNION ALL
    SELECT '_total', 'needs_review', count(*)::bigint FROM books WHERE needs_review AND NOT adult
UNION ALL
    SELECT '_total', 'adult', count(*)::bigint FROM books WHERE adult
UNION ALL
    -- Distinct names actually reachable, matching what the Authors page lists:
    -- an author whose every book is flagged adult is not shown there either.
    SELECT '_total', 'authors', count(DISTINCT a)::bigint
      FROM books, unnest(author_names) a WHERE NOT adult
UNION ALL
    SELECT '_total', 'series', count(DISTINCT series_name)::bigint
      FROM books WHERE series_name IS NOT NULL AND NOT adult;
-- +goose StatementEnd

CREATE UNIQUE INDEX facet_counts_key ON facet_counts (kind, value);

-- +goose Down
DROP MATERIALIZED VIEW IF EXISTS facet_counts;
