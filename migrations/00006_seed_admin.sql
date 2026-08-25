-- +goose Up
-- Nothing seeded here on purpose: the first-run flow creates the admin account
-- interactively so no default credentials ever exist. This migration exists as
-- the anchor point for future data fixups.
SELECT 1;

-- +goose Down
SELECT 1;
