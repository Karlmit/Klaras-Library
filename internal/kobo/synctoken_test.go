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
	req.Header.Set(SyncTokenHeader, rec.Header().Get(SyncTokenHeader))
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
