-- +goose Up

-- "Random book": swipe through the library one cover at a time and keep what
-- appeals.
--
-- What a reader keeps lands on an ordinary shelf rather than in a table of its
-- own, because a shelf is already everything this needs: it appears in the
-- sidebar, it can be shared, and it can be marked for Kobo sync -- so a book
-- kept on a phone reaches the reader without another thought. A parallel
-- structure would have to reimplement all of that and would still not sync.
--
-- The flag exists so the shelf can be found again without matching on a name a
-- reader is free to rename.
ALTER TABLE shelves ADD COLUMN is_discovery boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX shelves_discovery_key ON shelves (user_id) WHERE is_discovery;

-- Everyone who already has an account gets theirs now, so the feature is
-- furnished on first visit rather than empty.
INSERT INTO shelves (user_id, name, is_discovery, position)
SELECT id, 'Discoveries', true, -1 FROM users
ON CONFLICT DO NOTHING;

-- A pass is a decision, and has to be remembered or the same book comes back on
-- the next shuffle. Kept apart from the shelf: the shelf is a list a reader
-- curates, this is a record of what they have already been shown.
CREATE TABLE discovery_passes (
    user_id   bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    book_id   bigint      NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    passed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, book_id)
);

-- +goose Down
DROP TABLE IF EXISTS discovery_passes;
DROP INDEX IF EXISTS shelves_discovery_key;
ALTER TABLE shelves DROP COLUMN IF EXISTS is_discovery;
