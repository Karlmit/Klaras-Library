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
- **Metadata lookup** — Apple Books, Google Books and Open Library, searched together. Results open
  as a field-by-field checklist. Each found value sits in an editable field with what
  the book holds today as a caption beneath it, so a value can be corrected before it
  is taken. The cover is shown new beside current, both with their real pixel
  dimensions. Fields are ticked by default only where the book has nothing, so a wrong
  edition cannot overwrite good metadata, and nothing but the cover is written until
  you press Save. The search terms are editable and a single source can be picked; a
  source that fails says so rather than looking like no match.
- **Covers** — upload a file, paste an image address, or pick from the covers the
  providers know about, shown side by side with their resolutions. Choosing one selects
  it; replacing takes a second, explicit press. Remote images are proxied through the
  app, so the browser never tells Apple or Google which books are here. Any public host
  is allowed; anything resolving inside the network is refused at connect time, on every
  redirect hop.

  On a sample of 24 random books from this library, Apple Books had a cover for 83%,
  Google Books 66%, and Open Library 4% — Open Library earns its place on series data
  and obscure records, not on covers.
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
      # true  = the shop, your Kobo account and any purchased or borrowed Kobo
      #         books keep working, merged into the same sync.
      # false = the device talks only to this server. More private, but
      #         purchased Kobo books stop appearing.
      - KLARAS_KOBO_PROXY_STORE=true
    volumes:
      # Your ebook library. Klaras owns this tree and moves files within it, so
      # point it at the library itself, not a parent folder.
      - /mnt/user/books/library:/library
      # Drop ebook files here and they are imported automatically.
      - /mnt/user/books/ingest:/ingest
      # calibre-web's own state, read-only, for the one-time import. It holds
      # your shelves, their Kobo sync flags, users and sync tokens -- none of
      # which live in Calibre's metadata.db. Remove this line once imported.
      - /mnt/user/appdata/calibre-web:/calibre-web:ro
      # Cover thumbnails and converted KEPUBs. About 170 kB per book, so ~5 GB
      # for 28,000 books. Safe to delete; it regenerates.
      #
      # Create it before the first start, or Docker will make it owned by root
      # and the container cannot write to it:
      #     mkdir -p /mnt/user/appdata/klaras-library/cache
      #     chown -R 99:100 /mnt/user/appdata/klaras-library/cache
      # Chown that path only. The postgres directory below belongs to uid 70
      # and Postgres will not start if it is changed.
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
      # Owned by uid 70 (the postgres user in the alpine image). Never chown
      # this to 99:100 -- Postgres refuses to start on a data directory it does
      # not own, with "could not open file global/pg_filenode.map".
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

### Create the cache directory first

Docker creates a missing bind-mount directory owned by **root**, which the
container (running as `99:100`) cannot write to. Every cover then silently
falls back to a placeholder.

```bash
mkdir -p /mnt/user/appdata/klaras-library/cache
chown -R 99:100 /mnt/user/appdata/klaras-library/cache
```

**Chown that path only.** Do not chown the parent `klaras-library` folder: it
also holds the Postgres data directory, which belongs to **uid 70** in the
alpine image. Changing it makes Postgres refuse to start with
`could not open file "global/pg_filenode.map": Permission denied`. If that has
already happened, the data is fine and the fix is:

```bash
chown -R 70:70 /mnt/user/appdata/klaras-library/postgres
```

Two more Unraid details worth not skipping:

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
docker compose exec klaras-library klaras import \
  --calibre-library /library \
  --calibre-web-db /calibre-web/app.db \
  --dry-run
```

Drop `--dry-run` when the numbers look right. `--calibre-web-db` needs the
calibre-web appdata mount shown in the compose above; without it you still get
the whole library, but no shelves, users or Kobo tokens.

What comes across:

- Books, authors, series, publishers, categories, ratings, identifiers, files
- **Calibre UUIDs**, so existing Kobo entitlements keep working
- **Calibre's `last_modified`**, which drives Kobo incremental sync
- From calibre-web's `app.db`: users, shelves, their Kobo sync flags, sync
  tokens and reading state

### Imported users need a password

calibre-web stores werkzeug scrypt hashes and this uses argon2id, so passwords
cannot come across. Imported accounts arrive unusable on purpose, and an
administrator sets a password for each:

```bash
docker compose exec klaras-library klaras users
docker compose exec -it klaras-library klaras passwd klarasvensson
```

`users` lists every account, which shelves and Kobo tokens they own, and
whether they can log in yet. `passwd` prompts without echoing; pass
`--password` instead if you are scripting it. The same thing is available to
admins in the web UI.

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

### Moving a device across from calibre-web

**No new sync token is needed.** The import brings your existing token over
unchanged, so the device's credentials still work. Only the hostname has to
point somewhere new, and there are two ways to do that:

- **Repoint your existing hostname** at the new container. Nothing changes on
  the device at all, and rolling back is just pointing it at calibre-web again.
  This is the smoother option.
- **Use a new hostname**, and edit `api_store` in `.kobo/Kobo/Kobo eReader.conf`
  on the device. Useful if you want both servers reachable at once.

The device also keeps its sync position, because the token format is
wire-compatible with calibre-web's, field for field. It will resume rather than
re-download the shelf.

For a device that has never synced, create a token in the web UI and you are
given the exact `api_store` URL to paste.

### Kobo store proxying

`KLARAS_KOBO_PROXY_STORE` decides what happens to the requests the device makes
that are nothing to do with your library — the shop, recommendations, your Kobo
account, Kobo Plus, OverDrive.

| | `true` | `false` |
|---|---|---|
| Your library | syncs | syncs |
| Books bought from Kobo | sync, merged into the same response | do not appear |
| Shop / account / Kobo Plus screens | work | empty |
| Device talks to Kobo | yes | never |
| Outbound call per sync | yes, ~12s cap, fails soft | no |

**Set it to `true` if the device is ever used with the Kobo store.** Setting it
to `false` makes the device talk only to your server, which is more private and
one less moving part, but purchased and borrowed books stop arriving.

If you are migrating from calibre-web, match what you had: check
`config_kobo_proxy` in its `app.db`, or the "Proxy unknown requests to Kobo
Store" checkbox in its admin page.

When proxying is on, the sync response merges Kobo's entitlements with your
library's, and the store's own sync position is carried inside our token — so
both stay in step over the single header the device knows about. If Kobo is
slow or unreachable, that round is skipped and only your library is served; the
store position is not advanced, so nothing is lost.

### Reverse proxy

**No special configuration is needed.** A plain proxy to the container is
enough; the defaults in Nginx Proxy Manager, Caddy or Traefik will do.

Two things genuinely do matter:

- **TLS 1.2 must be enabled.** Kobo firmware ships an old TLS client, and a
  "modern compatibility" profile that permits only TLS 1.3 will fail. This is
  a setting on your proxy, not here.
- **`/kobo/` must not sit behind SSO.** See below; this one fails silently, with
  every request returning 200.

Notably **not** required, unlike calibre-web: forwarding `X-Forwarded-Proto`
or `X-Scheme`. Download URLs are built from `KLARAS_EXTERNAL_URL` rather than
guessed from the request, so the "Kobo gets http:// links and fails silently"
problem cannot happen. `KLARAS_EXTERNAL_URL` must be the URL the *device* can
reach, and the server refuses to start if it is not https.

#### If SSO sits in front of the library

**Keep the `/kobo/` carve-out.** If a forward-auth layer such as authentik,
Authelia or oauth2-proxy guards the host, device traffic must be exempted from
it. A Kobo has no browser, no cookie jar and no way to complete a login
redirect, so anything that expects it to authenticate interactively will fail.

An earlier version of this file said the carve-out could be deleted once you
moved off calibre-web. That was wrong, and it is worth being specific about the
failure it causes, because nothing in either application's logs points at it:
sync appears to work. Requests reach the server, every one returns 200, and the
device still reports "sync failed" -- because the auth layer is rewriting
responses on the way back out, which the application behind it cannot see.

For Nginx Proxy Manager with authentik, keep a block like this in the proxy
host's **Advanced** tab, pointed at Klaras Library's port:

```nginx
location ^~ /kobo/ {
    auth_request off;                        # a Kobo cannot do SSO

    proxy_pass http://192.168.1.66:8084;     # Klaras Library

    proxy_set_header Host             $http_host;
    proxy_set_header X-Real-IP        $remote_addr;
    proxy_set_header X-Forwarded-For  $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;

    proxy_set_header Connection "";          # not a websocket
    proxy_redirect off;
}
```

Two lines carry the weight. `auth_request off` keeps the SSO layer out of the
device's way, and with it the `Set-Cookie` such layers staple onto every
response. `Connection ""` undoes the `Connection 'upgrade'` that the stock
proxy block sets on *all* traffic, not just websockets.

The browser UI stays behind SSO, which is the arrangement you want: the device
is authenticated by the secret in its own URL, which is the only credential it
has.

Do read **Exposing it to the internet** below.

### Exposing it to the internet

Klaras Library is designed to be reachable directly, because Kobo devices
cannot pass through an SSO login. Three surfaces authenticate:

| Surface | Credential |
|---|---|
| Web UI | session cookie, argon2id password |
| `/opds` | HTTP Basic, same accounts |
| `/kobo/{token}` | 128-bit token in the URL, per device |

Repeated failures are locked out: eight in fifteen minutes trips a fifteen
minute block, counted per client address **and** per username, so neither a
single host hammering many accounts nor a distributed run against one account
gets through. A successful login clears the counter, and a paired Kobo polling
normally is never affected because only failures count.

If you previously relied on an SSO proxy for protection, that is what replaces
it. Use a long passphrase for every account.

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
klaras users                List accounts, their roles and paired devices
klaras passwd USERNAME      Set someone's password
klaras kobo-resync USERNAME Offer every book to their Kobo again
klaras relink               Repoint books at files that are on disk under
                            another name
klaras scan-adult [--dry-run]
                            Find erotica and hide it from non-administrators
klaras fetch-descriptions [--dry-run] [--limit N] [--files-only]
                            Fill in missing descriptions
klaras migrate [up|down|status]
klaras dev-seed --books N   Synthetic books, for benchmarking on your own hardware
klaras version
```

Everything an operator needs day to day is also in the browser, under
**Settings**: accounts and passwords on the Users tab, and `kobo-resync` as
**Force a full resync** on the Kobo tab — or the `N · resync` button beside an
account on the Users tab, to reset someone else's device. Reach for the CLI when
the browser is not an option, not as the normal path.

### Missing descriptions

Roughly half an imported library arrives with no description, which makes both
the book page and **Random book** thin. Two sources fill them in, and the server
works through them on its own once a day.

**The books' own files**, first, because they are free and authoritative.
Several Swedish publishers ship the back-cover text as a content page -- named
`bookinfo` or `about_book`, or marked with Storytel's *HOPPA ÖVER INTROTEXT*.
About one book in eleven has one. Colophons and author biographies are refused;
they sit in the same place and are not blurbs.

**Google Books**, second, for books with an ISBN. Set `KLARAS_GOOGLE_BOOKS_KEY`
to a free key from the [Google Cloud console](https://console.cloud.google.com/apis/credentials)
and restrict it to the Books API. Around half of Swedish ISBNs come back with a
Swedish description. Without a key only the keyless per-address quota is
available, which is exhausted almost immediately at this volume.

Matching is on ISBN alone, never on title and author, and the returned title
still has to resemble the one on file. A wrong blurb is worse than none: it
reads as authoritative and nobody re-checks it.

**Settings → Descriptions** shows how it is getting on: how much of the library
has a description, how many books are still to try, how many are out of reach
(no ISBN, or Google had no record), what the last fortnight looked like, when it
last ran, and roughly how many more nights it needs. **Run now** starts a pass
immediately rather than waiting for tonight.

The free quota is 1,000 lookups a day, so a large library takes a week or two.
Every attempt is recorded, successful or not, so each night continues where the
last stopped instead of re-asking the same first thousand. `klaras
fetch-descriptions` runs the same thing by hand.

### Shelves

A book's **Shelves** button lists your own shelves with a tick beside the ones
it is on; the row is a toggle, so the same control puts a book on a shelf and
takes it off. Removing is also a bulk action: open a shelf from the sidebar,
select books, and **Remove from "…"** appears in the bulk bar. That is the
practical way to prune a shelf Random book has filled.

Taking a book off a Kobo-synced shelf writes a tombstone, so the device drops it
on the next sync rather than keeping it for ever.

### Random book

A discovery screen: one cover at a time, kept or passed with a swipe, the arrow
keys, or the buttons. It is the top entry in the sidebar.

What a reader keeps goes onto an ordinary shelf called **Discoveries**, one per
account, created automatically. That is the point of using a shelf rather than
a private list: it can be marked **Sync to Kobo** like any other, so swiping
right on a phone puts the book on the reader. It can also be renamed -- the
shelf is found by a flag, not by its name.

Only books with a cover are offered, since a card is mostly cover. Adult
content never appears. A pass is remembered, so the same book does not come
round again; Undo takes back the last decision either way.

### Adult content

`klaras scan-adult --dry-run` identifies erotica from metadata and lists it;
without `--dry-run` it flags what it finds. Flagged books are hidden from every
account except administrators -- absent from the grid, from search and
suggestions, from the sidebar counts, from OPDS, and from Kobo sync, and their
detail, cover and download URLs answer 404 rather than 403, so the flag itself
stays private.

Administrators see them under **Adult content** in the sidebar, which is their
own view rather than a filter on the current one. Select books there and
**Not adult — restore** returns them to the library; a restored book is
remembered, so a later scan will not flag it again. **Delete** removes the rest,
files included, and asks you to type the count first.

Two rules, in `internal/library/adult.go`:

- **Publisher**, for imprints that publish nothing else. This is the stronger
  signal: Lusthuset's catalogue is entirely erotica but the word appears in
  about 1% of its metadata, so a keyword scan finds almost none of it.
  Deliberately excludes mixed houses -- flagging a general publisher's crime
  novels would be worse than missing a few titles.
- **Keywords**, on title, description, series and tags, and narrow on purpose.
  "Sex" alone matches sexdagarskriget, sextiotalet and every book about sex
  education; the Swedish stems for *erotic* are specific enough to stand alone.

Flagging does not touch `updated_at`, so it does not tell every paired Kobo that
a thousand books have changed.

### When doctor reports files missing on disk

`klaras doctor` compares every recorded file against the filesystem. A non-zero
"files missing on disk" usually does not mean anything was lost -- the common
cause is a name that differs from the real one by a character nobody can see.
Calibre truncates long filenames and can leave a trailing space before the
extension, so the imported name and the file drift apart.

`klaras relink --dry-run` finds each absent file by size and extension rather
than by name, since the name is the thing that is wrong, and reports what it
would repoint. Without `--dry-run` it commits. A book is only relinked when
exactly one file of the right size and type is unaccounted for anywhere in the
library; anything ambiguous is reported and left alone.

Relinked books point at wherever their file actually is, which may be an old
folder, so run `reorganize` again afterwards to file them properly.

### When a Kobo syncs but no books arrive

Run a resync. Klaras Library keeps a record of what each device has been told,
and describes a book the device already holds as changed rather than new, so it
is not downloaded again. When that record and the device disagree — after a
factory reset, a restore from backup, or a sync that never finished — books can
be described as already-owned and quietly skipped. Sync reports success and the
collection stays empty.

Forgetting the record is safe: nothing is deleted and no metadata changes. The
device downloads the books again, which takes a few minutes.

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
