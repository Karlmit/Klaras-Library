-- +goose Up

-- Distinguish "we told the device about this book" from "the device kept it".
--
-- kobo_synced_books previously recorded the former and the sync query read it
-- as the latter: a book present in the table was announced as a
-- ChangedEntitlement, which tells the device it already owns the book and only
-- needs new metadata. It then downloads nothing.
--
-- That is only correct if the device actually received the response. Every sync
-- the device abandons -- a dropped connection, a proxy that never forwarded it,
-- a diagnostic request made on its behalf -- marked the books as sent anyway,
-- and from then on they could never download. The failure is silent and
-- permanent: sync reports success, the collection appears, the books do not.
--
-- The device tells us what it kept, in the sync token it sends next time. A
-- book is confirmed once a later request arrives bearing a watermark at or past
-- the one issued alongside it. Until then it stays new, and is re-announced.
-- Re-announcing a book the device already has is harmless; failing to announce
-- one it lacks is not, so the ambiguity now resolves in the safe direction.
ALTER TABLE kobo_synced_books
    ADD COLUMN announced_watermark timestamptz,
    ADD COLUMN confirmed           boolean NOT NULL DEFAULT false;

-- Existing rows carry no evidence either way: the old table could not tell the
-- two states apart. They are therefore left unconfirmed, so every book is
-- announced once more and any that a device never actually received arrives
-- properly this time. The cost is one re-download per book, once.
UPDATE kobo_synced_books SET confirmed = false;

CREATE INDEX kobo_synced_books_unconfirmed_idx
    ON kobo_synced_books (user_id, announced_watermark)
    WHERE NOT confirmed;

-- +goose Down
DROP INDEX IF EXISTS kobo_synced_books_unconfirmed_idx;
ALTER TABLE kobo_synced_books
    DROP COLUMN IF EXISTS announced_watermark,
    DROP COLUMN IF EXISTS confirmed;
