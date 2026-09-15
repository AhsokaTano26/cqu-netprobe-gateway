package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxTrackedAuthFailures bounds the per-IP failure map so that a flood of
// forged source addresses cannot grow it without limit.
const maxTrackedAuthFailures = 10000

// authFailureBurst is how many failed authentications one address may make
// within windowForAuthFailures before it is throttled.
const (
	authFailureBurst      = 20
	windowForAuthFailures = time.Minute
)

// Limiter implements the two layers described in the design doc §10: a token
// bucket per probe, plus a coarse per-IP throttle that applies only to failed
// authentication.
type Limiter struct {
	interval time.Duration
	burst    int

	mu       sync.Mutex
	buckets  map[string]*rate.Limiter
	failures map[string]*rate.Limiter
}

// NewLimiter builds a limiter allowing one request per interval with the given
// burst. Protocol v1 §21 recommends 1 request / 5s with burst 3.
func NewLimiter(interval time.Duration, burst int) *Limiter {
	return &Limiter{
		interval: interval,
		burst:    burst,
		buckets:  make(map[string]*rate.Limiter),
		failures: make(map[string]*rate.Limiter),
	}
}

// Allow reports whether probeID may push now.
func (l *Limiter) Allow(probeID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[probeID]
	if !ok {
		b = rate.NewLimiter(rate.Every(l.interval), l.burst)
		l.buckets[probeID] = b
	}
	return b.Allow()
}

// Remove drops a probe's bucket, called when the probe is deleted or disabled
// so a later probe reusing the ID starts fresh.
func (l *Limiter) Remove(probeID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, probeID)
}

// AllowAuthFailure reports whether ip may attempt authentication again.
//
// This deliberately does NOT throttle successful pushes. Campus probes share
// NAT addresses, so an IP-based limit on valid traffic would penalise an entire
// campus for one noisy neighbour. It exists only to slow token guessing.
func (l *Limiter) AllowAuthFailure(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.failures[ip]
	if !ok {
		if len(l.failures) >= maxTrackedAuthFailures {
			// Evict an arbitrary entry rather than growing without bound. The
			// map is a defence-in-depth throttle, not an identity store, so
			// losing an entry only means one address gets a fresh allowance.
			for k := range l.failures {
				delete(l.failures, k)
				break
			}
		}
		b = rate.NewLimiter(rate.Every(windowForAuthFailures/time.Duration(authFailureBurst)), authFailureBurst)
		l.failures[ip] = b
	}
	return b.Allow()
}

// AuthFailureBlocked reports whether ip has already exhausted its
// authentication-failure budget. It is a read-only peek: it creates no bucket
// and consumes no allowance, so calling it on every well-formed request does
// not itself charge the IP anything. §10.2 specifies that a blocked IP's
// requests return 401 fast, which is what this enables.
func (l *Limiter) AuthFailureBlocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.failures[ip]
	return ok && b.Tokens() < 1
}
