-- +goose Up

-- Lets a user sync a shelf someone else owns to their own Kobo.
--
-- shelves.kobo_sync means "sync this to the OWNER's devices", which is why a
-- shared shelf could never reach anyone else's reader however it was toggled:
-- the sync query is scoped by shelf owner. Subscription is the other half --
-- "also send me this shared shelf" -- and it belongs to the subscriber, so one
-- person opting in never changes what anybody else's device receives.
CREATE TABLE shelf_kobo_subscriptions (
    user_id    bigint      NOT NULL REFERENCES users   (id) ON DELETE CASCADE,
    shelf_id   bigint      NOT NULL REFERENCES shelves (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, shelf_id)
);
CREATE INDEX shelf_kobo_subscriptions_shelf_idx ON shelf_kobo_subscriptions (shelf_id);

-- +goose Down
DROP TABLE IF EXISTS shelf_kobo_subscriptions;
