package kobo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// TestStoreProbeShapes pins the three answers calibre-web gives specifically
// rather than generically when store proxying is off.
//
// The device calls all of these immediately before every sync. Upstream would
// not carry per-endpoint code for them if an empty object were accepted, and a
// device that cannot read one abandons the sync it was about to start -- which
// on the device reads as "sync failed", with the sync itself having returned
// 200.
func TestStoreProbeShapes(t *testing.T) {
	f := newFixture(t, 1)

	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/kobo/"+f.token+"/v1/analytics/gettests", nil)
	req.Header.Set("X-Kobo-userkey", "abc123")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tests map[string]any
	decodeJSON(t, res, &tests)
	if tests["Result"] != "Success" {
		t.Errorf("gettests Result = %v, want Success", tests["Result"])
	}
	if tests["TestKey"] != "abc123" {
		t.Errorf("gettests TestKey = %v, want the userkey echoed back", tests["TestKey"])
	}
	if _, ok := tests["Tests"]; !ok {
		t.Error("gettests has no Tests member")
	}

	res, err = http.Get(f.srv.URL + "/kobo/" + f.token + "/v1/user/loyalty/benefits")
	if err != nil {
		t.Fatal(err)
	}
	var benefits map[string]any
	decodeJSON(t, res, &benefits)
	if _, ok := benefits["Benefits"]; !ok {
		t.Errorf("benefits = %v, want a Benefits member", benefits)
	}
}

// TestInitializationCarriesAPIToken guards a header calibre-web always sends
// with the resource table.
func TestInitializationCarriesAPIToken(t *testing.T) {
	f := newFixture(t, 1)
	res, err := http.Get(f.srv.URL + "/kobo/" + f.token + "/v1/initialization")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("x-kobo-apitoken"); got != "e30=" {
		t.Errorf("x-kobo-apitoken = %q, want %q", got, "e30=")
	}
}

// TestSyncSendsContentLength guards against Go's chunked encoding.
//
// Go streams once a response passes its sniff buffer, so a sync large enough to
// matter went out chunked with no length while calibre-web (Flask) always sends
// one. The device is the only client here that cannot be inspected, so it gets
// the framing the server it is known to work with uses.
func TestSyncSendsContentLength(t *testing.T) {
	f := newFixture(t, 40)
	res, err := http.Get(f.srv.URL + "/kobo/" + f.token + "/v1/library/sync")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if len(body) < 2048 {
		t.Fatalf("fixture produced only %d bytes; too small to exercise chunking", len(body))
	}
	if res.ContentLength != int64(len(body)) {
		t.Errorf("Content-Length = %d, body = %d bytes (chunked responses report -1)",
			res.ContentLength, len(body))
	}
	if len(res.TransferEncoding) > 0 {
		t.Errorf("response used transfer encoding %v, want none", res.TransferEncoding)
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

// decodeJSON reads a response body into v and closes it.
func decodeJSON(t *testing.T, res *http.Response, v any) {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestUnacknowledgedSyncIsReannounced is the guard for a silent, permanent
// failure mode.
//
// A book is announced as new once, then as changed. ChangedEntitlement tells
// the device it already owns the book and only needs fresh metadata, so it
// downloads nothing. Marking books as sent the moment the response was written
// meant any sync the device never received -- a dropped connection, a proxy
// that swallowed it, a request made on the device's behalf while debugging --
// demoted those books forever. Sync then reported success and the collection
// appeared with no books in it.
//
// The device acknowledges by sending a watermark back. Until it does, books
// stay new.
func TestUnacknowledgedSyncIsReannounced(t *testing.T) {
	f := newFixture(t, 3)

	items, token, _ := f.sync("")
	if got := countKinds(items)["NewEntitlement"]; got != 3 {
		t.Fatalf("first sync sent %d NewEntitlement, want 3", got)
	}
	if token == "" {
		t.Fatal("first sync issued no sync token")
	}

	// The device never stored that token -- it asks again from the beginning.
	items, _, _ = f.sync("")
	if got := countKinds(items)["NewEntitlement"]; got != 3 {
		t.Errorf("unacknowledged books came back as %v, want 3 NewEntitlement; "+
			"a book announced into a response the device never received must "+
			"stay new or it can never download", countKinds(items))
	}

	// Now the device does come back with the token, which is proof it kept the
	// response. The books are past the watermark, so nothing is resent.
	items, _, _ = f.sync(token)
	if n := len(items); n != 0 {
		t.Errorf("acknowledged sync resent %d items, want 0: %v", n, countKinds(items))
	}

	// Asking again from the beginning now yields changes, not new books: the
	// device has confirmed it holds them.
	items, _, _ = f.sync("")
	kinds := countKinds(items)
	if kinds["ChangedEntitlement"] != 3 || kinds["NewEntitlement"] != 0 {
		t.Errorf("after acknowledgement got %v, want 3 ChangedEntitlement and 0 NewEntitlement", kinds)
	}
}

// TestWatermarkPastUndeliveredBooksStillAnnounces reproduces the state Klara's
// Kobo was left in, and is the reason a watermark alone cannot be trusted.
//
// Her device had read to the end of the stream and stored the token, so its
// watermark was past every book on the shelf. But those books had been
// announced as ChangedEntitlement -- "you already own this, here is fresh
// metadata" -- so it acknowledged them and downloaded nothing. Acknowledging a
// response is not the same as holding the books in it.
//
// Filtering on the watermark alone then makes the loss permanent: the device
// asks "anything since X?", the honest answer is "no", and sync reports success
// for ever while the collection stays empty. Anything the device has not
// confirmed holding is offered regardless of the watermark.
//
// The legacy rows here are exactly what migration 00013 leaves behind: present,
// unconfirmed, and with no record of the watermark they went out with.
func TestWatermarkPastUndeliveredBooksStillAnnounces(t *testing.T) {
	f := newFixture(t, 3)

	_, token, _ := f.sync("")
	if token == "" {
		t.Fatal("no sync token issued")
	}

	// Rewrite history into the pre-00013 shape: announced, never proven
	// delivered, no watermark recorded.
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE kobo_synced_books
		SET confirmed = false, announced_watermark = NULL
		WHERE user_id = $1`, f.user); err != nil {
		t.Fatal(err)
	}

	// The device asks from a watermark that is past all three books.
	items, token2, _ := f.sync(token)
	kinds := countKinds(items)
	if kinds["NewEntitlement"] != 3 {
		t.Fatalf("got %v, want 3 NewEntitlement: a book the device never "+
			"confirmed holding must be offered even when its watermark is "+
			"past it, or it can never arrive", kinds)
	}

	// That response did land, and the device says so by coming back with the
	// token from it. Only then does the shelf go quiet.
	items, _, _ = f.sync(token2)
	if n := len(items); n != 0 {
		t.Errorf("confirmed shelf resent %d items, want 0: %v", n, countKinds(items))
	}
}

// TestDownloadConfirmsDelivery pins the strong signal.
//
// Fetching the file is the only hard evidence that the device holds a book. A
// shelf must therefore converge on quiet even if its sync state was corrupted:
// unconfirmed books are announced as new, the device downloads them, and they
// stop being announced.
func TestDownloadConfirmsDelivery(t *testing.T) {
	f := newFixture(t, 2)

	items, _, _ := f.sync("")
	if got := countKinds(items)["NewEntitlement"]; got != 2 {
		t.Fatalf("first sync sent %d NewEntitlement, want 2", got)
	}

	// The device never acknowledges the sync, so the token cannot confirm
	// anything -- but it does fetch both files. Only the shelf's own books:
	// the fixture creates more, off the shelf, to catch scope errors.
	for _, id := range f.books[:2] {
		res, err := http.Get(fmt.Sprintf("%s/kobo/%s/download/%d/epub", f.srv.URL, f.token, id))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("download of %d returned %d, %d bytes", id, res.StatusCode, len(body))
		}
	}

	// Asking again from the beginning: it holds them, so they are changes now,
	// not new books.
	items, _, _ = f.sync("")
	kinds := countKinds(items)
	if kinds["NewEntitlement"] != 0 || kinds["ChangedEntitlement"] != 2 {
		t.Errorf("after downloading got %v, want 0 NewEntitlement and 2 ChangedEntitlement", kinds)
	}
}

// TestForeignWatermarkCannotConfirm guards the mistake that corrupted this
// library's sync state.
//
// A request made on the device's behalf -- a diagnostic curl, a replayed
// response -- announces books against a watermark the device never saw. If
// confirmation accepted any watermark at or past that one, the device's own
// next token would silently confirm books it had never been sent, and they
// could never be offered again.
func TestForeignWatermarkCannotConfirm(t *testing.T) {
	f := newFixture(t, 2)

	// Someone else pulls a full sync. The books are now announced, against a
	// watermark only that response carried.
	if got := countKinds(mustItems(f.sync("")))["NewEntitlement"]; got != 2 {
		t.Fatalf("setup sync sent %d NewEntitlement, want 2", got)
	}

	// The device arrives with a watermark of its own, well past the books.
	future := base64Token(t, time.Now().Add(24*time.Hour))
	items, _, _ := f.sync(future)
	if got := countKinds(items)["NewEntitlement"]; got != 2 {
		t.Errorf("got %v, want 2 NewEntitlement: a watermark the device did not "+
			"get from us must not confirm anything", countKinds(items))
	}
}

func mustItems(items []map[string]json.RawMessage, _ string, _ bool) []map[string]json.RawMessage {
	return items
}

// base64Token builds a sync token carrying an arbitrary watermark.
func base64Token(t *testing.T, at time.Time) string {
	t.Helper()
	tok := kobo.NewSyncToken()
	tok.BooksLastModified = at
	tok.BooksLastCreated = at
	tok.TagsLastModified = at
	tok.ReadingStateLastModified = at
	h := http.Header{}
	tok.WriteHeader(h)
	for k, v := range h {
		if strings.EqualFold(k, kobo.SyncTokenHeader) {
			return v[0]
		}
	}
	t.Fatal("no token written")
	return ""
}

// TestResyncIgnoresTheDeviceWatermark covers the "forget what this device has
// been told" path, which is calibre-web's force-full-sync mechanism.
//
// Clearing the record is not enough on its own: the device's watermark is still
// past every book, so a timestamp filter would offer none of them and the
// operator would see a resync that changed nothing. With nothing confirmed
// there is nothing to lose, so the token is disregarded and the stream starts
// over.
func TestResyncIgnoresTheDeviceWatermark(t *testing.T) {
	f := newFixture(t, 3)

	_, token, _ := f.sync("")
	// The device confirms, so the shelf is quiet.
	_, token2, _ := f.sync(token)
	if items, _, _ := f.sync(token2); len(items) != 0 {
		t.Fatalf("shelf not quiet before the resync: %v", countKinds(items))
	}

	// The operator runs kobo-resync.
	if _, err := f.pool.Exec(context.Background(),
		`DELETE FROM kobo_synced_books WHERE user_id=$1`, f.user); err != nil {
		t.Fatal(err)
	}

	// The device asks again with the very same token it has always used.
	items, _, _ := f.sync(token2)
	if got := countKinds(items)["NewEntitlement"]; got != 3 {
		t.Errorf("after a resync got %v, want 3 NewEntitlement: clearing the "+
			"record must actually resend the books", countKinds(items))
	}
}
