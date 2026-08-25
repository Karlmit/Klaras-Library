package kobo

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSyncTokenPreservesSubSecond is a regression guard.
//
// Truncating the watermark to whole seconds made every entity with a
// fractional timestamp match "> watermark" again on the next request, so a
// device re-synced the same books and collections indefinitely.
func TestSyncTokenPreservesSubSecond(t *testing.T) {
	original := time.Date(2026, 7, 15, 19, 20, 48, 31474000, time.UTC)

	tok := NewSyncToken()
	tok.BooksLastModified = original
	tok.TagsLastModified = original

	rec := httptest.NewRecorder()
	tok.WriteHeader(rec.Header())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(SyncTokenHeader, koboHeaderValue(rec.Header(), SyncTokenHeader))
	back := SyncTokenFromRequest(req)

	// A microsecond of slack: the value travels through a float64.
	if d := back.BooksLastModified.Sub(original); d > time.Microsecond || d < -time.Microsecond {
		t.Errorf("books watermark drifted by %v (got %s, want %s)",
			d, back.BooksLastModified, original)
	}
	if back.BooksLastModified.Before(original) {
		t.Errorf("watermark went backwards: %s < %s -- the same rows will resync forever",
			back.BooksLastModified, original)
	}
}

func TestSyncTokenHandlesMissingAndJunk(t *testing.T) {
	// A device with no token, or a corrupted one, must get a usable fresh
	// token rather than an error: it has no way to recover from a rejection
	// short of a factory reset.
	for name, header := range map[string]string{
		"absent":      "",
		"not base64":  "!!!!not base64!!!!",
		"not json":    "aGVsbG8gd29ybGQ",
		"old version": "eyJ2ZXJzaW9uIjoiMC05LTAiLCJkYXRhIjp7fX0",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if header != "" {
				req.Header.Set(SyncTokenHeader, header)
			}
			tok := SyncTokenFromRequest(req)
			if tok == nil {
				t.Fatal("returned nil instead of a fresh token")
			}
			if !tok.BooksLastModified.Equal(epoch) {
				t.Errorf("expected a fresh token, got watermark %s", tok.BooksLastModified)
			}
		})
	}
}

func TestSyncTokenUnpaddedBase64(t *testing.T) {
	// calibre-web strips base64 padding; a device migrating across must not
	// have its position discarded.
	original := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tok := NewSyncToken()
	tok.BooksLastModified = original

	rec := httptest.NewRecorder()
	tok.WriteHeader(rec.Header())
	padded := rec.Header().Get(SyncTokenHeader)

	stripped := padded
	for len(stripped) > 0 && stripped[len(stripped)-1] == '=' {
		stripped = stripped[:len(stripped)-1]
	}
	if stripped == padded {
		t.Skip("token happened to need no padding")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(SyncTokenHeader, stripped)
	back := SyncTokenFromRequest(req)
	if !back.BooksLastModified.Equal(original) {
		t.Errorf("unpadded token lost its position: got %s, want %s",
			back.BooksLastModified, original)
	}
}

// TestKoboHeadersAreLowercase pins the wire casing.
//
// HTTP header names are case-insensitive, so this is not correctness in the
// spec sense -- it is compatibility with the one client that cannot be
// inspected. calibre-web sends these names lowercase and the device is known to
// work against it; Header.Set would silently canonicalise them and there would
// be no way to notice from the Go side.
func TestKoboHeadersAreLowercase(t *testing.T) {
	rec := httptest.NewRecorder()
	NewSyncToken().WriteHeader(rec.Header())
	setKoboHeader(rec.Header(), "x-kobo-sync", "continue")

	for _, name := range []string{SyncTokenHeader, "x-kobo-sync"} {
		if _, ok := rec.Header()[name]; !ok {
			t.Errorf("%q is not on the response under that exact name; got keys %v",
				name, keysOf(rec.Header()))
		}
		if _, ok := rec.Header()[http.CanonicalHeaderKey(name)]; ok {
			t.Errorf("%q was also written canonicalised, so the device sees it twice", name)
		}
		if got := koboHeaderValue(rec.Header(), name); got == "" {
			t.Errorf("koboHeaderValue(%q) came back empty", name)
		}
	}
}

func keysOf(h http.Header) []string {
	out := make([]string, 0, len(h))
	for k := range h {
		out = append(out, k)
	}
	return out
}
