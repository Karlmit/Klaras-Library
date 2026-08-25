package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter throttles repeated authentication failures.
//
// This counts failures rather than requests, because that is the shape of the
// threat: a legitimate user gets their password wrong occasionally, an attacker
// gets it wrong thousands of times. A plain request-rate cap would either be
// too loose to stop a guessing run or tight enough to annoy a real person.
//
// Keys are tracked in two dimensions at once:
//
//   - per client IP, which stops one host hammering many accounts;
//   - per username, which stops a distributed run against a single account.
//
// Either tripping is enough to reject, so neither can be sidestepped by
// varying the other.
type Limiter struct {
	mu       sync.Mutex
	failures map[string]*bucket
	max      int
	window   time.Duration
	lockout  time.Duration
}

type bucket struct {
	count      int
	first      time.Time
	lockedTill time.Time
}

// NewLimiter builds a failure limiter.
//
// The defaults used by the server are 8 failures in 15 minutes, then a 15
// minute lockout: generous enough that a person fumbling a passphrase never
// notices, tight enough that online guessing is hopeless against an argon2id
// hash.
func NewLimiter(max int, window, lockout time.Duration) *Limiter {
	l := &Limiter{
		failures: make(map[string]*bucket),
		max:      max,
		window:   window,
		lockout:  lockout,
	}
	return l
}

// Allowed reports whether an attempt may proceed, and how long to wait if not.
func (l *Limiter) Allowed(keys ...string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	var worst time.Duration
	for _, k := range keys {
		b, ok := l.failures[k]
		if !ok {
			continue
		}
		if now.Before(b.lockedTill) {
			if d := time.Until(b.lockedTill); d > worst {
				worst = d
			}
		}
	}
	if worst > 0 {
		return false, worst
	}
	return true, 0
}

// Fail records a failed attempt against every key.
func (l *Limiter) Fail(keys ...string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, k := range keys {
		b, ok := l.failures[k]
		if !ok || now.Sub(b.first) > l.window {
			l.failures[k] = &bucket{count: 1, first: now}
			continue
		}
		b.count++
		if b.count >= l.max {
			b.lockedTill = now.Add(l.lockout)
			// Reset the counter so the lockout is not extended indefinitely by
			// attempts that arrive while it is already in force.
			b.count = 0
			b.first = now
		}
	}
}

// Succeed clears the counters for a successful attempt, so a person who
// mistyped twice and then got it right starts clean.
func (l *Limiter) Succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.failures, k)
	}
}

// Sweep drops entries that have expired. Called periodically so a long guessing
// run against random usernames cannot grow the map without bound.
func (l *Limiter) Sweep() int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	removed := 0
	for k, b := range l.failures {
		if now.After(b.lockedTill) && now.Sub(b.first) > l.window {
			delete(l.failures, k)
			removed++
		}
	}
	return removed
}

// RunSweeper prunes expired entries until the context is done.
func (l *Limiter) RunSweeper(stop <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			l.Sweep()
		}
	}
}

// ClientIP extracts the caller's address, honouring one layer of reverse proxy.
//
// X-Forwarded-For is only trusted for its LAST entry, which is the one the
// proxy itself appended; earlier entries are client-supplied and would let an
// attacker rotate the rate-limit key at will.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if ip := net.ParseIP(last); ip != nil {
			return ip.String()
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if ip := net.ParseIP(xr); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
