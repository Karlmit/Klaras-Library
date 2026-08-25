// Package kobo implements the sync protocol a Kobo eReader speaks.
//
// The wire format is not documented by Kobo; this follows calibre-web's
// implementation deliberately, field for field, so a device already paired with
// calibre-web keeps working after only a URL change -- no re-pairing and no
// full resync. See cps/kobo.py and cps/services/SyncToken.py upstream.
package kobo

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
)

// SyncTokenHeader carries the token in both directions.
const SyncTokenHeader = "x-kobo-synctoken"

// Token versions. calibre-web writes 1-1-0 and rejects anything below 1-0-0;
// matching that keeps tokens interchangeable between the two servers.
const (
	tokenVersion    = "1-1-0"
	tokenMinVersion = "1-0-0"
)

// SyncToken is the device's position in the sync stream.
//
// It is opaque to us in the sense that the device just echoes it back, but the
// field names must match calibre-web's exactly for a migrated device to resume
// rather than start over.
type SyncToken struct {
	RawKoboStoreToken        string    `json:"raw_kobo_store_token"`
	BooksLastModified        time.Time `json:"-"`
	BooksLastCreated         time.Time `json:"-"`
	ArchiveLastModified      time.Time `json:"-"`
	ReadingStateLastModified time.Time `json:"-"`
	TagsLastModified         time.Time `json:"-"`
}

// wire is the on-the-wire shape: epoch seconds, as calibre-web writes them.
type wire struct {
	Version string `json:"version"`
	Data    struct {
		RawKoboStoreToken        string  `json:"raw_kobo_store_token"`
		BooksLastModified        float64 `json:"books_last_modified"`
		BooksLastCreated         float64 `json:"books_last_created"`
		ArchiveLastModified      float64 `json:"archive_last_modified"`
		ReadingStateLastModified float64 `json:"reading_state_last_modified"`
		TagsLastModified         float64 `json:"tags_last_modified"`
	} `json:"data"`
}

// epoch is the zero value used for "never synced". Postgres timestamps compare
// fine against it and it round-trips through the token as 0.
var epoch = time.Unix(0, 0).UTC()

// NewSyncToken returns a token representing "this device has seen nothing".
func NewSyncToken() *SyncToken {
	return &SyncToken{
		BooksLastModified:        epoch,
		BooksLastCreated:         epoch,
		ArchiveLastModified:      epoch,
		ReadingStateLastModified: epoch,
		TagsLastModified:         epoch,
	}
}

// SyncTokenFromRequest reads the token, falling back to a fresh one whenever
// the header is absent, malformed or too old.
//
// A bad token must never be an error: the device would have no way to recover
// except a factory reset. Starting the stream over is always safe, just slower.
func SyncTokenFromRequest(r *http.Request) *SyncToken {
	raw := r.Header.Get(SyncTokenHeader)
	if raw == "" {
		return NewSyncToken()
	}
	// calibre-web strips base64 padding; restore it before decoding.
	if pad := len(raw) % 4; pad != 0 {
		raw += strings.Repeat("=", 4-pad)
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return NewSyncToken()
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return NewSyncToken()
	}
	if compareVersion(w.Version, tokenMinVersion) < 0 {
		return NewSyncToken()
	}
	return &SyncToken{
		RawKoboStoreToken:        w.Data.RawKoboStoreToken,
		BooksLastModified:        fromEpoch(w.Data.BooksLastModified),
		BooksLastCreated:         fromEpoch(w.Data.BooksLastCreated),
		ArchiveLastModified:      fromEpoch(w.Data.ArchiveLastModified),
		ReadingStateLastModified: fromEpoch(w.Data.ReadingStateLastModified),
		TagsLastModified:         fromEpoch(w.Data.TagsLastModified),
	}
}

// setKoboHeader writes a response header under a lowercase name.
//
// http.Header.Set would canonicalise "x-kobo-synctoken" to
// "X-Kobo-Synctoken". HTTP header names are case-insensitive and any correct
// client treats the two as identical, but calibre-web sends them lowercase and
// the Kobo firmware is the one client here that cannot be inspected or fixed.
// Writing into the map directly keeps the name exactly as Go will put it on the
// wire, so our responses match the server the device is known to work with.
// Nothing reads these back on the response side; use r.Header.Get for requests.
func setKoboHeader(h http.Header, name, value string) {
	delete(h, http.CanonicalHeaderKey(name))
	h[name] = []string{value}
}

// koboHeaderValue reads back a header set by setKoboHeader.
//
// http.Header.Get canonicalises its argument, so it cannot see a key written
// into the map verbatim. Anything reading one of these off a RESPONSE must go
// through here; request headers are canonicalised by net/http on parse and are
// safe with the ordinary Get.
func koboHeaderValue(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return h.Get(name)
}

// WriteHeader serialises the token onto a response.
func (t *SyncToken) WriteHeader(h http.Header) {
	var w wire
	w.Version = tokenVersion
	w.Data.RawKoboStoreToken = t.RawKoboStoreToken
	w.Data.BooksLastModified = toEpoch(t.BooksLastModified)
	w.Data.BooksLastCreated = toEpoch(t.BooksLastCreated)
	w.Data.ArchiveLastModified = toEpoch(t.ArchiveLastModified)
	w.Data.ReadingStateLastModified = toEpoch(t.ReadingStateLastModified)
	w.Data.TagsLastModified = toEpoch(t.TagsLastModified)

	b, err := json.Marshal(w)
	if err != nil {
		return
	}
	setKoboHeader(h, SyncTokenHeader, base64.StdEncoding.EncodeToString(b))
}

// toEpoch converts to fractional epoch seconds.
//
// The fraction is essential, not cosmetic. Postgres timestamps carry
// microseconds; truncating the watermark to a whole second means every row
// whose timestamp has a fractional part still satisfies "> watermark" on the
// next request, and the device re-syncs the same books and collections for
// ever. calibre-web writes fractional seconds here too (its to_epoch_timestamp
// uses total_seconds()), so this is also what keeps the token interchangeable.
func toEpoch(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

// fromEpoch converts fractional epoch seconds back to a timestamp, rounded to
// the nearest microsecond.
//
// The rounding is required for correctness, not tidiness. A float64 holds about
// 16 significant digits and epoch-microseconds already needs 16, so a value
// like 1784143248.031474 comes back as ...0314738 -- a hundred nanoseconds
// LOW. A watermark even slightly below a row's timestamp matches that row
// again, writes back the same watermark, and the device resyncs it on every
// request for ever. Postgres stores microsecond precision, so rounding to the
// nearest microsecond restores the exact original value.
func fromEpoch(f float64) time.Time {
	if f <= 0 {
		return epoch
	}
	micros := int64(math.Round(f * 1e6))
	return time.Unix(micros/1e6, (micros%1e6)*1000).UTC()
}

// compareVersion compares dash-separated versions like "1-1-0".
func compareVersion(a, b string) int {
	as, bs := strings.Split(a, "-"), strings.Split(b, "-")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = atoi(as[i])
		}
		if i < len(bs) {
			bv = atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
