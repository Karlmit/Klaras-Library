<div align="center">

<img src="web/src/assets/brand/logo-256.png" width="128" alt="">

# Klaras Library

**A fast, self-hosted ebook library with Kobo sync.**
A rebuild of [calibre-web](https://github.com/janeczku/calibre-web) in Go and PostgreSQL,
built for libraries in the tens of thousands of books.

</div>

## Why

calibre-web is functionally good and gets slow at scale. The cause is not its
language. A [sync timeout report](https://github.com/crocodilestick/Calibre-Web-Automated/issues/1112)
on a 13,000-book library shows a Kobo sync limited to a 63-book shelf *still*
scanning all 13,000 rows — 13s for changed entries, 11s for filtering, 10s for
covers — and blowing past the device's ~30 second HTTP timeout.

The fixes are architectural: scope every query to what was asked for, paginate
with keyset cursors instead of `OFFSET`, denormalise the columns the list view
reads, pre-generate covers, and convert KEPUB ahead of time instead of while the
device waits.

Measured on a real 28,038-book library:

| | calibre-web | Klaras Library |
|---|---|---|
| Grid, first page | — | **0.20 ms** |
| Grid, page ~25,000 | degrades with depth | **0.23 ms** (flat) |
| Same page via `OFFSET` | what calibre-web does | 38 ms, scans every row |
| Full-text search | — | **0.65 ms** |
| **Kobo sync (52 books on 7 shelves)** | 30 s+, times out | **0.57 ms**, reads 56 rows |
| Import a Calibre library | — | **7 s** for 28,038 books |

These are asserted by tests, not measured once: the suite fails the build if any
critical query starts sequentially scanning `books`.

## Features

- **Kobo sync** — shelves become Collections on the device. Wire-compatible with
  calibre-web's sync token, so a paired device needs only a URL change.
- **Automatic EPUB → KEPUB** — in-process via [kepubify](https://github.com/pgaskin/kepubify),
  converted by background workers and never during a sync request.
- **Calibre-style management** — bulk metadata editing (which calibre-web lacks),
  cover art, series, categories, upload.
- **Managed file tree** — `Author/Series/Title`, with every move journalled so a
  crash cannot leave the database and the disk disagreeing.
- **Watch folder** — drop files in and they are imported, filed and converted.
- **Metadata lookup** — Google Books and Open Library, reviewed before applying.
- **OPDS 1.2 and 2.0** — for KOReader, Moon+ Reader and friends.
- **In-browser reader** — lazy-loaded, so it costs nothing until used.
- **Multi-user** — admin, editor and reader roles, per-user shelves and Kobo tokens.

## Quick start

```bash
curl -O https://raw.githubusercontent.com/Karlmit/Klaras-Library/main/docker-compose.example.yml
mv docker-compose.example.yml docker-compose.yml
# edit the four values marked CHANGE ME
docker compose up -d
```

Open `http://<your-server>:8084` and create the first admin account.

The image is published to GitHub Container Registry for `linux/amd64` and
`linux/arm64`:

```
ghcr.io/karlmit/klaras-library:latest
ghcr.io/karlmit/klaras-library:1.0.0
```

`:latest` is republished on every release, so Unraid's update check sees the new
digest and offers the update.

## Importing an existing Calibre library

Both source databases are opened **read-only and immutable**, so a running
calibre-web is unaffected. `--dry-run` performs the whole import in a
transaction and rolls it back, reporting exactly what a real run would do.

```bash
docker compose exec klaras klaras import \
  --calibre-library /library \
  --calibre-web-db /calibre-web/app.db \
  --dry-run
```

What comes across:

- Books, authors, series, publishers, categories, ratings, identifiers, files
- **Calibre UUIDs**, so existing Kobo entitlements keep working
- **Calibre's `last_modified`**, which drives Kobo incremental sync
- From calibre-web's `app.db`: users, shelves, their Kobo sync flags, sync
  tokens and reading state

Passwords cannot come across — calibre-web uses werkzeug scrypt, this uses
argon2id — so imported users set a new password on first login.

Suspect data is imported faithfully and **flagged**, never silently corrected.
On the library this was built against that surfaced 1,115 books with a merged
author name (`Adler-Olsen;Jussi`, `A. Trunk| P. Erlandsson`), 738 possible
duplicates and 5 implausible dates. They appear under **Needs review** in the UI.

## Kobo setup

1. In the web UI, create a sync token for your user. You are given the exact URL
   the device needs.
2. On the Kobo, set `api_store` in `.kobo/Kobo/Kobo eReader.conf` to that URL.
3. Mark a shelf **Kobo sync**. Its books appear on the device as a Collection.

Two things bite people repeatedly with calibre-web and apply equally here:

- **TLS 1.2 must be enabled on your reverse proxy.** Kobo firmware ships an old
  TLS client, and a "modern compatibility" profile that permits only TLS 1.3
  will fail.
- **Forward `X-Forwarded-Proto: https`.** Without it the device is handed
  `http://` download URLs, which fail silently.

`KLARAS_EXTERNAL_URL` must be the URL the *device* can reach. The server refuses
to start if it is not https.

## Swedish

The library this was built for is 94% Swedish, which drives two schema-level
choices. Both are named aliases defined in `migrations/00001`, so the language
lives in one place:

- **`library_sort`** — ICU `sv-SE`. Å, Ä and Ö are distinct letters sorting
  *after* Z, not variants of A and O. A generic ICU locale files "Åkesson"
  under A.
- **`library_search`** — Postgres' `swedish` text-search config, which folds
  inflection: `flicka` finds *Flickorna*, `mörda` finds *Mördaren*.

With accent folding on top, typing `rodluvan` on a keyboard without Swedish keys
finds *Rödluvan*. To use another language, change those two definitions and
`POSTGRES_INITDB_ARGS`.

## Commands

```
klaras serve                Run the server (default)
klaras import               Import a Calibre library and calibre-web state
klaras reorganize           Bring the file tree in line with the path template
klaras backfill-covers      Generate thumbnails for the whole library
klaras doctor               Report library problems (read-only)
klaras migrate [up|down|status]
klaras dev-seed --books N   Synthetic books, for benchmarking on your own hardware
```

`reorganize` moves files and refuses to run without `--dry-run` first.

## Development

Requires Go 1.27+, Node 22+ and Docker.

```bash
make dev-up            # dev + test Postgres
make migrate
make seed              # 30k synthetic books
make run               # :8083
make test-integration
make help
```

## Licence

Not yet chosen. This is a clean-room rebuild rather than a fork of calibre-web
(which is GPL-3.0), so no licence is inherited.
