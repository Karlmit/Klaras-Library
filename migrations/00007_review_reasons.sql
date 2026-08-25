-- +goose Up

-- Why a book was flagged for review. Populated by the importer's data-quality
-- pass and by the filesystem sweep; surfaced as a "Needs review" filter in the
-- UI so imported problems are visible rather than silently absorbed.
ALTER TABLE books ADD COLUMN review_reasons text[] NOT NULL DEFAULT '{}';

CREATE INDEX books_review_reasons_idx ON books USING gin (review_reasons)
    WHERE needs_review;

-- +goose Down
DROP INDEX IF EXISTS books_review_reasons_idx;
ALTER TABLE books DROP COLUMN IF EXISTS review_reasons;
