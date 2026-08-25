package calibre

import (
	"database/sql"
	"testing"
)

func TestParseCalibreTime(t *testing.T) {
	cases := []struct {
		in      string
		wantOK  bool
		wantISO string
		why     string
	}{
		// What modernc.org/sqlite actually hands back. Testing only the raw
		// on-disk form once hid a bug where every date in the library failed
		// to parse -- these driver-normalised cases must stay first.
		{"2024-11-29T11:10:48.441849Z", true, "2024-11-29T11:10:48Z", "driver-normalised, as really observed"},
		{"2024-12-01T21:12:34.301805Z", true, "2024-12-01T21:12:34Z", "driver-normalised"},
		{"0101-01-01T00:00:00Z", false, "", "driver-normalised no-date sentinel"},
		{"2024-11-29 11:10:48.441849+00:00", true, "2024-11-29T11:10:48Z", "raw on-disk form"},
		{"2020-08-15 00:00:00+00:00", true, "2020-08-15T00:00:00Z", "pubdate form"},
		{"0101-01-01 00:00:00+00:00", false, "", "Calibre's no-date sentinel"},
		{"2024-12-01T21:12:33.854301+00:00", true, "2024-12-01T21:12:33Z", "ISO T separator"},
		{"2024-12-01 21:12:33", true, "2024-12-01T21:12:33Z", "no zone"},
		{"2024-12-01", true, "2024-12-01T00:00:00Z", "date only"},
		{"", false, "", "empty"},
		{"garbage", false, "", "unparseable"},
	}
	for _, c := range cases {
		got, ok := parseCalibreTime(sql.NullString{String: c.in, Valid: true})
		if ok != c.wantOK {
			t.Errorf("%q (%s): ok=%v want %v", c.in, c.why, ok, c.wantOK)
			continue
		}
		if ok && got.Format("2006-01-02T15:04:05Z") != c.wantISO {
			t.Errorf("%q: got %s want %s", c.in, got.Format("2006-01-02T15:04:05Z"), c.wantISO)
		}
	}
	if _, ok := parseCalibreTime(sql.NullString{Valid: false}); ok {
		t.Error("NULL should not parse")
	}
}
