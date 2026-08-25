package kobo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/auth"
	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
	"github.com/Karlmit/Klaras-Library/internal/kepub"
	"github.com/Karlmit/Klaras-Library/internal/kobo"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/internal/testdb"
)

// fixture is a running Kobo endpoint backed by a real database.
type fixture struct {
	srv   *httptest.Server
	pool  *pgxpool.Pool
	token string
	user  int64
	books []int64
	t     *testing.T
}

func newFixture(t *testing.T, shelfBooks int) *fixture {
	return newFixtureOpts(t, shelfBooks, false)
}

// newFixtureWithProxy enables store proxying. The test environment cannot reach
// storeapi.kobo.com, which is exactly the failure being exercised.
func newFixtureWithProxy(t *testing.T, shelfBooks int) *fixture {
	return newFixtureOpts(t, shelfBooks, true)
}

func newFixtureOpts(t *testing.T, shelfBooks int, proxyStore bool) *fixture {
	t.Helper()
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "kobo")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	if err := store.Migrate(ctx, dsn, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db, err := store.Open(ctx, dsn, 8, log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Pool.Exec(ctx, `
		TRUNCATE books, authors, series, publishers, tags, book_authors, book_tags,
		         identifiers, book_files, users, shelves, shelf_books,
		         kobo_auth_tokens, kobo_synced_books, kobo_archived,
		         reading_state, deleted_shelves, shelf_book_removals, jobs
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}

	dir := t.TempDir()
	libRoot := filepath.Join(dir, "library")
	cacheDir := filepath.Join(dir, "cache")

	// Books, each with a real file on disk so download paths work.
	f := &fixture{pool: db.Pool, user: 1, t: t}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, role)
		VALUES (1, 'device-owner', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO shelves (id, user_id, name, kobo_sync) VALUES (1, 1, 'Kobo', true)`); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= shelfBooks+5; i++ {
		bookDir := filepath.Join(libRoot, fmt.Sprintf("Author %d", i), fmt.Sprintf("Book %d", i))
		if err := os.MkdirAll(bookDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bookDir, "book.epub"), []byte("not a real epub"), 0o644); err != nil {
			t.Fatal(err)
		}
		var id int64
		rel := filepath.Join(fmt.Sprintf("Author %d", i), fmt.Sprintf("Book %d", i))
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO books (uuid, title, path, has_cover)
			VALUES (gen_random_uuid(), $1, $2, false) RETURNING id`,
			fmt.Sprintf("Bok nummer %d", i), rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO book_files (book_id, format, filename, size_bytes)
			VALUES ($1,'EPUB','book.epub',15)`, id); err != nil {
			t.Fatal(err)
		}
		f.books = append(f.books, id)
		// Only the first shelfBooks go on the synced shelf; the rest exist
		// purely to prove the sync query ignores them.
		if i <= shelfBooks {
			if _, err := db.Pool.Exec(ctx,
				`INSERT INTO shelf_books (shelf_id, book_id) VALUES (1,$1)`, id); err != nil {
				t.Fatal(err)
			}
		}
	}

	authSvc := auth.NewService(db.Pool)
	token, err := authSvc.NewKoboToken(ctx, 1, "test device")
	if err != nil {
		t.Fatal(err)
	}
	f.token = token

	r := chi.NewRouter()
	kobo.NewHandler(kobo.Deps{
		Pool: db.Pool, Auth: authSvc,
		Kepub:       kepub.New(libRoot, cacheDir),
		Covers:      covers.New(libRoot, cacheDir),
		Queue:       jobs.New(db.Pool, log),
		LibraryRoot: libRoot, ExternalURL: "https://library.example.com",
		SyncLimit: 10, ProxyStore: proxyStore,
		Limiter: auth.NewLimiter(8, time.Minute, time.Minute),
		Log:     log,
	}).Routes(r)

	f.srv = httptest.NewServer(r)
	t.Cleanup(f.srv.Close)
	return f
}

// sync performs one sync request and returns the entities plus the new token.
func (f *fixture) sync(token string) ([]map[string]json.RawMessage, string, bool) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		f.srv.URL+"/kobo/"+f.token+"/v1/library/sync", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(kobo.SyncTokenHeader, token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("sync returned %d", res.StatusCode)
	}
	var items []map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&items); err != nil {
		f.t.Fatalf("decode sync response: %v", err)
	}
	return items, res.Header.Get(kobo.SyncTokenHeader),
		res.Header.Get("x-kobo-sync") == "continue"
}

func countKinds(items []map[string]json.RawMessage) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		for k := range it {
			out[k]++
		}
	}
	return out
}

// TestSyncConverges is the property that matters most.
//
// A device that never reaches "nothing new" re-downloads its whole shelf on
// every poll. A sub-second rounding error in the sync token caused exactly
// that, so this walks the full conversation and insists it terminates.
func TestSyncConverges(t *testing.T) {
	f := newFixture(t, 8)

	items, token, cont := f.sync("")
	kinds := countKinds(items)
	if kinds["NewEntitlement"] != 8 {
		t.Errorf("first sync sent %d NewEntitlement, want 8: %v", kinds["NewEntitlement"], kinds)
	}
	if cont {
		t.Error("8 books should fit in one page")
	}

	for round := 2; round <= 5; round++ {
		items, token, cont = f.sync(token)
		if len(items) == 0 {
			return // converged
		}
		if cont {
			continue
		}
		t.Fatalf("round %d still returned %d entities (%v); the device would resync for ever",
			round, len(items), countKinds(items))
	}
	t.Fatal("sync never converged after 5 rounds")
}

// TestSyncIsShelfScoped is the defect this project exists to fix.
func TestSyncIsShelfScoped(t *testing.T) {
	f := newFixture(t, 6) // 6 on the shelf, 5 more in the library

	items, _, _ := f.sync("")
	got := countKinds(items)["NewEntitlement"]
	if got != 6 {
		t.Errorf("sync sent %d books; only the 6 on the Kobo shelf should be sent, "+
			"never the rest of the library", got)
	}
}

// TestSyncPaginates checks the continue protocol for shelves larger than a page.
func TestSyncPaginates(t *testing.T) {
	f := newFixture(t, 25) // SyncLimit is 10 in the fixture

	var (
		token string
		total int
		pages int
	)
	for pages < 10 {
		items, next, cont := f.sync(token)
		total += countKinds(items)["NewEntitlement"]
		token = next
		pages++
		if !cont {
			break
		}
		if len(items) == 0 {
			t.Fatal("continue was set but no items were returned")
		}
	}
	if total != 25 {
		t.Errorf("received %d books across %d pages, want 25", total, pages)
	}
	if pages < 3 {
		t.Errorf("25 books at 10 per page should take at least 3 requests, took %d", pages)
	}
}

// TestRemovingFromShelfReachesDevice covers the tombstone path.
func TestRemovingFromShelfReachesDevice(t *testing.T) {
	f := newFixture(t, 4)
	ctx := context.Background()

	_, token, _ := f.sync("")
	if items, tk, _ := f.sync(token); len(items) != 0 {
		token = tk
	}

	// Take one book off the shelf.
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM shelf_books WHERE shelf_id=1 AND book_id=$1`, f.books[0]); err != nil {
		t.Fatal(err)
	}

	items, _, _ := f.sync(token)
	var removed bool
	for _, it := range items {
		raw, ok := it["ChangedEntitlement"]
		if !ok {
			continue
		}
		var ce struct {
			BookEntitlement struct {
				IsRemoved bool `json:"IsRemoved"`
			} `json:"BookEntitlement"`
		}
		if err := json.Unmarshal(raw, &ce); err != nil {
			t.Fatal(err)
		}
		if ce.BookEntitlement.IsRemoved {
			removed = true
		}
	}
	if !removed {
		t.Error("removing a book from the shelf did not produce a removal entitlement; " +
			"the device would keep the book for ever")
	}
}

// TestReadingStateRoundTrip covers progress in both directions.
func TestReadingStateRoundTrip(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()

	var uuid string
	if err := f.pool.QueryRow(ctx, `SELECT uuid::text FROM books WHERE id=$1`, f.books[0]).
		Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	base := f.srv.URL + "/kobo/" + f.token + "/v1/library/" + uuid + "/state"

	body := `{"ReadingStates":[{"EntitlementId":"` + uuid + `",
		"CurrentBookmark":{"ProgressPercent":42,"ContentSourceProgressPercent":40,
		  "Location":{"Source":"kepub","Type":"KoboSpan","Value":"span-42"}},
		"Statistics":{"SpentReadingMinutes":95,"RemainingTimeMinutes":130},
		"StatusInfo":{"Status":"Reading"}}]}`
	req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT state returned %d", res.StatusCode)
	}

	res, err = http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var states []kobo.ReadingState
	if err := json.NewDecoder(res.Body).Decode(&states); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	s := states[0]
	if s.StatusInfo.Status != "Reading" {
		t.Errorf("status = %q, want Reading", s.StatusInfo.Status)
	}
	if s.CurrentBookmark.ProgressPercent != 42 {
		t.Errorf("progress = %d, want 42", s.CurrentBookmark.ProgressPercent)
	}
	if s.CurrentBookmark.Location.Value != "span-42" {
		t.Errorf("location = %q, want span-42", s.CurrentBookmark.Location.Value)
	}
	if s.Statistics.SpentReadingMinutes != 95 {
		t.Errorf("spent = %d, want 95", s.Statistics.SpentReadingMinutes)
	}

	// A later partial update must not wipe the stored bookmark.
	partial := `{"ReadingStates":[{"EntitlementId":"` + uuid + `","StatusInfo":{"Status":"Reading"}}]}`
	req, _ = http.NewRequest(http.MethodPut, base, strings.NewReader(partial))
	req.Header.Set("Content-Type", "application/json")
	if res, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	states = nil
	if err := json.NewDecoder(res.Body).Decode(&states); err != nil {
		t.Fatal(err)
	}
	if states[0].CurrentBookmark.ProgressPercent != 42 {
		t.Errorf("a partial update wiped the bookmark: progress is now %d, want 42",
			states[0].CurrentBookmark.ProgressPercent)
	}
}

// TestUnauthenticatedIsRejected checks the URL token actually gates access.
func TestUnauthenticatedIsRejected(t *testing.T) {
	f := newFixture(t, 1)
	res, err := http.Get(f.srv.URL + "/kobo/not-a-real-token/v1/library/sync")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token returned %d, want 401", res.StatusCode)
	}
}

// TestUnimplementedEndpointsAreNotErrors documents a firmware quirk: a 404 on
// an endpoint the device probes makes it retry and surface sync failures.
func TestUnimplementedEndpointsAreNotErrors(t *testing.T) {
	f := newFixture(t, 1)
	for _, p := range []string{"/v1/user/profile", "/v1/analytics/gettests", "/v1/products/foo"} {
		res, err := http.Get(f.srv.URL + "/kobo/" + f.token + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d; the device treats anything but 200 as a sync error",
				p, res.StatusCode)
		}
	}
}

// TestDownloadURLsAreAbsoluteHTTPS guards a deployment footgun: the device
// resolves these itself and silently fails on plain http.
func TestDownloadURLsAreAbsoluteHTTPS(t *testing.T) {
	f := newFixture(t, 2)
	items, _, _ := f.sync("")

	var checked int
	for _, it := range items {
		raw, ok := it["NewEntitlement"]
		if !ok {
			continue
		}
		var ne struct {
			BookMetadata struct {
				DownloadUrls []struct {
					Url    string `json:"Url"`
					Format string `json:"Format"`
				} `json:"DownloadUrls"`
			} `json:"BookMetadata"`
		}
		if err := json.Unmarshal(raw, &ne); err != nil {
			t.Fatal(err)
		}
		for _, u := range ne.BookMetadata.DownloadUrls {
			checked++
			if len(u.Url) < 8 || u.Url[:8] != "https://" {
				t.Errorf("download URL %q is not absolute https", u.Url)
			}
		}
	}
	if checked == 0 {
		t.Error("no download URLs were produced at all")
	}
}

// TestStoreProxyFailureDoesNotBreakLocalSync is the safety property behind
// KLARAS_KOBO_PROXY_STORE.
//
// With proxying on, every sync makes an outbound call to Kobo. Kobo being slow,
// down, or unreachable from the server must cost nothing but a short delay --
// the library is the part that has to work.
func TestStoreProxyFailureDoesNotBreakLocalSync(t *testing.T) {
	f := newFixtureWithProxy(t, 4)

	items, token, _ := f.sync("")
	if n := countKinds(items)["NewEntitlement"]; n != 4 {
		t.Errorf("got %d books with the store unreachable, want 4: %v",
			n, countKinds(items))
	}
	if token == "" {
		t.Error("no sync token issued when the store was unreachable")
	}

	// And it must still converge.
	items, _, _ = f.sync(token)
	if len(items) != 0 {
		t.Errorf("second sync returned %d entities; the store failure broke convergence",
			len(items))
	}
}

// TestHandlerAlwaysHasALimiter guards a footgun.
//
// An omitted limiter used to panic on the first request. Panicking on a missing
// security control is bad, but silently running without one is worse, so the
// constructor supplies a default.
func TestHandlerAlwaysHasALimiter(t *testing.T) {
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "kobo")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	db, err := store.Open(context.Background(), dsn, 2, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	r := chi.NewRouter()
	// Deliberately no Limiter.
	kobo.NewHandler(kobo.Deps{
		Pool: db.Pool, Auth: auth.NewService(db.Pool), Log: log,
	}).Routes(r)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/kobo/nope/v1/library/sync")
	if err != nil {
		t.Fatalf("request failed, which means the handler panicked: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 from a bad token", res.StatusCode)
	}
}

// TestStoreMergeIsBounded guards the size of a merged response.
//
// A device that has never synced through us sends no store token, so Kobo does
// a full account sync and can hand back thousands of entitlements at once.
// Appending them all produced a response far bigger than the device expects.
func TestStoreMergeIsBounded(t *testing.T) {
	const limit = 10

	// More store items than the budget allows.
	store := kobo.NewStoreResultForTest(make([]json.RawMessage, 500), "store-token-abc", false)

	tok := kobo.NewSyncToken()
	items := make([]any, 0, 3)
	for i := 0; i < 3; i++ {
		items = append(items, map[string]string{"local": "entity"})
	}

	h := http.Header{}
	out, cont := kobo.ApplyStoreResultForTest(store, items, tok, h, limit)

	if len(out) > limit {
		t.Errorf("merged response holds %d entities, want at most %d", len(out), limit)
	}
	if !cont {
		t.Error("truncating did not ask the device to come back for the rest")
	}
	// Holding items back must not advance the store's position, or the
	// deferred entitlements are lost.
	if tok.RawKoboStoreToken != "" {
		t.Errorf("store position advanced past a truncated batch: %q", tok.RawKoboStoreToken)
	}
}

// TestStoreMergeAdvancesWhenComplete is the other half of that contract.
func TestStoreMergeAdvancesWhenComplete(t *testing.T) {
	store := kobo.NewStoreResultForTest(make([]json.RawMessage, 2), "store-token-abc", false)
	tok := kobo.NewSyncToken()

	out, cont := kobo.ApplyStoreResultForTest(store, []any{}, tok, http.Header{}, 100)
	if len(out) != 2 {
		t.Errorf("delivered %d of 2 store entities", len(out))
	}
	if cont {
		t.Error("asked for another round despite delivering everything")
	}
	if tok.RawKoboStoreToken != "store-token-abc" {
		t.Errorf("store position not advanced after a complete batch: %q", tok.RawKoboStoreToken)
	}
}

// TestInitializationReturnsTheFullResourceTable is a regression guard with
// teeth: a device REPLACES its stored resource list with this response, so
// returning a subset blanks every endpoint left out. Klara's reader lost about
// 130 of them that way and stopped syncing, with nothing in the server logs.
func TestInitializationReturnsTheFullResourceTable(t *testing.T) {
	f := newFixture(t, 1)

	res, err := http.Get(f.srv.URL + "/kobo/" + f.token + "/v1/initialization")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var body struct {
		Resources map[string]any `json:"Resources"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if n := len(body.Resources); n < 100 {
		t.Errorf("returned %d resources; the device expects the full table of ~150 "+
			"and blanks anything missing", n)
	}
	// The endpoints that were empty on Klara's device.
	for _, k := range []string{
		"account_page", "autocomplete", "categories", "eula_page", "book",
		"book_detail_page", "library_sync", "user_profile", "tags",
		"device_auth", "products", "featured_lists",
	} {
		v, ok := body.Resources[k]
		if !ok {
			t.Errorf("resource %q is missing; the device would blank it", k)
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			t.Errorf("resource %q is empty", k)
		}
	}
	// ...and the three that must point back at us.
	for _, k := range []string{"image_host", "image_url_template", "image_url_quality_template"} {
		s, _ := body.Resources[k].(string)
		if !strings.Contains(s, "library.example.com") {
			t.Errorf("resource %q = %q, expected it to point at this server", k, s)
		}
	}
}
