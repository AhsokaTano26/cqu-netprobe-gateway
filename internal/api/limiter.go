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

// authFailureEntry is one address's failure counter plus the recency stamp the
// eviction policy needs. Without the stamp the map can only evict blindly.
type authFailureEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// Limiter implements the two layers described in the design doc §10: a token
// bucket per probe, plus a coarse per-IP throttle that applies only to failed
// authentication.
type Limiter struct {
	interval time.Duration
	burst    int

	mu       sync.Mutex
	buckets  map[string]*rate.Limiter
	failures map[string]*authFailureEntry
}

// NewLimiter builds a limiter allowing one request per interval with the given
// burst. Protocol v1 §21 recommends 1 request / 5s with burst 3.
func NewLimiter(interval time.Duration, burst int) *Limiter {
	return &Limiter{
		interval: interval,
		burst:    burst,
		buckets:  make(map[string]*rate.Limiter),
		failures: make(map[string]*authFailureEntry),
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

	e, ok := l.failures[ip]
	if !ok {
		if len(l.failures) >= maxTrackedAuthFailures {
			// Evict the least recently used entry, not an arbitrary one. Under a
			// flood of distinct source addresses a blind eviction can discard the
			// attacker's own counter and hand it a fresh burst, so the layer stops
			// limiting the very actor it exists for. §10.2 specifies a bounded LRU.
			//
			// The linear scan looks alarming, but it only runs on an insert into
			// an already-full map - i.e. only under attack - and walks at most
			// maxTrackedAuthFailures entries, which is microseconds.
			oldestKey, oldest := "", time.Time{}
			first := true
			for k, candidate := range l.failures {
				if first || candidate.lastUsed.Before(oldest) {
					oldestKey, oldest, first = k, candidate.lastUsed, false
				}
			}
			delete(l.failures, oldestKey)
		}
		e = &authFailureEntry{
			limiter: rate.NewLimiter(rate.Every(windowForAuthFailures/time.Duration(authFailureBurst)), authFailureBurst),
		}
		l.failures[ip] = e
	}
	// Recency is stamped here and in the peek below: an entry that is still
	// being used must not be the one evicted, or a block would silently lapse.
	e.lastUsed = time.Now()
	return e.limiter.Allow()
}

// AuthFailureBlocked reports whether ip has already exhausted its
// authentication-failure budget. It is a read-only peek: it creates no bucket
// and consumes no allowance, so calling it on every well-formed request does
// not itself charge the IP anything. §10.2 specifies that a blocked IP's
// requests return 401 fast, which is what this enables.
func (l *Limiter) AuthFailureBlocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.failures[ip]
	if !ok {
		return false
	}
	// The peek is read-only as far as the budget goes, but it still counts as
	// use for eviction: an address that keeps knocking must keep its entry, or
	// the eviction would reset the block it is serving.
	e.lastUsed = time.Now()
	return e.limiter.Tokens() < 1
}
