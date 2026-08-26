package httpapi

import (
	"net/http"
	"net/url"
	"testing"
)

// The allow-list is the whole security of the cover-fetch endpoint, so it gets
// a test rather than a comment. Each rejected case here is a request someone
// could actually make: the URL is user-supplied and the server opens it.
func TestCoverHostAllowed(t *testing.T) {
	allowed := []string{
		"books.google.com",
		"books.googleusercontent.com",
		"covers.openlibrary.org",
		"archive.org",
		"ia800304.us.archive.org", // where a download redirect lands
	}
	for _, h := range allowed {
		if !coverHostAllowed(h) {
			t.Errorf("%s should be allowed", h)
		}
	}

	refused := []string{
		"169.254.169.254",          // cloud metadata
		"127.0.0.1",                // loopback
		"192.168.1.66",             // the Unraid host
		"localhost",                //
		"evil.example.com",         //
		"notarchive.org.evil.com",  // suffix worn as a prefix
		"archive.org.evil.com",     // the same trick, more convincing
		"archive.org.uk",           // a real, different registrar
		"fakearchive.org",          // suffix without the dot
		"books.google.com.evil.io", // permitted host as a subdomain label
		"",                         //
	}
	for _, h := range refused {
		if coverHostAllowed(h) {
			t.Errorf("%s must be refused", h)
		}
	}
}

// The list has to hold on every hop. A permitted host answering 302 with an
// internal address is the case that makes a first-URL-only check useless.
func TestCoverFetchRedirectGuard(t *testing.T) {
	hop := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		return coverFetchClient.CheckRedirect(&http.Request{URL: u}, nil)
	}

	if err := hop("https://ia600100.us.archive.org/view_archive.php?a=1"); err != nil {
		t.Errorf("a real Archive delivery node should be followed: %v", err)
	}
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.66:8084/api/facets",
		"https://evil.example.com/x.jpg",
	} {
		if err := hop(raw); err == nil {
			t.Errorf("redirect to %s must be refused", raw)
		}
	}

	// A redirect loop must end even while every hop stays on an allowed host.
	u, _ := url.Parse("https://archive.org/x.jpg")
	via := make([]*http.Request, 8)
	if err := coverFetchClient.CheckRedirect(&http.Request{URL: u}, via); err == nil {
		t.Error("a redirect chain this long must be refused")
	}
}
