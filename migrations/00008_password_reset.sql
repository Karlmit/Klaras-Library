-- +goose Up

-- calibre-web stores werkzeug scrypt hashes; Klaras uses argon2id. Hashes
-- cannot be converted, so imported users arrive with an unusable placeholder
-- and must set a new password before they can log in.
ALTER TABLE users ADD COLUMN password_reset_required boolean NOT NULL DEFAULT false;

-- Provenance, so a re-import can match users up again.
ALTER TABLE users   ADD COLUMN calibreweb_id bigint;
ALTER TABLE shelves ADD COLUMN calibreweb_id bigint;
CREATE UNIQUE INDEX users_calibreweb_id_key   ON users   (calibreweb_id) WHERE calibreweb_id IS NOT NULL;
CREATE UNIQUE INDEX shelves_calibreweb_id_key ON shelves (calibreweb_id) WHERE calibreweb_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS shelves_calibreweb_id_key;
DROP INDEX IF EXISTS users_calibreweb_id_key;
ALTER TABLE shelves DROP COLUMN IF EXISTS calibreweb_id;
ALTER TABLE users   DROP COLUMN IF EXISTS calibreweb_id;
ALTER TABLE users   DROP COLUMN IF EXISTS password_reset_required;
