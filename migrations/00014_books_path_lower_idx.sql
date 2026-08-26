-- +goose Up

-- Supports the case-insensitive collision check in filestore.resolveDir.
--
-- Two books whose titles differ only in capitalisation -- "Hjärnstark" and
-- "HJÄRNSTARK", of which this library has eleven pairs -- render to directories
-- that are distinct on a case-sensitive filesystem and the same everywhere
-- else. Filing them apart keeps the tree portable to Windows, macOS and exFAT,
-- where the second would otherwise overwrite the first.
--
-- Without this index the check is a sequential scan, and reorganize performs
-- one per book: 28,000 scans of a 28,000-row table.
CREATE INDEX books_path_lower_idx ON books (lower(path));

-- +goose Down
DROP INDEX IF EXISTS books_path_lower_idx;
