package portal

import (
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxTrackedRegisterIPs bounds the bucket map so a flood of forged source
// addresses cannot grow it without limit. Same reasoning as api.Limiter: this is
// a throttle, not an identity store.
const maxTrackedRegisterIPs = 10000

// ipEntry is one address's registration allowance plus a recency stamp.
type ipEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RegisterLimiter throttles public probe registrations per source address.
//
// It is deliberately separate from api.Limiter rather than a shared
// abstraction: the two have different policies, different key spaces and
// different lifetimes, and parameterising one type over both would cost more
// clarity than the ~30 lines it saves.
//
// The IP is used ONLY to throttle. Protocol §30 forbids treating a source
// address as a probe identity — campus egress shares NAT addresses — so this
// type never influences who a caller is, only how often they may ask.
type RegisterLimiter struct {
	interval time.Duration
	burst    int

	mu      sync.Mutex
	buckets map[string]*ipEntry
}

// NewRegisterLimiter allows one registration per interval per address, with
// burst registrations back to back.
func NewRegisterLimiter(interval time.Duration, burst int) *RegisterLimiter {
	return &RegisterLimiter{
		interval: interval,
		burst:    burst,
		buckets:  make(map[string]*ipEntry),
	}
}

// Allow reports whether ip may register now, consuming one registration.
func (l *RegisterLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) >= maxTrackedRegisterIPs {
			l.evictIdleLocked(now)
		}
		e = &ipEntry{limiter: rate.NewLimiter(rate.Every(l.interval), l.burst)}
		l.buckets[ip] = e
	}
	e.lastUsed = now
	return e.limiter.Allow()
}

// Remove drops an address's bucket. Exposed for tests and for any future
// administrative reset.
func (l *RegisterLimiter) Remove(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, ip)
}

// evictIdleLocked reclaims expired buckets, and if every bucket is still live it
// evicts the least recently used one. Evicting an ACTIVE entry would hand the
// address it belongs to a fresh burst — the same self-defeating behaviour the
// API limiter was fixed for — so idle entries are preferred.
//
// The O(n) scan runs only when the map is full, i.e. only under attack, and
// costs microseconds.
func (l *RegisterLimiter) evictIdleLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.buckets {
		// A full bucket has not been spent recently; that is the safest entry
		// to reclaim.
		if e.limiter.Tokens() >= float64(l.burst) {
			delete(l.buckets, k)
			return
		}
		if oldestKey == "" || e.lastUsed.Before(oldest) {
			oldestKey, oldest = k, e.lastUsed
		}
	}
	if oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}

// itoa is a package-local shorthand; the portal formats small integers in a few
// places and one helper keeps the import list shorter.
func itoa(n int) string { return strconv.Itoa(n) }
