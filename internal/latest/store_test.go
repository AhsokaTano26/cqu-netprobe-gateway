package latest

import (
	"sync"
	"testing"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

func sampleResults() protocol.Results {
	return protocol.Results{
		"aliyun_dns": {
			protocol.ProbeICMP: {ICMP: &protocol.ICMPResult{Success: true, Sent: 5, Received: 5, LossRatio: 0}},
		},
	}
}

func TestPutAndGet(t *testing.T) {
	s := New()
	at := time.Unix(1789490000, 0).UTC()
	s.Put("hx-sy01-aaaaaa", sampleResults(), at)

	got, ok := s.Get("hx-sy01-aaaaaa")
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if !got.ServerReceivedAt.Equal(at) {
		t.Errorf("ServerReceivedAt = %v, want %v", got.ServerReceivedAt, at)
	}
	if got.Results["aliyun_dns"][protocol.ProbeICMP].ICMP == nil {
		t.Error("results were not stored")
	}
}

func TestGetMissing(t *testing.T) {
	s := New()
	if _, ok := s.Get("nope"); ok {
		t.Error("Get() on empty store returned ok = true")
	}
}

func TestPutReplaces(t *testing.T) {
	s := New()
	s.Put("p", sampleResults(), time.Unix(100, 0))
	s.Put("p", sampleResults(), time.Unix(200, 0))

	got, _ := s.Get("p")
	if got.ServerReceivedAt.Unix() != 200 {
		t.Errorf("ServerReceivedAt = %d, want 200", got.ServerReceivedAt.Unix())
	}
}

func TestDelete(t *testing.T) {
	s := New()
	s.Put("p", sampleResults(), time.Unix(100, 0))
	s.Delete("p")
	if _, ok := s.Get("p"); ok {
		t.Error("Get() after Delete returned ok = true")
	}
	// Deleting a missing key must not panic.
	s.Delete("p")
}

func TestSnapshotIsACopy(t *testing.T) {
	s := New()
	s.Put("p", sampleResults(), time.Unix(100, 0))

	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot() len = %d, want 1", len(snap))
	}
	// Mutating the snapshot must not affect the store.
	delete(snap, "p")
	if _, ok := s.Get("p"); !ok {
		t.Error("mutating the snapshot changed the store")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				s.Put("p", sampleResults(), time.Unix(int64(j), 0))
			}
		}()
	}
	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = s.Get("p")
				_ = s.Snapshot()
			}
		}()
	}
	// Deleter, exercising the cleanup path concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.Delete("gone")
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}
