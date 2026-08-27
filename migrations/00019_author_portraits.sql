-- +goose Up
-- What has been looked up for an author's portrait, and what came back.
--
-- The negative answer is the important one. There are ten thousand authors
-- here and most are not on Wikidata at all: without a record of that, every
-- scroll past a card would ask again, which is both slow for the person
-- scrolling and rude to a free service.
CREATE TABLE author_portraits (
    author_id  bigint PRIMARY KEY REFERENCES authors(id) ON DELETE CASCADE,
    -- The cached file's name under {cache}/portraits, or NULL when the lookup
    -- found nothing.
    filename   text,
    source     text,
    source_url text,
    tried_at   timestamptz NOT NULL DEFAULT now()
);

-- Retrying the misses eventually is worth it -- authors get added to Wikidata --
-- so the sweep needs to find the oldest attempts.
CREATE INDEX author_portraits_tried_idx ON author_portraits (tried_at)
    WHERE filename IS NULL;

-- +goose Down
DROP TABLE author_portraits;
