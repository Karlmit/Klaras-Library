package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"testing"
)

// The cover endpoint opens a URL a person typed, so what it may reach is the
// whole security of it. The rule is about the address, not the name: a
// hostname list cannot express "not inside this network", and cannot survive
// the name resolving to something different a moment later.
func TestPublicAddr(t *testing.T) {
	refused := map[string]string{
		"127.0.0.1":        "loopback",
		"::1":              "loopback v6",
		"10.0.0.5":         "RFC1918",
		"172.17.0.1":       "the Docker bridge",
		"192.168.1.66":     "the Unraid host",
		"169.254.169.254":  "cloud metadata",
		"0.0.0.0":          "unspecified",
		"100.64.0.1":       "carrier-grade NAT",
		"198.18.0.1":       "benchmarking range",
		"224.0.0.1":        "multicast",
		"255.255.255.255":  "broadcast",
		"fd00::1":          "unique-local v6",
		"fe80::1":          "link-local v6",
		"::ffff:127.0.0.1": "loopback wearing an IPv6 mapping",
		"::ffff:10.0.0.1":  "RFC1918 wearing an IPv6 mapping",
	}
	for addr, why := range refused {
		if publicAddr(net.ParseIP(addr)) {
			t.Errorf("%s (%s) must be refused", addr, why)
		}
	}
	if publicAddr(nil) {
		t.Error("an unparseable address must be refused")
	}

	for _, addr := range []string{"1.1.1.1", "142.250.74.46", "207.241.224.2", "2606:4700::1111"} {
		if !publicAddr(net.ParseIP(addr)) {
			t.Errorf("%s is a public address and should be allowed", addr)
		}
	}
}

// The dialer is where the rule is enforced, so exercise it as the http client
// will: with a host:port string, after resolution.
func TestSafeDialerControl(t *testing.T) {
	for _, addr := range []string{"169.254.169.254:80", "192.168.1.66:8084", "127.0.0.1:5432"} {
		if err := safeDialer.Control("tcp4", addr, nil); err == nil {
			t.Errorf("dialling %s must be refused", addr)
		}
	}
	if err := safeDialer.Control("tcp4", "142.250.74.46:443", nil); err != nil {
		t.Errorf("a public address should dial: %v", err)
	}
	if err := safeDialer.Control("tcp4", "not-an-address", nil); err == nil {
		t.Error("a malformed address must be refused")
	}
}

func TestCoverFetchRedirectGuard(t *testing.T) {
	hop := func(raw string, hops int) error {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		return coverFetchClient.CheckRedirect(&http.Request{URL: u}, make([]*http.Request, hops))
	}

	if err := hop("https://ia600100.us.archive.org/view_archive.php?a=1", 2); err != nil {
		t.Errorf("an ordinary https redirect should be followed: %v", err)
	}
	// Where a hop goes is the dialer's business; that it stays on the web is
	// this guard's.
	if err := hop("file:///etc/passwd", 1); err == nil {
		t.Error("a redirect to file:// must be refused")
	}
	if err := hop("https://archive.org/x.jpg", 8); err == nil {
		t.Error("a redirect chain this long must be refused")
	}
}
