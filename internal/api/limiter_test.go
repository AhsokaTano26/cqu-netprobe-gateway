package api

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterAllowsBurst(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("probe-a") {
			t.Fatalf("request %d within burst was denied", i+1)
		}
	}
	if l.Allow("probe-a") {
		t.Fatal("request beyond burst was allowed")
	}
}

func TestLimiterIsPerProbe(t *testing.T) {
	l := NewLimiter(5*time.Second, 1)
	if !l.Allow("probe-a") {
		t.Fatal("probe-a first request denied")
	}
	if !l.Allow("probe-b") {
		t.Fatal("probe-b was limited by probe-a's usage")
	}
}

func TestLimiterRefills(t *testing.T) {
	// 10ms per request so the test does not sleep for seconds.
	l := NewLimiter(10*time.Millisecond, 1)
	if !l.Allow("p") {
		t.Fatal("first request denied")
	}
	if l.Allow("p") {
		t.Fatal("second immediate request allowed")
	}
	time.Sleep(20 * time.Millisecond)
	if !l.Allow("p") {
		t.Fatal("request after refill denied")
	}
}

func TestLimiterRemoveResetsBucket(t *testing.T) {
	l := NewLimiter(5*time.Second, 1)
	if !l.Allow("p") {
		t.Fatal("first request denied")
	}
	if l.Allow("p") {
		t.Fatal("second request allowed")
	}
	l.Remove("p")
	if !l.Allow("p") {
		t.Fatal("request after Remove denied; deleting a probe must free its bucket")
	}
}

func TestAuthFailureLimiter(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)

	for i := 0; i < authFailureBurst; i++ {
		if !l.AllowAuthFailure("10.0.0.1") {
			t.Fatalf("failure %d within threshold was blocked", i+1)
		}
	}
	if l.AllowAuthFailure("10.0.0.1") {
		t.Fatal("failure beyond threshold was allowed")
	}
	// A different address must be unaffected.
	if !l.AllowAuthFailure("10.0.0.2") {
		t.Fatal("a different IP was blocked by 10.0.0.1's failures")
	}
}

func TestAuthFailureBlockedIsReadOnly(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)

	// An address with no recorded failures is not blocked, and peeking at it
	// must not create a bucket.
	if l.AuthFailureBlocked("10.0.0.9") {
		t.Fatal("an address with no recorded failures was reported blocked")
	}

	// Leave exactly one allowance in the budget.
	for i := 0; i < authFailureBurst-1; i++ {
		if !l.AllowAuthFailure("10.0.0.9") {
			t.Fatalf("failure %d within threshold was blocked", i+1)
		}
	}
	// The peek reports "not blocked" (one allowance remains) and consumes
	// nothing: the last allowance must still be spendable afterwards.
	for i := 0; i < 5; i++ {
		if l.AuthFailureBlocked("10.0.0.9") {
			t.Fatal("an address with an allowance left was reported blocked")
		}
	}
	if !l.AllowAuthFailure("10.0.0.9") {
		t.Fatal("the last allowance was consumed by AuthFailureBlocked; the peek must be read-only")
	}
	if !l.AuthFailureBlocked("10.0.0.9") {
		t.Fatal("an address past its budget was not reported blocked")
	}
	// The block is per-address.
	if l.AuthFailureBlocked("10.0.0.10") {
		t.Fatal("an unrelated address was reported blocked")
	}
}

func TestAuthFailureMapIsBounded(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)
	// Simulate a flood of forged source addresses.
	for i := 0; i < maxTrackedAuthFailures*2; i++ {
		l.AllowAuthFailure(ipForIndex(i))
	}
	l.mu.Lock()
	n := len(l.failures)
	l.mu.Unlock()
	if n > maxTrackedAuthFailures {
		t.Fatalf("failure map holds %d entries, want at most %d", n, maxTrackedAuthFailures)
	}
}

// ipForIndex produces distinct addresses without allocating a huge table.
func ipForIndex(i int) string {
	return fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
}

// fillAuthFailures fills the failure map to its cap.
func fillAuthFailures(l *Limiter) {
	for i := 0; i < maxTrackedAuthFailures; i++ {
		l.AllowAuthFailure(ipForIndex(i))
	}
}

// setRecency overwrites one entry's recency stamp. The real clock separates
// maxTrackedAuthFailures inserts by only a few milliseconds, which would make
// "which entry is oldest" a coin flip; this makes the ordering explicit.
func setRecency(l *Limiter, ip string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip].lastUsed = at
}

func tracked(l *Limiter, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.failures[ip]
	return ok
}

// TestAuthFailureEvictionIsLRU pins §10.2's "bounded LRU". The previous policy
// deleted an arbitrary entry, which under a flood of forged source addresses
// could discard the attacker's own counter and hand it a fresh burst, so the
// layer would stop limiting the actor it exists for. Here the address that is
// still failing is the oldest entry until its own call refreshes it, so an
// implementation that does not touch the stamp evicts it.
func TestAuthFailureEvictionIsLRU(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)
	fillAuthFailures(l)

	idle, active := ipForIndex(0), ipForIndex(1)
	setRecency(l, idle, time.Now().Add(-time.Hour))
	setRecency(l, active, time.Now().Add(-2*time.Hour))

	// The failing call must refresh the active address's recency.
	if !l.AllowAuthFailure(active) {
		t.Fatal("the still-failing address was throttled; its budget was full")
	}

	// One more address overflows the cap and forces exactly one eviction.
	l.AllowAuthFailure(ipForIndex(maxTrackedAuthFailures))

	if !tracked(l, active) {
		t.Error("the still-failing address was evicted; an active counter must survive eviction")
	}
	if tracked(l, idle) {
		t.Error("the idle address survived; the least recently used entry must be the one evicted")
	}
}

// TestAuthFailurePeekRefreshesRecency covers the other half of the LRU: the
// peek is read-only for the budget but must still count as use, or an address
// that keeps knocking would have its entry evicted and its block would lapse.
func TestAuthFailurePeekRefreshesRecency(t *testing.T) {
	l := NewLimiter(5*time.Second, 3)
	fillAuthFailures(l)

	idle, active := ipForIndex(0), ipForIndex(1)
	setRecency(l, idle, time.Now().Add(-time.Hour))
	setRecency(l, active, time.Now().Add(-2*time.Hour))

	l.AuthFailureBlocked(active)

	l.AllowAuthFailure(ipForIndex(maxTrackedAuthFailures))

	if !tracked(l, active) {
		t.Error("the evicted entry was the one being peeked at; the peek must refresh recency")
	}
	if tracked(l, idle) {
		t.Error("the least recently used entry survived eviction")
	}
}
