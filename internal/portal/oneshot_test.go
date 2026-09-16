package portal

import (
	"sync"
	"testing"
	"time"
)

// These tests moved here with the store: the portal owns the one and only
// one-shot slot store, and the admin UI mints into it through
// (*Server).MintTokenSlot rather than keeping a copy that the public page could
// never redeem.

func TestOneShotTokenTakeOnce(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	o := newOneShotStore(60*time.Second, func() time.Time { return now })

	slot, err := o.putPair("hx-sy01-aaaaaa", "cqu_probe_secret")
	if err != nil {
		t.Fatalf("putPair() error = %v", err)
	}
	if len(slot) < 32 {
		t.Errorf("slot id is only %d chars", len(slot))
	}

	probeID, got, ok := o.takePair(slot)
	if !ok || got != "cqu_probe_secret" {
		t.Fatalf("takePair() = %q, %q, %v; want the token, true", probeID, got, ok)
	}
	if probeID != "hx-sy01-aaaaaa" {
		t.Errorf("takePair() probeID = %q, want the ID the slot was minted with", probeID)
	}
	// A second take must fail: this is what makes the token show-once.
	if _, _, ok := o.takePair(slot); ok {
		t.Fatal("takePair() succeeded twice; the token must be shown only once")
	}
}

// TestOneShotTokenTakeIsAtomic is TestOneShotTokenTakeOnce's concurrent
// counterpart. TestOneShotTokenTakeOnce only proves the sequential case, so a
// take implemented as read -> unlock -> delete would pass it while handing the
// plaintext token to every racing caller. Exactly one goroutine may win.
func TestOneShotTokenTakeIsAtomic(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	o := newOneShotStore(60*time.Second, func() time.Time { return now })

	slot, err := o.putPair("hx-sy01-aaaaaa", "cqu_probe_secret")
	if err != nil {
		t.Fatalf("putPair() error = %v", err)
	}

	const goroutines = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize overlap
			if _, v, ok := o.takePair(slot); ok && v == "cqu_probe_secret" {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("takePair() winners = %d, want exactly 1", winners)
	}
}

func TestOneShotTokenExpires(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	current := now
	o := newOneShotStore(60*time.Second, func() time.Time { return current })

	slot, _ := o.putPair("hx-sy01-aaaaaa", "cqu_probe_secret")
	current = now.Add(61 * time.Second)
	if _, _, ok := o.takePair(slot); ok {
		t.Fatal("an expired slot was still redeemable")
	}
}
