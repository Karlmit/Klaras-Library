-- +goose Up

-- Let a writer say "this is not a content change".
--
-- books_before_write stamps updated_at = now() whenever an update leaves it
-- equal to its old value, which is what makes ordinary edits self-timestamping
-- and lets the importer preserve Calibre's dates by setting a different one.
-- It also makes preserving the CURRENT value impossible: writing
-- updated_at = updated_at is precisely the auto-touch condition.
--
-- That matters because updated_at drives Kobo sync. Flagging 1,900 books as
-- adult would tell every paired device that 1,900 books had changed, for a
-- column no device can see. Same for clearing the flag during review.
--
-- A transaction-local setting is the escape hatch. SET LOCAL keeps it to the
-- one transaction that asked for it, so nothing leaks into the connection when
-- it returns to the pool.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION books_before_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    NEW.authors_flat := array_to_string(NEW.author_names, ' ');
    NEW.tags_flat    := array_to_string(NEW.tag_names,    ' ');

    IF TG_OP = 'INSERT' OR NEW.series_id IS DISTINCT FROM OLD.series_id THEN
        SELECT s.name INTO NEW.series_name FROM series s WHERE s.id = NEW.series_id;
    END IF;

    -- Auto-touch unless the caller set updated_at explicitly (the importer
    -- does, to preserve Calibre's last_modified) or has declared that this
    -- write is bookkeeping rather than an edit.
    IF TG_OP = 'UPDATE' AND NEW.updated_at = OLD.updated_at
       AND COALESCE(current_setting('klaras.preserve_updated_at', true), '') <> 'on' THEN
        NEW.updated_at := now();
    END IF;

    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION books_before_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    NEW.authors_flat := array_to_string(NEW.author_names, ' ');
    NEW.tags_flat    := array_to_string(NEW.tag_names,    ' ');

    IF TG_OP = 'INSERT' OR NEW.series_id IS DISTINCT FROM OLD.series_id THEN
        SELECT s.name INTO NEW.series_name FROM series s WHERE s.id = NEW.series_id;
    END IF;

    IF TG_OP = 'UPDATE' AND NEW.updated_at = OLD.updated_at THEN
        NEW.updated_at := now();
    END IF;

    RETURN NEW;
END $$;
-- +goose StatementEnd
