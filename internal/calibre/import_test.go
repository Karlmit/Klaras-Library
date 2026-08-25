package calibre_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	"github.com/Karlmit/Klaras-Library/internal/calibre"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/internal/testdb"
)

// buildFixtureLibrary writes a miniature Calibre library that exercises the
// cases that actually caused trouble against the real 28k library:
//
//   - timestamps in the form modernc.org/sqlite hands back (T...Z), which once
//     silently failed to parse for every row
//   - Calibre's 0101-01-01 "no date" sentinel
//   - Swedish characters in titles and author names
//   - an author name with a ';' separator (a merged/sort-name record)
//   - a book with no file at all
//   - KEPUB alongside EPUB, and a book missing its KEPUB
func buildFixtureLibrary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT NOT NULL DEFAULT 'Unknown', sort TEXT,
  timestamp TIMESTAMP, pubdate TIMESTAMP, series_index REAL NOT NULL DEFAULT 1.0,
  author_sort TEXT, path TEXT NOT NULL DEFAULT '', uuid TEXT, has_cover BOOL DEFAULT 0,
  last_modified TIMESTAMP NOT NULL DEFAULT '2000-01-01 00:00:00+00:00');
CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL, sort TEXT, link TEXT DEFAULT '');
CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT NOT NULL, sort TEXT);
CREATE TABLE publishers (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE comments (id INTEGER PRIMARY KEY, book INTEGER, text TEXT);
CREATE TABLE ratings (id INTEGER PRIMARY KEY, rating INTEGER);
CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT);
CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER, type TEXT, val TEXT);
CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER, format TEXT, uncompressed_size INTEGER, name TEXT);
CREATE TABLE custom_columns (id INTEGER PRIMARY KEY, label TEXT, name TEXT, datatype TEXT, is_multiple BOOL);
CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER);
CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER, series INTEGER);
CREATE TABLE books_publishers_link (id INTEGER PRIMARY KEY, book INTEGER, publisher INTEGER);
CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER, tag INTEGER);
CREATE TABLE books_ratings_link (id INTEGER PRIMARY KEY, book INTEGER, rating INTEGER);
CREATE TABLE books_languages_link (id INTEGER PRIMARY KEY, book INTEGER, lang_code INTEGER, item_order INTEGER DEFAULT 0);

INSERT INTO authors (id,name,sort) VALUES
  (1,'Camilla Läckberg','Läckberg, Camilla'),
  (2,'Adler-Olsen;Jussi','Adler-Olsen, Jussi'),
  (3,'Susanne Åkesson','Åkesson, Susanne');
INSERT INTO series (id,name,sort) VALUES (1,'Fjällbacka','Fjällbacka');
INSERT INTO publishers (id,name) VALUES (1,'Norstedts');
INSERT INTO tags (id,name) VALUES (1,'Deckare'),(2,'Svensk');
INSERT INTO languages (id,lang_code) VALUES (1,'swe'),(2,'eng');
INSERT INTO ratings (id,rating) VALUES (1,8);

-- 1: full metadata, in a series, both formats
INSERT INTO books (id,title,sort,timestamp,pubdate,series_index,author_sort,path,uuid,has_cover,last_modified)
 VALUES (1,'Isprinsessan','Isprinsessan','2024-11-29 11:10:48.441849+00:00','2003-05-01 00:00:00+00:00',
         1.0,'Läckberg, Camilla','Camilla Lackberg/Isprinsessan (1)','11111111-1111-1111-1111-111111111111',1,
         '2025-03-04 08:00:00+00:00');
-- 2: Calibre's no-date sentinel, merged author name, no KEPUB
INSERT INTO books (id,title,sort,timestamp,pubdate,author_sort,path,uuid,has_cover,last_modified)
 VALUES (2,'Kvinnan i buren','Kvinnan i buren','2024-12-01 21:12:33.854301+00:00','0101-01-01 00:00:00+00:00',
         'Adler-Olsen, Jussi','Adler-Olsen;Jussi/Kvinnan i buren (2)','22222222-2222-2222-2222-222222222222',1,
         '2025-06-11 09:30:00+00:00');
-- 3: leading article in the title, no files at all
INSERT INTO books (id,title,sort,timestamp,pubdate,author_sort,path,uuid,has_cover,last_modified)
 VALUES (3,'Den vita staden','vita staden, Den','2025-01-05 10:00:00+00:00','2019-09-09 00:00:00+00:00',
         'Åkesson, Susanne','Susanne Akesson/Den vita staden (3)','33333333-3333-3333-3333-333333333333',0,
         '2025-01-05 10:00:00+00:00');
-- 4: no UUID -- must be skipped, it could never sync to a Kobo
INSERT INTO books (id,title,timestamp,pubdate,author_sort,path,uuid,last_modified)
 VALUES (4,'Ingen UUID','2025-01-05 10:00:00+00:00','2019-09-09 00:00:00+00:00','X','x/4',NULL,
         '2025-01-05 10:00:00+00:00');

INSERT INTO books_authors_link (book,author) VALUES (1,1),(2,2),(3,3),(4,1);
INSERT INTO books_series_link (book,series) VALUES (1,1);
INSERT INTO books_publishers_link (book,publisher) VALUES (1,1);
INSERT INTO books_tags_link (book,tag) VALUES (1,1),(1,2),(2,1);
INSERT INTO books_ratings_link (book,rating) VALUES (1,1);
INSERT INTO books_languages_link (book,lang_code) VALUES (1,1),(2,1),(3,1);
INSERT INTO comments (book,text) VALUES (1,'En deckare fran Fjallbacka.');
INSERT INTO identifiers (book,type,val) VALUES (1,'isbn','9789113023472'),(1,'ISBN','dupe-should-drop');
INSERT INTO data (book,format,uncompressed_size,name) VALUES
  (1,'EPUB',500000,'Isprinsessan - Camilla Lackberg'),
  (1,'KEPUB',520000,'Isprinsessan - Camilla Lackberg'),
  (2,'EPUB',400000,'Kvinnan i buren - Jussi Adler-Olsen');
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return dir
}

func importTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "calibre")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := store.Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db, err := store.Open(ctx, dsn, 4, log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return db.Pool
}

func TestImportLibrary(t *testing.T) {
	pool := importTestPool(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	src, err := calibre.OpenSource(buildFixtureLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	res, err := calibre.ImportLibrary(ctx, pool, src,
		calibre.Options{Purge: true}, log)
	if err != nil {
		t.Fatal(err)
	}
	if res.Books != 3 {
		t.Errorf("imported %d books, want 3 (the UUID-less book must be skipped)", res.Books)
	}
	if res.Skipped["books_without_uuid"] != 1 {
		t.Errorf("expected exactly 1 book skipped for a missing UUID, got %v", res.Skipped)
	}

	t.Run("uuids are preserved verbatim", func(t *testing.T) {
		var uuid string
		if err := pool.QueryRow(ctx, `SELECT uuid::text FROM books WHERE id=1`).Scan(&uuid); err != nil {
			t.Fatal(err)
		}
		if uuid != "11111111-1111-1111-1111-111111111111" {
			t.Errorf("uuid = %q; existing Kobo entitlements depend on this matching Calibre", uuid)
		}
	})

	t.Run("calibre last_modified survives the denormalisation rebuild", func(t *testing.T) {
		// Regression guard: refresh_all_book_denorm UPDATEs every book, and the
		// books trigger auto-touches updated_at unless it is set explicitly.
		// Without the restore pass every book would carry the import time and
		// the first Kobo sync would resend the whole shelf.
		var updated time.Time
		if err := pool.QueryRow(ctx, `SELECT updated_at FROM books WHERE id=2`).Scan(&updated); err != nil {
			t.Fatal(err)
		}
		want := time.Date(2025, 6, 11, 9, 30, 0, 0, time.UTC)
		if !updated.UTC().Equal(want) {
			t.Errorf("updated_at = %s, want %s (Calibre's last_modified)", updated.UTC(), want)
		}
	})

	t.Run("no-date sentinel becomes NULL", func(t *testing.T) {
		var pubdate *time.Time
		if err := pool.QueryRow(ctx, `SELECT pubdate FROM books WHERE id=2`).Scan(&pubdate); err != nil {
			t.Fatal(err)
		}
		if pubdate != nil {
			t.Errorf("pubdate = %v, want NULL for Calibre's 0101-01-01 sentinel", pubdate)
		}
		// ...but a real date must survive.
		var real *time.Time
		if err := pool.QueryRow(ctx, `SELECT pubdate FROM books WHERE id=1`).Scan(&real); err != nil {
			t.Fatal(err)
		}
		if real == nil || real.Year() != 2003 {
			t.Errorf("pubdate for book 1 = %v, want 2003 (a real date must not be dropped)", real)
		}
	})

	t.Run("swedish characters and denormalisation", func(t *testing.T) {
		var title string
		var authors []string
		var series *string
		var tags []string
		if err := pool.QueryRow(ctx,
			`SELECT title, author_names, series_name, tag_names FROM books WHERE id=1`).
			Scan(&title, &authors, &series, &tags); err != nil {
			t.Fatal(err)
		}
		if len(authors) != 1 || authors[0] != "Camilla Läckberg" {
			t.Errorf("author_names = %v, want [Camilla Läckberg]", authors)
		}
		if series == nil || *series != "Fjällbacka" {
			t.Errorf("series_name = %v, want Fjällbacka", series)
		}
		if len(tags) != 2 {
			t.Errorf("tag_names = %v, want 2 tags", tags)
		}
	})

	t.Run("file names get the format extension", func(t *testing.T) {
		rows, err := pool.Query(ctx,
			`SELECT format, filename FROM book_files WHERE book_id=1 ORDER BY format`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var f, n string
			if err := rows.Scan(&f, &n); err != nil {
				t.Fatal(err)
			}
			got[f] = n
		}
		// Verified against the real library: Calibre writes ".kepub", not
		// ".kepub.epub".
		if got["EPUB"] != "Isprinsessan - Camilla Lackberg.epub" {
			t.Errorf("EPUB filename = %q", got["EPUB"])
		}
		if got["KEPUB"] != "Isprinsessan - Camilla Lackberg.kepub" {
			t.Errorf("KEPUB filename = %q", got["KEPUB"])
		}
	})

	t.Run("duplicate identifier schemes collapse", func(t *testing.T) {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM identifiers WHERE book_id=1 AND scheme='isbn'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("got %d isbn rows for book 1, want 1 (case-variant duplicates must collapse)", n)
		}
	})

	t.Run("quality issues are flagged not corrected", func(t *testing.T) {
		reasons := map[string]bool{}
		rows, err := pool.Query(ctx,
			`SELECT DISTINCT unnest(review_reasons) FROM books WHERE needs_review`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			reasons[r] = true
		}
		for _, want := range []string{"author_name_has_separator", "no_files"} {
			if !reasons[want] {
				t.Errorf("expected books flagged %q, got %v", want, reasons)
			}
		}
		// The merged author name must be imported as written, not "fixed".
		var name string
		if err := pool.QueryRow(ctx, `SELECT name FROM authors WHERE id=2`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Adler-Olsen;Jussi" {
			t.Errorf("author name = %q; suspect data must be imported verbatim and flagged", name)
		}
	})

	t.Run("refuses to overwrite without purge", func(t *testing.T) {
		src2, err := calibre.OpenSource(buildFixtureLibrary(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src2.Close()
		if _, err := calibre.ImportLibrary(ctx, pool, src2, calibre.Options{}, log); err == nil {
			t.Error("importing into a populated library should be refused without --purge")
		}
	})
}

// TestDryRunCoversTheAppDBImportToo is a regression guard.
//
// The two halves used to run in separate transactions, so --dry-run rolled the
// library back and the app.db pass then refused with "library is empty". That
// made the dry run useless for the thing people most need to check before
// committing: whether their users, shelves and Kobo tokens come across.
func TestDryRunCoversTheAppDBImportToo(t *testing.T) {
	pool := importTestPool(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	src, err := calibre.OpenSource(buildFixtureLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	app, err := calibre.OpenAppDB(buildFixtureAppDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// Start from empty so the dry run cannot be reading leftovers.
	if _, err := pool.Exec(ctx, `TRUNCATE books, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	res, ares, err := calibre.ImportAll(ctx, pool, src, app,
		calibre.Options{DryRun: true, Purge: true}, log)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if res.Books == 0 {
		t.Error("dry run reported no books")
	}
	if ares == nil {
		t.Fatal("dry run skipped the app.db import entirely")
	}
	if ares.Users == 0 {
		t.Error("dry run reported no users; this is exactly what people need to see")
	}
	if ares.Shelves == 0 {
		t.Error("dry run reported no shelves")
	}
	if ares.KoboTokens == 0 {
		t.Error("dry run reported no Kobo tokens")
	}

	// And nothing was actually written.
	var books, users int64
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM books), (SELECT count(*) FROM users)`).
		Scan(&books, &users); err != nil {
		t.Fatal(err)
	}
	if books != 0 || users != 0 {
		t.Errorf("dry run committed data: %d books, %d users", books, users)
	}
}

// TestImportAllIsAtomic checks the other half of sharing one transaction.
func TestImportAllIsAtomic(t *testing.T) {
	pool := importTestPool(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	src, err := calibre.OpenSource(buildFixtureLibrary(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	app, err := calibre.OpenAppDB(buildFixtureAppDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	res, ares, err := calibre.ImportAll(ctx, pool, src, app,
		calibre.Options{Purge: true}, log)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || !ares.Committed {
		t.Error("a real run did not report itself committed")
	}

	// Shelf membership survived, which needs both halves to have landed.
	var shelfBooks int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM shelf_books`).Scan(&shelfBooks); err != nil {
		t.Fatal(err)
	}
	if shelfBooks == 0 {
		t.Error("no shelf membership; the two halves did not see each other")
	}
}

// buildFixtureAppDB writes a miniature calibre-web app.db.
func buildFixtureAppDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// role 479 is calibre-web's admin bitmask; 32 is its anonymous Guest.
	if _, err := db.Exec(`
CREATE TABLE user (id INTEGER PRIMARY KEY, name VARCHAR, email VARCHAR, role SMALLINT,
  password VARCHAR, kindle_mail VARCHAR, locale VARCHAR, sidebar_view INTEGER,
  default_language VARCHAR, denied_tags VARCHAR, allowed_tags VARCHAR,
  denied_column_value VARCHAR, allowed_column_value VARCHAR, view_settings JSON,
  kobo_only_shelves_sync INTEGER);
CREATE TABLE shelf (id INTEGER PRIMARY KEY, uuid VARCHAR, name VARCHAR, is_public INTEGER,
  user_id INTEGER, kobo_sync BOOLEAN, created DATETIME, last_modified DATETIME);
CREATE TABLE book_shelf_link (id INTEGER PRIMARY KEY, book_id INTEGER, "order" INTEGER,
  shelf INTEGER, date_added DATETIME);
CREATE TABLE remote_auth_token (id INTEGER PRIMARY KEY, auth_token VARCHAR, user_id INTEGER,
  verified BOOLEAN, expiration DATETIME, token_type INTEGER);
CREATE TABLE book_read_link (id INTEGER PRIMARY KEY, book_id INTEGER, user_id INTEGER,
  read_status INTEGER NOT NULL, last_modified DATETIME, last_time_started_reading DATETIME,
  times_started_reading INTEGER NOT NULL);
CREATE TABLE kobo_reading_state (id INTEGER PRIMARY KEY, user_id INTEGER, book_id INTEGER,
  last_modified DATETIME, priority_timestamp DATETIME);
CREATE TABLE kobo_bookmark (id INTEGER PRIMARY KEY, kobo_reading_state_id INTEGER,
  last_modified DATETIME, location_source VARCHAR, location_type VARCHAR,
  location_value VARCHAR, progress_percent FLOAT, content_source_progress_percent FLOAT);
CREATE TABLE kobo_statistics (id INTEGER PRIMARY KEY, kobo_reading_state_id INTEGER,
  last_modified DATETIME, remaining_time_minutes INTEGER, spent_reading_minutes INTEGER);

INSERT INTO user (id,name,email,role,locale) VALUES
  (1,'klara','klara@example.com',479,'sv'),
  (2,'Guest','no@email',32,'en');
INSERT INTO shelf (id,uuid,name,is_public,user_id,kobo_sync,created,last_modified) VALUES
  (1,'5025a22e-0a24-4eef-94c5-2d3102a8fe8a','Kobo',0,1,1,
   '2025-01-01 00:00:00+00:00','2025-01-02 00:00:00+00:00');
INSERT INTO book_shelf_link (book_id,"order",shelf,date_added)
  VALUES (1,0,1,'2025-01-01 00:00:00+00:00');
INSERT INTO remote_auth_token (auth_token,user_id,verified,token_type)
  VALUES ('4cb0db941b0356b37b6d134baa81eac2',1,0,1);
`); err != nil {
		t.Fatal(err)
	}
	return path
}
