-- +goose Up

-- Where the in-browser reader left off.
--
-- Kept apart from location_value, which holds the Kobo's own position: the
-- device speaks KoboSpan and epub.js speaks CFI, so the two cannot share a
-- field. progress_percent and status ARE shared, which is the part that is
-- meaningful on both -- finish a book in the browser and the Kobo agrees.
ALTER TABLE reading_state ADD COLUMN web_location text;

-- +goose Down
ALTER TABLE reading_state DROP COLUMN IF EXISTS web_location;
