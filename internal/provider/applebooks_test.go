package provider

import (
	"testing"
	"time"
)

// Apple returns a 100px thumbnail; the size is a path segment, and rewriting it
// is what makes these usable as covers at all. Getting it wrong is invisible --
// a valid 100px image is still served, just far too small to be a cover.
func TestArtworkUpscale(t *testing.T) {
	cases := map[string]string{
		"https://is1-ssl.mzstatic.com/image/thumb/Pub/v4/ab/cd/x.jpg/100x100bb.jpg": "https://is1-ssl.mzstatic.com/image/thumb/Pub/v4/ab/cd/x.jpg/1400x1400bb.jpg",
		"https://is1-ssl.mzstatic.com/image/thumb/Pub/v4/ab/cd/x.png/60x60bb.png":   "https://is1-ssl.mzstatic.com/image/thumb/Pub/v4/ab/cd/x.png/1400x1400bb.png",
		// An address in another shape must be left exactly as it is rather than
		// mangled into one that 404s.
		"https://example.com/cover.jpg": "https://example.com/cover.jpg",
	}
	for in, want := range cases {
		if got := artworkSize.ReplaceAllString(in, "/1400x1400bb.$1"); got != want {
			t.Errorf("\n in   %s\n got  %s\n want %s", in, got, want)
		}
	}
}

// Apple's blurbs are HTML; every other provider returns plain text, and a
// description is shown as text, so the markup would be read out literally.
func TestCleanHTML(t *testing.T) {
	got := cleanHTML("<p>F&ouml;rsta stycket.<br/>Andra raden.</p><p>Tredje &amp; sista.</p>")
	want := "Första stycket.\nAndra raden.\n\nTredje & sista."
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if cleanHTML("") != "" {
		t.Error("empty in, empty out")
	}
	if got := cleanHTML("Redan ren text."); got != "Redan ren text." {
		t.Errorf("plain text should survive: %q", got)
	}
	// The letters this library is mostly written in.
	if got := cleanHTML("Sk&aring;nes &ouml;ar och &auml;lvar"); got != "Skånes öar och älvar" {
		t.Errorf("Swedish entities must decode, got %q", got)
	}
}

// The bucket is what keeps a bulk run from spending the interactive path's
// allowance. Refusing must be temporary, never sticky.
func TestBucket(t *testing.T) {
	b := newBucket(3, time.Hour)
	for i := 0; i < 3; i++ {
		if !b.take() {
			t.Fatalf("token %d should have been available", i+1)
		}
	}
	if b.take() {
		t.Error("a fourth token should be refused")
	}

	// Once the window passes, the allowance comes back.
	b.next = time.Now().Add(-time.Second)
	if !b.take() {
		t.Error("the bucket must refill once its window has passed")
	}
}

// A search with nothing to search on must not spend a token or call out.
func TestAppleIgnoresEmptyQuery(t *testing.T) {
	a := newAppleBooks("sv")
	res, err := a.Search(t.Context(), Query{ISBN: "9789100138813"}, 5)
	if err != nil || res != nil {
		t.Errorf("an ISBN-only query has no term to search: got %v, %v", res, err)
	}
	if a.limiter.tokens != 20 {
		t.Errorf("no token should have been spent, %d left of 20", a.limiter.tokens)
	}
}

func TestAppleStorefront(t *testing.T) {
	if got := newAppleBooks("sv").storefront; got != "se" {
		t.Errorf("a Swedish library should search the Swedish store, got %q", got)
	}
	if got := newAppleBooks("en").storefront; got != "us" {
		t.Errorf("got %q", got)
	}
}
