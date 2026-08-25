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

Copy this into `docker-compose.yml`, change the four marked values, then
`docker compose up -d` and open `http://<your-server>:8084`.

```yaml
services:
  klaras-library:
    image: ghcr.io/karlmit/klaras-library:latest
    container_name: klaras-library
    # Unraid's nobody:users. Without this the container runs as uid 1000 and
    # cannot write to your shares, so ingest, covers and reorganize all fail.
    user: "99:100"
    environment:
      - TZ=Europe/Stockholm
      - KLARAS_DATABASE_URL=postgres://klaras:CHANGE_ME_DB_PASSWORD@klaras-postgres:5432/klaras?sslmode=disable
      # The public HTTPS URL this server is reached at. Kobo devices are handed
      # absolute download URLs, so this must be what the DEVICE can resolve,
      # not the container. Must be https: Kobo fails silently on plain http.
      - KLARAS_EXTERNAL_URL=https://library.example.com
      - KLARAS_LOG_LEVEL=info
      # Let the device still reach the real Kobo shop for anything that is not
      # your library. "false" keeps it from talking to Kobo at all.
      - KLARAS_KOBO_PROXY_STORE=false
    volumes:
      # Your ebook library. Klaras owns this tree and moves files within it, so
      # point it at the library itself, not a parent folder.
      - /mnt/user/books/library:/library
      # Drop ebook files here and they are imported automatically.
      - /mnt/user/books/ingest:/ingest
      # Cover thumbnails and converted KEPUBs. About 170 kB per book, so ~5 GB
      # for 28,000 books. Safe to delete; it regenerates.
      - /mnt/user/appdata/klaras-library/cache:/cache
    ports:
      # 8083 is usually already taken by calibre-web, so the host side is 8084.
      - 8084:8083
    depends_on:
      klaras-postgres:
        condition: service_healthy
    restart: unless-stopped

  klaras-postgres:
    image: postgres:17-alpine
    container_name: klaras-postgres
    environment:
      - TZ=Europe/Stockholm
      - POSTGRES_USER=klaras
      - POSTGRES_DB=klaras
      # Must match the password in KLARAS_DATABASE_URL above.
      - POSTGRES_PASSWORD=CHANGE_ME_DB_PASSWORD
      # Swedish collation: å, ä and ö are distinct letters sorting after z, not
      # variants of a and o. This can only be set when the database is first
      # created, so choose it before the first start.
      - POSTGRES_INITDB_ARGS=--locale-provider=icu --icu-locale=sv-SE --encoding=UTF8
    volumes:
      - /mnt/user/appdata/klaras-library/postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U klaras -d klaras"]
      interval: 5s
      timeout: 5s
      retries: 20
    restart: unless-stopped
```

The four things to change:

| | |
|---|---|
| `CHANGE_ME_DB_PASSWORD` | in **both** places — they must match |
| `KLARAS_EXTERNAL_URL` | the public https URL, as the Kobo will see it |
| `/mnt/user/books/library` | your library folder |
| `TZ` | your timezone |

Two Unraid details worth not skipping:

- **`user: "99:100"`** is required. Without it the container runs as uid 1000
  and cannot write to your shares, so ingest, cover generation and reorganize
  all fail with permission errors.
- **`8084:8083`** because calibre-web usually already holds 8083. Run both side
  by side until you are happy, then switch.

The image is published to GitHub Container Registry for `linux/amd64` and
`linux/arm64`:

```
ghcr.io/karlmit/klaras-library:latest
ghcr.io/karlmit/klaras-library:1.0
ghcr.io/karlmit/klaras-library:1.0.1
```

`:latest` is republished on every release, so Unraid's update check sees the
new digest and offers the update.

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

## Order of operations on first run

Everything up to the last step is reversible. `reorganize` is the point of no
return, because it is what makes your existing calibre-web unusable.

1. **`docker compose up -d`** — creates an empty database and starts the server.
2. **Import** (below). Both source databases are opened read-only, so
   calibre-web keeps working and nothing on disk changes.
3. **Browse and search.** Check the books, covers and categories look right.
4. **Point one Kobo at it** and confirm a shelf syncs and a book opens.
5. **Live with it for a while.** Until this point, rolling back is just
   pointing the device's `api_store` back at calibre-web.
6. **Only then `reorganize`** — and review the `--dry-run` plan first.

Do not run `reorganize` before step 1. It never touches `metadata.db`, so
moving files first would leave Calibre's catalogue pointing at the old paths,
and the import in step 2 would then produce a library where every book points
at a file that is no longer there.

If the new layout turns out wrong, the move journal makes it undoable:

```bash
docker compose exec klaras-library klaras revert-moves --since 24h --dry-run
docker compose exec klaras-library klaras revert-moves --since 24h --yes
```

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
klaras revert-moves         Undo file moves, using the journal
klaras doctor               Report library problems (read-only)
klaras migrate [up|down|status]
klaras dev-seed --books N   Synthetic books, for benchmarking on your own hardware
```

`reorganize` moves files and refuses to run without `--dry-run` first. On a
library imported from Calibre it will move **every** book, because Calibre
transliterates non-ASCII characters out of folder names: `A U Baath/Islandska
sagor (8)` becomes `Bååth, A U/Isländska sagor`. Review the plan, and see
**Order of operations** above for when to run it.

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
