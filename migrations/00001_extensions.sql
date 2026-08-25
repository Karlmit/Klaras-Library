-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS unaccent;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS btree_gin;
-- +goose StatementEnd

-- unaccent() is STABLE because it depends on a dictionary that could in principle be
-- reloaded. Generated columns and expression indexes both require IMMUTABLE, so we pin
-- the dictionary and wrap it. Safe as long as the unaccent dictionary is never edited;
-- if it ever is, every index and generated column below must be rebuilt.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION f_unaccent(text) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
    RETURN unaccent('unaccent'::regdictionary, $1);
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Language configuration.
--
-- This library is ~99% Swedish. Both objects below are named aliases so the
-- language can be changed in ONE place; changing either one requires a REINDEX
-- (collation) or a rebuild of books.search_tsv (search config).
-- ---------------------------------------------------------------------------

-- Swedish sorts a, a and o as distinct letters AFTER z, not as variants of a
-- and o. A generic ICU locale files "Akesson" under A, which is wrong:
--   und   -> Akesson < Andersson < Arlig < Bergman < Odegaard < ... < Zetterberg
--   sv-SE -> Andersson < Bergman < Yngve < Zetterberg < Akesson < Arlig < Odegaard
-- Deterministic so it can back a unique index and give keyset pagination a
-- total order.
-- +goose StatementBegin
CREATE COLLATION IF NOT EXISTS library_sort (
    provider      = icu,
    locale        = 'sv-SE',
    deterministic = true
);
-- +goose StatementEnd

-- The Swedish snowball stemmer folds definite and plural suffixes that the
-- 'simple' config leaves alone -- Flickorna/flicka, Mordaren/morda,
-- Tradgarden/tradgard -- and strips Swedish stopwords. Verified harmless on the
-- ~1% English titles. Irregulars (Bockerna/bok) still miss; the trigram index
-- is the fallback for those.
-- +goose StatementBegin
CREATE TEXT SEARCH CONFIGURATION library_search ( COPY = swedish );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TEXT SEARCH CONFIGURATION IF EXISTS library_search;
-- +goose StatementEnd
-- +goose StatementBegin
DROP COLLATION IF EXISTS library_sort;
-- +goose StatementEnd
-- +goose StatementBegin
DROP FUNCTION IF EXISTS f_unaccent(text);
-- +goose StatementEnd
