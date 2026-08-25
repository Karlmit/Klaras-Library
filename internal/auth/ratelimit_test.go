package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterLocksOutAfterRepeatedFailures(t *testing.T) {
	l := NewLimiter(3, time.Minute, time.Minute)

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allowed("ip:1.2.3.4"); !ok {
			t.Fatalf("locked out after only %d failures, limit is 3", i)
		}
		l.Fail("ip:1.2.3.4")
	}
	// The third failure trips it.
	if ok, _ := l.Allowed("ip:1.2.3.4"); !ok {
		t.Fatal("locked out one failure early")
	}
	l.Fail("ip:1.2.3.4")

	ok, wait := l.Allowed("ip:1.2.3.4")
	if ok {
		t.Error("not locked out after reaching the limit")
	}
	if wait <= 0 || wait > time.Minute {
		t.Errorf("Retry-After hint is %v, want something inside the lockout window", wait)
	}
	// A different address is unaffected.
	if ok, _ := l.Allowed("ip:5.6.7.8"); !ok {
		t.Error("one client's failures locked out an unrelated address")
	}
}

func TestLimiterIsClearedBySuccess(t *testing.T) {
	l := NewLimiter(3, time.Minute, time.Minute)
	l.Fail("ip:1.2.3.4")
	l.Fail("ip:1.2.3.4")
	l.Succeed("ip:1.2.3.4")

	// Someone who mistyped twice then got it right starts clean.
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allowed("ip:1.2.3.4"); !ok {
			t.Fatal("a successful login did not reset the counter")
		}
		l.Fail("ip:1.2.3.4")
	}
}

func TestLimiterLocksPerUsernameToo(t *testing.T) {
	// A distributed run against one account varies the IP, so the username
	// dimension is what catches it.
	l := NewLimiter(3, time.Minute, time.Minute)
	for i, ip := range []string{"ip:1.1.1.1", "ip:2.2.2.2", "ip:3.3.3.3"} {
		if ok, _ := l.Allowed(ip, "user:karl"); !ok {
			t.Fatalf("locked out after %d attempts from distinct addresses", i)
		}
		l.Fail(ip, "user:karl")
	}
	if ok, _ := l.Allowed("ip:4.4.4.4", "user:karl"); ok {
		t.Error("a distributed run against one username was not caught")
	}
	// ...but another account from a fresh address still works.
	if ok, _ := l.Allowed("ip:4.4.4.4", "user:klara"); !ok {
		t.Error("locking one username locked an unrelated one")
	}
}

func TestLimiterExpiresAndSweeps(t *testing.T) {
	l := NewLimiter(2, 20*time.Millisecond, 20*time.Millisecond)
	l.Fail("ip:1.2.3.4")
	l.Fail("ip:1.2.3.4")
	if ok, _ := l.Allowed("ip:1.2.3.4"); ok {
		t.Fatal("should be locked out")
	}
	time.Sleep(40 * time.Millisecond)
	if ok, _ := l.Allowed("ip:1.2.3.4"); !ok {
		t.Error("lockout did not expire")
	}
	if n := l.Sweep(); n == 0 {
		t.Error("sweep did not reclaim the expired entry")
	}
}

func TestClientIPTrustsOnlyTheLastForwardedHop(t *testing.T) {
	// Earlier X-Forwarded-For entries are supplied by the client, so trusting
	// them would let an attacker rotate the rate-limit key at will.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the proxy-appended hop 203.0.113.9", got)
	}

	// Spoofed junk falls back rather than becoming a key of its own.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:1234"
	r2.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(r2); got != "10.0.0.1" {
		t.Errorf("ClientIP = %q, want the socket address when the header is junk", got)
	}
}
