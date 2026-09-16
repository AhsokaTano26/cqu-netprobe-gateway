package portal

import (
	"testing"
	"time"
)

func TestRegisterLimiterAllowsBurstThenBlocks(t *testing.T) {
	l := NewRegisterLimiter(time.Hour, 3)
	for i := 1; i <= 3; i++ {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("registration %d within burst was blocked", i)
		}
	}
	if l.Allow("10.0.0.1") {
		t.Fatal("registration beyond burst was allowed")
	}
}

func TestRegisterLimiterIsPerIP(t *testing.T) {
	l := NewRegisterLimiter(time.Hour, 1)
	if !l.Allow("10.0.0.1") {
		t.Fatal("first registration denied")
	}
	if !l.Allow("10.0.0.2") {
		t.Fatal("a second address was limited by the first address's usage")
	}
}

func TestRegisterLimiterMapIsBounded(t *testing.T) {
	l := NewRegisterLimiter(time.Hour, 1)
	for i := 0; i < maxTrackedRegisterIPs*2; i++ {
		l.Allow(ipForIndex(i))
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > maxTrackedRegisterIPs {
		t.Fatalf("bucket map holds %d entries, want at most %d", n, maxTrackedRegisterIPs)
	}
}

func TestRegisterLimiterKeepsActiveEntriesWhenFull(t *testing.T) {
	// Fill the map with addresses that have spent their allowance, then add one
	// more. The eviction must reclaim an idle entry rather than an active one,
	// so an address that is still being throttled stays throttled.
	l := NewRegisterLimiter(time.Hour, 1)
	for i := 0; i < maxTrackedRegisterIPs; i++ {
		l.Allow(ipForIndex(i))
	}
	active := "203.0.113.7"
	if !l.Allow(active) { // spends its only token, now throttled
		t.Fatal("the active address's first registration was denied")
	}
	if l.Allow(active) {
		t.Fatal("the active address should now be out of allowance")
	}

	// Adding a fresh address forces one eviction.
	l.Allow("198.51.100.1")

	if l.Allow(active) {
		t.Fatal("an active throttled entry was evicted, resetting its allowance")
	}
}

func TestRegisterLimiterRemove(t *testing.T) {
	l := NewRegisterLimiter(time.Hour, 1)
	l.Allow("10.0.0.1")
	if l.Allow("10.0.0.1") {
		t.Fatal("second registration with no allowance was allowed")
	}
	l.Remove("10.0.0.1")
	if !l.Allow("10.0.0.1") {
		t.Fatal("Remove did not free the bucket")
	}
}

// ipForIndex produces distinct addresses without a huge table.
func ipForIndex(i int) string {
	return "10." + itoa(i>>16&0xff) + "." + itoa(i>>8&0xff) + "." + itoa(i&0xff)
}
