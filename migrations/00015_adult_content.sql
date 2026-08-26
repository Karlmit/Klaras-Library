-- +goose Up

-- Adult content, hidden from everyone but administrators.
--
-- This library is shared by a household and about 7% of it is erotica, almost
-- all of it from six dedicated imprints. Finding those by eye means scrolling
-- 28,000 covers, so they are identified from metadata and hidden by default;
-- an administrator reviews the result and either clears the flag or deletes the
-- book.
--
-- The reason is kept because a flag with no explanation cannot be reviewed. An
-- administrator seeing "publisher LUST" against a book decides differently than
-- one seeing "the word erotisk appears in the description", and the second kind
-- is where the mistakes will be.
ALTER TABLE books
    ADD COLUMN adult        boolean NOT NULL DEFAULT false,
    ADD COLUMN adult_reason text;

-- Partial: the flagged set is a small fraction of the library, and every
-- non-admin query filters on NOT adult, which the planner serves from the table
-- statistics rather than this index. This index is for the review screen, which
-- asks the opposite question.
CREATE INDEX books_adult_idx ON books (title_sort, id) WHERE adult;


-- The sidebar counts everything, so it would advertise the flagged books even
-- while the grid hid them: an author with nothing but erotica in the library
-- would still appear, with a count, leading to an empty page. Rebuilt to count
-- only what a reader can actually reach. Administrators see the flagged books
-- through their own screen, which does not use these counts.
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
    SELECT '_total', 'adult', count(*)::bigint FROM books WHERE adult;
-- +goose StatementEnd

CREATE UNIQUE INDEX facet_counts_key ON facet_counts (kind, value);

-- +goose Down
DROP INDEX IF EXISTS books_adult_idx;
ALTER TABLE books
    DROP COLUMN IF EXISTS adult,
    DROP COLUMN IF EXISTS adult_reason;
