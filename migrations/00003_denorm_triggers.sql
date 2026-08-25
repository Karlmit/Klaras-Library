-- +goose Up

-- ---------------------------------------------------------------------------
-- Sort-key helpers (only used for books we create; imported books keep the
-- sort values Calibre already computed).
-- ---------------------------------------------------------------------------

-- Moves a leading article to the end, Calibre-style: "The Hobbit" -> "Hobbit, The".
-- Covers English, Swedish, German, French and Spanish articles; this library is
-- primarily English + Swedish.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION title_sort_of(p_title text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    articles constant text[] := ARRAY[
        'the','a','an',                       -- en
        'en','ett','den','det','de',          -- sv
        'der','die','das','ein','eine',       -- de
        'le','la','les','un','une',           -- fr
        'el','los','las','unos','unas'        -- es
    ];
    m text[];
BEGIN
    IF p_title IS NULL OR btrim(p_title) = '' THEN
        RETURN '';
    END IF;
    m := regexp_match(btrim(p_title), '^(\S+)\s+(.+)$');
    IF m IS NOT NULL AND lower(m[1]) = ANY (articles) THEN
        RETURN m[2] || ', ' || m[1];
    END IF;
    RETURN btrim(p_title);
END $$;
-- +goose StatementEnd

-- "Douglas Adams" -> "Adams, Douglas". Single-word names are returned unchanged.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION author_sort_of(p_name text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    m text[];
BEGIN
    IF p_name IS NULL OR btrim(p_name) = '' THEN
        RETURN '';
    END IF;
    m := regexp_match(btrim(p_name), '^(.+)\s+(\S+)$');
    IF m IS NULL THEN
        RETURN btrim(p_name);
    END IF;
    RETURN m[2] || ', ' || m[1];
END $$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Denormalisation refresh
-- ---------------------------------------------------------------------------

-- Recomputes the trigger-maintained columns for the given books. The final
-- IS DISTINCT FROM guard matters: without it every refresh would bump
-- updated_at and cause a spurious Kobo re-sync of unchanged books.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh_book_denorm(p_ids bigint[]) RETURNS bigint
    LANGUAGE plpgsql AS $fn$
DECLARE
    n bigint;
BEGIN
    UPDATE books b
       SET author_names = d.author_names,
           author_sort  = d.author_sort,
           tag_names    = d.tag_names
      FROM (
        SELECT x.id,
               COALESCE(a.names, '{}'::text[]) AS author_names,
               COALESCE(a.srt,   '')           AS author_sort,
               COALESCE(t.names, '{}'::text[]) AS tag_names
          FROM unnest(p_ids) AS x(id)
          LEFT JOIN LATERAL (
                SELECT array_agg(au.name ORDER BY ba.position, au.id)      AS names,
                       (array_agg(au.sort ORDER BY ba.position, au.id))[1] AS srt
                  FROM book_authors ba
                  JOIN authors au ON au.id = ba.author_id
                 WHERE ba.book_id = x.id
          ) a ON true
          LEFT JOIN LATERAL (
                SELECT array_agg(tg.name ORDER BY tg.name) AS names
                  FROM book_tags bt
                  JOIN tags tg ON tg.id = bt.tag_id
                 WHERE bt.book_id = x.id
          ) t ON true
      ) d
     WHERE b.id = d.id
       AND (b.author_names IS DISTINCT FROM d.author_names
         OR b.author_sort  IS DISTINCT FROM d.author_sort
         OR b.tag_names    IS DISTINCT FROM d.tag_names);
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END $fn$;
-- +goose StatementEnd

-- Whole-library rebuild. Used once at the end of a bulk import, where the
-- per-statement triggers are disabled for speed. Never called on a live request.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh_all_book_denorm() RETURNS bigint
    LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    SELECT refresh_book_denorm(ARRAY(SELECT id FROM books)) INTO n;
    UPDATE books b SET series_name = s.name
      FROM series s
     WHERE b.series_id = s.id
       AND b.series_name IS DISTINCT FROM s.name;
    UPDATE books SET series_name = NULL
     WHERE series_id IS NULL AND series_name IS NOT NULL;
    RETURN n;
END $$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- books row trigger: derived columns + updated_at
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION books_before_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    -- search_tsv is a generated column and so may only reference immutable
    -- expressions on this row. array_to_string is STABLE, so the flattened
    -- forms are materialised here instead.
    NEW.authors_flat := array_to_string(NEW.author_names, ' ');
    NEW.tags_flat    := array_to_string(NEW.tag_names,    ' ');

    IF TG_OP = 'INSERT' OR NEW.series_id IS DISTINCT FROM OLD.series_id THEN
        SELECT s.name INTO NEW.series_name FROM series s WHERE s.id = NEW.series_id;
    END IF;

    IF coalesce(NEW.title_sort, '') = '' THEN
        NEW.title_sort := title_sort_of(NEW.title);
    END IF;

    -- Auto-touch unless the caller set updated_at explicitly (the importer does,
    -- to preserve Calibre's last_modified).
    IF TG_OP = 'UPDATE' AND NEW.updated_at = OLD.updated_at THEN
        NEW.updated_at := now();
    END IF;

    RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER books_before_write
    BEFORE INSERT OR UPDATE ON books
    FOR EACH ROW EXECUTE FUNCTION books_before_write();

-- ---------------------------------------------------------------------------
-- Link-table triggers
--
-- Statement-level with transition tables, not row-level: a bulk import that
-- inserts 60k book_authors rows fires these three times, not 60k times.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_link_refresh_ins() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM refresh_book_denorm(ARRAY(SELECT DISTINCT book_id FROM newtab));
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_link_refresh_del() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM refresh_book_denorm(ARRAY(SELECT DISTINCT book_id FROM oldtab));
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_link_refresh_upd() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM refresh_book_denorm(ARRAY(
        SELECT DISTINCT book_id FROM (
            SELECT book_id FROM newtab
            UNION
            SELECT book_id FROM oldtab
        ) u));
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER book_authors_ins AFTER INSERT ON book_authors
    REFERENCING NEW TABLE AS newtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_ins();
CREATE TRIGGER book_authors_del AFTER DELETE ON book_authors
    REFERENCING OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_del();
CREATE TRIGGER book_authors_upd AFTER UPDATE ON book_authors
    REFERENCING NEW TABLE AS newtab OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_upd();

CREATE TRIGGER book_tags_ins AFTER INSERT ON book_tags
    REFERENCING NEW TABLE AS newtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_ins();
CREATE TRIGGER book_tags_del AFTER DELETE ON book_tags
    REFERENCING OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_del();
CREATE TRIGGER book_tags_upd AFTER UPDATE ON book_tags
    REFERENCING NEW TABLE AS newtab OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_link_refresh_upd();

-- ---------------------------------------------------------------------------
-- Rename propagation
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_authors_renamed() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM refresh_book_denorm(ARRAY(
        SELECT DISTINCT ba.book_id
          FROM newtab n
          JOIN oldtab o ON o.id = n.id
          JOIN book_authors ba ON ba.author_id = n.id
         WHERE n.name IS DISTINCT FROM o.name
            OR n.sort IS DISTINCT FROM o.sort));
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER authors_renamed AFTER UPDATE ON authors
    REFERENCING NEW TABLE AS newtab OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_authors_renamed();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_tags_renamed() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM refresh_book_denorm(ARRAY(
        SELECT DISTINCT bt.book_id
          FROM newtab n
          JOIN oldtab o ON o.id = n.id
          JOIN book_tags bt ON bt.tag_id = n.id
         WHERE n.name IS DISTINCT FROM o.name));
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER tags_renamed AFTER UPDATE ON tags
    REFERENCING NEW TABLE AS newtab OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_tags_renamed();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_series_renamed() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE books b
       SET series_name = n.name
      FROM newtab n
      JOIN oldtab o ON o.id = n.id
     WHERE b.series_id = n.id
       AND n.name IS DISTINCT FROM o.name;
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER series_renamed AFTER UPDATE ON series
    REFERENCING NEW TABLE AS newtab OLD TABLE AS oldtab
    FOR EACH STATEMENT EXECUTE FUNCTION trg_series_renamed();

-- +goose Down
DROP TRIGGER IF EXISTS series_renamed  ON series;
DROP TRIGGER IF EXISTS tags_renamed    ON tags;
DROP TRIGGER IF EXISTS authors_renamed ON authors;
DROP TRIGGER IF EXISTS book_tags_upd    ON book_tags;
DROP TRIGGER IF EXISTS book_tags_del    ON book_tags;
DROP TRIGGER IF EXISTS book_tags_ins    ON book_tags;
DROP TRIGGER IF EXISTS book_authors_upd ON book_authors;
DROP TRIGGER IF EXISTS book_authors_del ON book_authors;
DROP TRIGGER IF EXISTS book_authors_ins ON book_authors;
DROP TRIGGER IF EXISTS books_before_write ON books;
DROP FUNCTION IF EXISTS trg_series_renamed, trg_tags_renamed, trg_authors_renamed,
                        trg_link_refresh_upd, trg_link_refresh_del, trg_link_refresh_ins,
                        books_before_write, refresh_all_book_denorm, refresh_book_denorm,
                        author_sort_of, title_sort_of;
