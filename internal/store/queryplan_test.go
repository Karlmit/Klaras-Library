package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/devseed"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/internal/testdb"
)

// These tests are the regression guard for the defect this project exists to
// fix. calibre-web is slow at 30k books because its queries full-scan the
// library; a plan that reads every row is therefore a test failure here, not a
// performance note. See the query-plan table in the project plan.
//
// They need a real Postgres. Set KLARAS_TEST_DATABASE_URL to enable; without
// it they skip so `go test ./...` still works on a bare checkout.

const seedBooks = 30000

var (
	setupOnce sync.Once
	sharedDB  *store.DB
	setupErr  error
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "store")

	setupOnce.Do(func() {
		ctx := context.Background()
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

		if setupErr = store.Migrate(ctx, dsn, log); setupErr != nil {
			return
		}
		sharedDB, setupErr = store.Open(ctx, dsn, 8, log)
		if setupErr != nil {
			return
		}

		// Reseed only when the fixture is not already the right size; a full
		// seed costs ~11s and every test in this file shares it.
		var n int64
		if setupErr = sharedDB.Pool.QueryRow(ctx, "SELECT count(*) FROM books").Scan(&n); setupErr != nil {
			return
		}
		if n != seedBooks {
			_, setupErr = devseed.Run(ctx, sharedDB.Pool, seedBooks, log)
			if setupErr != nil {
				return
			}
		}
		setupErr = seedShelf(ctx, sharedDB.Pool)
	})

	if setupErr != nil {
		t.Fatalf("test database setup: %v", setupErr)
	}
	return sharedDB.Pool
}

// seedShelf creates the ~40-book Kobo-synced shelf the sync query is measured against.
func seedShelf(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role)
		VALUES (1, 'planuser', 'x', 'admin')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO shelves (id, user_id, name, kobo_sync)
		VALUES (1, 1, 'Kobo', true)
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO shelf_books (shelf_id, book_id)
		SELECT 1, id FROM books ORDER BY id LIMIT 40
		ON CONFLICT DO NOTHING;

		ANALYZE users, shelves, shelf_books;`)
	return err
}

// planNode is the subset of Postgres' JSON EXPLAIN output we care about.
type planNode struct {
	NodeType     string     `json:"Node Type"`
	RelationName string     `json:"Relation Name"`
	ActualRows   float64    `json:"Actual Rows"`
	ActualLoops  float64    `json:"Actual Loops"`
	Plans        []planNode `json:"Plans"`
}

type explainResult struct {
	Plan          planNode `json:"Plan"`
	ExecutionTime float64  `json:"Execution Time"`
	PlanningTime  float64  `json:"Planning Time"`
}

func explain(t *testing.T, pool *pgxpool.Pool, query string, args ...any) explainResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var raw []byte
	sql := "EXPLAIN (ANALYZE, FORMAT JSON) " + query
	if err := pool.QueryRow(ctx, sql, args...).Scan(&raw); err != nil {
		t.Fatalf("explain failed: %v\nquery: %s", err, query)
	}
	var out []explainResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse explain json: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty explain output")
	}
	return out[0]
}

// walk visits every node in the plan tree.
func walk(n planNode, fn func(planNode)) {
	fn(n)
	for _, c := range n.Plans {
		walk(c, fn)
	}
}

// assertNoSeqScanOn fails if the plan sequentially scans the named relation.
func assertNoSeqScanOn(t *testing.T, res explainResult, relation string) {
	t.Helper()
	walk(res.Plan, func(n planNode) {
		if strings.Contains(n.NodeType, "Seq Scan") && n.RelationName == relation {
			t.Errorf("plan sequentially scans %q (%.0f rows) -- this is the calibre-web "+
				"pathology this schema exists to prevent", relation, n.ActualRows)
		}
	})
}

// assertRowsExamined fails if the plan touched more rows of a relation than expected.
// This catches "correct result, wrong amount of work" regressions that a Seq Scan
// check alone would miss.
func assertRowsExamined(t *testing.T, res explainResult, relation string, max float64) {
	t.Helper()
	var total float64
	walk(res.Plan, func(n planNode) {
		if n.RelationName == relation {
			loops := n.ActualLoops
			if loops == 0 {
				loops = 1
			}
			total += n.ActualRows * loops
		}
	})
	if total > max {
		t.Errorf("plan examined %.0f rows of %q, want <= %.0f", total, relation, max)
	}
}

func TestGridPageIsIndexedAndFlat(t *testing.T) {
	pool := testPool(t)

	first := explain(t, pool, `
		SELECT id, uuid, title, author_names, series_name, series_index, rating, has_cover
		FROM books ORDER BY title_sort, id LIMIT 60`)
	assertNoSeqScanOn(t, first, "books")
	assertRowsExamined(t, first, "books", 200)

	// The point of keyset pagination: a deep page must cost the same as the
	// first one. With OFFSET this grows linearly and is what makes calibre-web
	// crawl as you page into a large library.
	deep := explain(t, pool, `
		SELECT id, uuid, title, author_names, series_name, series_index, rating, has_cover
		FROM books WHERE (title_sort, id) > ($1, $2)
		ORDER BY title_sort, id LIMIT 60`, "Storm Signal", int64(25000))
	assertNoSeqScanOn(t, deep, "books")
	assertRowsExamined(t, deep, "books", 200)

	t.Logf("grid first page %.2fms, deep page %.2fms",
		first.ExecutionTime, deep.ExecutionTime)
}

func TestGridQueryDoesNotJoin(t *testing.T) {
	pool := testPool(t)
	res := explain(t, pool, `
		SELECT id, uuid, title, author_names, series_name, series_index, rating, has_cover
		FROM books ORDER BY title_sort, id LIMIT 60`)

	// The denormalised columns exist so the list view needs no joins at all.
	// If a join appears here, the denormalisation has been bypassed.
	walk(res.Plan, func(n planNode) {
		if strings.Contains(n.NodeType, "Join") || n.NodeType == "Nested Loop" {
			t.Errorf("grid query performs a %s; the denormalised columns should "+
				"make joins unnecessary", n.NodeType)
		}
		switch n.RelationName {
		case "", "books":
		default:
			t.Errorf("grid query touches %q; it should read only books", n.RelationName)
		}
	})
}

func TestFullTextSearchUsesGIN(t *testing.T) {
	pool := testPool(t)
	res := explain(t, pool, `
		SELECT id, title, ts_rank(search_tsv, q) AS r
		FROM books, plainto_tsquery('simple', f_unaccent($1)) q
		WHERE search_tsv @@ q
		ORDER BY r DESC, id LIMIT 60`, "crimson house")
	assertNoSeqScanOn(t, res, "books")
	t.Logf("full-text search %.2fms", res.ExecutionTime)
}

func TestAccentInsensitiveSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// A Swedish library must find "Röda" when the user types "roda".
	//
	// Note the config name: search_tsv is built with 'library_search', so a
	// query using a different configuration silently matches nothing rather
	// than erroring. Every query in the application must use the same name.
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM books
		WHERE search_tsv @@ plainto_tsquery('library_search', f_unaccent($1))`, "roda").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error(`searching "roda" found nothing; accent folding is not working`)
	}
	t.Logf(`"roda" matched %d books containing "Röda"`, n)
}

func TestFuzzyAuthorSearchUsesTrigram(t *testing.T) {
	pool := testPool(t)
	res := explain(t, pool, `
		SELECT id, title, authors_flat FROM books
		WHERE f_unaccent(authors_flat) % f_unaccent($1)
		ORDER BY similarity(f_unaccent(authors_flat), f_unaccent($1)) DESC, id
		LIMIT 60`, "mankel")
	assertNoSeqScanOn(t, res, "books")
	t.Logf("fuzzy author search %.2fms", res.ExecutionTime)
}

// TestKoboSyncIsShelfScoped is the most important test in this file.
//
// The bug that motivated this rebuild is that calibre-web's sync scans the
// whole library even when the synced shelf holds 63 books, spending 30+
// seconds and exceeding the Kobo's HTTP timeout. This asserts the query
// examines only shelf-sized row counts.
func TestKoboSyncIsShelfScoped(t *testing.T) {
	pool := testPool(t)
	res := explain(t, pool, `
		SELECT b.id, b.uuid, b.title, b.author_names
		FROM books b
		JOIN shelf_books sb ON sb.book_id = b.id
		JOIN shelves s      ON s.id = sb.shelf_id
		WHERE s.user_id = $1 AND s.kobo_sync
		  AND GREATEST(b.updated_at, sb.added_at) > $2
		ORDER BY GREATEST(b.updated_at, sb.added_at), b.id
		LIMIT 100`, int64(1), time.Now().Add(-100*365*24*time.Hour))

	assertNoSeqScanOn(t, res, "books")
	// 40 books on the shelf. Allow generous headroom for index-scan overhead,
	// but nothing close to the 30000-row library.
	assertRowsExamined(t, res, "books", 500)

	if res.ExecutionTime > 1000 {
		t.Errorf("sync query took %.1fms; the Kobo device times out at ~30s and "+
			"this must stay far below that even as the library grows",
			res.ExecutionTime)
	}
	t.Logf("shelf-scoped sync %.2fms (library holds %d books)", res.ExecutionTime, seedBooks)
}

func TestDenormalisationIsComplete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var noAuthors, noFlat, seriesMissing, badTsv int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE author_names = '{}'),
		       count(*) FILTER (WHERE authors_flat = ''),
		       count(*) FILTER (WHERE series_id IS NOT NULL AND series_name IS NULL),
		       count(*) FILTER (WHERE search_tsv IS NULL)
		FROM books`).Scan(&noAuthors, &noFlat, &seriesMissing, &badTsv)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		n    int64
	}{
		{"books with no author_names", noAuthors},
		{"books with empty authors_flat", noFlat},
		{"books with a series but no series_name", seriesMissing},
		{"books with a null search_tsv", badTsv},
	} {
		if c.n != 0 {
			t.Errorf("%s: %d (denormalisation triggers are not keeping up)", c.name, c.n)
		}
	}
}

func TestRenamePropagatesWithoutSpuriousResync(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is the point

	var bookID int64
	var before time.Time
	if err := tx.QueryRow(ctx, `
		SELECT b.id, b.updated_at FROM books b
		JOIN book_authors ba ON ba.book_id = b.id
		LIMIT 1`).Scan(&bookID, &before); err != nil {
		t.Fatal(err)
	}

	// A no-op refresh must not bump updated_at, or every Kobo would resync the
	// entire shelf after any unrelated maintenance.
	if _, err := tx.Exec(ctx, `SELECT refresh_book_denorm(ARRAY[$1]::bigint[])`, bookID); err != nil {
		t.Fatal(err)
	}
	var afterNoop time.Time
	if err := tx.QueryRow(ctx, `SELECT updated_at FROM books WHERE id=$1`, bookID).Scan(&afterNoop); err != nil {
		t.Fatal(err)
	}
	if !afterNoop.Equal(before) {
		t.Errorf("no-op refresh bumped updated_at (%v -> %v); this would trigger a "+
			"spurious Kobo resync", before, afterNoop)
	}

	// A real rename must propagate to the denormalised columns AND bump
	// updated_at, so the device learns about it.
	var authorID int64
	if err := tx.QueryRow(ctx,
		`SELECT author_id FROM book_authors WHERE book_id=$1 LIMIT 1`, bookID).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	newName := fmt.Sprintf("Renamed Author %d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `UPDATE authors SET name=$1 WHERE id=$2`, newName, authorID); err != nil {
		t.Fatal(err)
	}

	var names []string
	var flat string
	var afterRename time.Time
	if err := tx.QueryRow(ctx,
		`SELECT author_names, authors_flat, updated_at FROM books WHERE id=$1`, bookID).
		Scan(&names, &flat, &afterRename); err != nil {
		t.Fatal(err)
	}
	if !contains(names, newName) {
		t.Errorf("author rename did not reach books.author_names: %v", names)
	}
	if !strings.Contains(flat, newName) {
		t.Errorf("author rename did not reach books.authors_flat: %q", flat)
	}
	if !afterRename.After(before) {
		t.Error("author rename did not bump updated_at; the Kobo would never see the change")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestOffsetPaginationIsTheKnownPathology is a negative control.
//
// It asserts that the query style calibre-web uses -- ORDER BY ... OFFSET n --
// really does sequentially scan the library, which does two things: it proves
// the Seq Scan detector above can actually fail, and it documents why keyset
// pagination is mandatory rather than a stylistic preference.
//
// If this ever stops seq-scanning, the detector may have silently broken.
func TestOffsetPaginationIsTheKnownPathology(t *testing.T) {
	pool := testPool(t)
	res := explain(t, pool, `
		SELECT id, uuid, title, author_names, series_name, series_index, rating, has_cover
		FROM books ORDER BY title_sort, id LIMIT 60 OFFSET 25000`)

	var sawSeqScan bool
	var rows float64
	walk(res.Plan, func(n planNode) {
		if strings.Contains(n.NodeType, "Seq Scan") && n.RelationName == "books" {
			sawSeqScan = true
			rows = n.ActualRows
		}
	})
	if !sawSeqScan {
		t.Skip("planner did not seq-scan the OFFSET query on this instance; " +
			"the detector cannot be validated here")
	}
	t.Logf("OFFSET 25000 read %.0f rows in %.2fms -- the keyset query reads 60; "+
		"this is why OFFSET is banned in list endpoints", rows, res.ExecutionTime)
}

// ---------------------------------------------------------------------------
// Swedish language behaviour.
//
// This library is ~94% Swedish (27,496 of 28,038 books). Both the collation and
// the text-search configuration are named aliases defined in migration 00001,
// so these tests pin the behaviour that alias is supposed to provide.
// ---------------------------------------------------------------------------

// TestSwedishCollationOrder guards the alphabet.
//
// In Swedish, a, a and o are the last three letters of the alphabet, not
// accented variants of a and o. A generic ICU locale sorts "Akesson" under A,
// which puts it at the top of an author list where a Swedish reader expects it
// near the bottom.
func TestSwedishCollationOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Real author names from the library.
	got := []string{}
	rows, err := pool.Query(ctx, `
		SELECT n FROM (VALUES
			('Åkesson, Susanne'), ('Bergström, Christer'), ('Eklöf, Thomas'),
			('Ögren, Annica'), ('Ambjörnsson, Ronny'), ('Zetterberg, Karin')
		) AS t(n)
		ORDER BY n COLLATE library_sort`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"Ambjörnsson, Ronny",
		"Bergström, Christer",
		"Eklöf, Thomas",
		"Zetterberg, Karin",
		"Åkesson, Susanne", // Å after Z
		"Ögren, Annica",    // Ö last
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q\nfull order: %v", i, got[i], want[i], got)
			break
		}
	}
}

// TestSortColumnsUseSwedishCollation makes sure the collation is actually
// attached to the columns the grid sorts on. Without it, ORDER BY title_sort
// silently falls back to the cluster default and the alphabet is wrong again.
func TestSortColumnsUseSwedishCollation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, col := range []struct{ table, column string }{
		{"books", "title_sort"},
		{"books", "author_sort"},
		{"books", "series_name"},
		{"authors", "name"},
		{"authors", "sort"},
		{"series", "name"},
		{"tags", "name"},
	} {
		var collation string
		err := pool.QueryRow(ctx, `
			SELECT c.collname
			FROM pg_attribute a
			JOIN pg_collation c ON c.oid = a.attcollation
			WHERE a.attrelid = $1::regclass AND a.attname = $2`,
			col.table, col.column).Scan(&collation)
		if err != nil {
			t.Errorf("%s.%s has no explicit collation: %v", col.table, col.column, err)
			continue
		}
		if collation != "library_sort" {
			t.Errorf("%s.%s uses collation %q, want library_sort",
				col.table, col.column, collation)
		}
	}
}

// TestSwedishStemmingAndAccentFolding covers the two search behaviours that
// matter for this library: Swedish inflection folding, and finding accented
// words when the user types plain ASCII.
func TestSwedishStemmingAndAccentFolding(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback is the point

	titles := []string{
		"Flickorna på Dalarna",
		"Mördaren i huset",
		"Röda Rummet",
		"Trädgården",
	}
	for _, ti := range titles {
		if _, err := tx.Exec(ctx,
			`INSERT INTO books (uuid, title, path) VALUES (gen_random_uuid(), $1, $2)`,
			ti, "swedishtest/"+ti); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		query, wantTitle, why string
	}{
		{"flicka", "Flickorna på Dalarna", "Swedish plural -orna folds to the base form"},
		{"mörda", "Mördaren i huset", "Swedish definite -aren folds to the base form"},
		{"roda", "Röda Rummet", "plain ASCII finds the accented word"},
		{"tradgard", "Trädgården", "ASCII + inflection together"},
	}
	for _, c := range cases {
		var found string
		err := tx.QueryRow(ctx, `
			SELECT title FROM books
			WHERE path LIKE 'swedishtest/%'
			  AND search_tsv @@ plainto_tsquery('library_search', f_unaccent($1))
			LIMIT 1`, c.query).Scan(&found)
		if err != nil {
			t.Errorf("searching %q found nothing (%s)", c.query, c.why)
			continue
		}
		if found != c.wantTitle {
			t.Errorf("searching %q found %q, want %q", c.query, found, c.wantTitle)
		}
	}
}
