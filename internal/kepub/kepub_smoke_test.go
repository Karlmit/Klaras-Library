package kepub

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestConvertRealEpub runs a genuine EPUB through the converter when one is
// available. Skips otherwise, so CI without the library still passes.
func TestConvertRealEpub(t *testing.T) {
	src := os.Getenv("KLARAS_TEST_EPUB")
	if src == "" {
		t.Skip("KLARAS_TEST_EPUB not set")
	}
	dir := t.TempDir()
	s := New("/", dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	out, err := s.Convert(ctx, "11111111-1111-1111-1111-111111111111", src)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := os.Stat(src)
	t.Logf("converted %d kB -> %d kB in %s", in.Size()/1024, st.Size()/1024,
		time.Since(start).Round(time.Millisecond))

	if st.Size() == 0 {
		t.Fatal("produced an empty file")
	}

	// A second call must hit the cache rather than convert again.
	t2 := time.Now()
	out2, err := s.Convert(ctx, "11111111-1111-1111-1111-111111111111", src)
	if err != nil {
		t.Fatal(err)
	}
	if out2 != out {
		t.Errorf("cache returned a different path: %q vs %q", out2, out)
	}
	if d := time.Since(t2); d > 300*time.Millisecond {
		t.Errorf("cached lookup took %s; it should be a stat, not a re-conversion", d)
	}

	if _, ok := s.Cached("11111111-1111-1111-1111-111111111111", src); !ok {
		t.Error("Cached() did not find the file Convert() just wrote")
	}
}
