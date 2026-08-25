-- +goose Up

-- Sidebar facet counts.
--
-- Computing these live costs ~22ms each and there are several, so a page load
-- would spend most of its budget counting things that change rarely. They are
-- materialised and refreshed in the background instead; the counts may lag a
-- library edit by a few seconds, which is the right trade for a sidebar.
CREATE MATERIALIZED VIEW facet_counts AS
    SELECT 'author'::text AS kind, a AS value, count(*)::bigint AS n
      FROM books, unnest(author_names) a GROUP BY a
UNION ALL
    SELECT 'tag', t, count(*)::bigint
      FROM books, unnest(tag_names) t GROUP BY t
UNION ALL
    SELECT 'language', l, count(*)::bigint
      FROM books, unnest(languages) l GROUP BY l
UNION ALL
    SELECT 'series', series_name, count(*)::bigint
      FROM books WHERE series_name IS NOT NULL GROUP BY series_name
UNION ALL
    SELECT 'format', f.format, count(DISTINCT f.book_id)::bigint
      FROM book_files f GROUP BY f.format
UNION ALL
    -- Single-row totals, so the header does not need its own count(*).
    SELECT '_total', 'books', count(*)::bigint FROM books
UNION ALL
    SELECT '_total', 'needs_review', count(*)::bigint FROM books WHERE needs_review;

-- REFRESH ... CONCURRENTLY requires a unique index and lets readers keep using
-- the old contents while the new ones are built -- no stall on the sidebar.
CREATE UNIQUE INDEX facet_counts_key ON facet_counts (kind, value);
CREATE INDEX facet_counts_top ON facet_counts (kind, n DESC, value);

-- Tracks whether a refresh is actually needed, so the background ticker can do
-- nothing cheaply when the library is idle.
CREATE TABLE facet_state (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    refreshed_at  timestamptz NOT NULL DEFAULT 'epoch',
    -- Highest books.updated_at seen at the last refresh.
    watermark     timestamptz NOT NULL DEFAULT 'epoch',
    book_count    bigint      NOT NULL DEFAULT -1
);
INSERT INTO facet_state (id) VALUES (true);

-- +goose Down
DROP TABLE IF EXISTS facet_state;
DROP MATERIALIZED VIEW IF EXISTS facet_counts;
