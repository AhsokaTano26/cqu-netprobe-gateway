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
