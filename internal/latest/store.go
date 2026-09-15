// Package latest keeps the most recent validated measurement per probe in
// memory. Protocol v1 allows this data to be lost on restart: the probe's next
// push restores it.
package latest

import (
	"sync"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// Entry is one probe's latest measurement and the server time it arrived.
// ServerReceivedAt is authoritative for online status; the probe's own
// timestamp is never used for that (Protocol v1 §23).
type Entry struct {
	Results          protocol.Results
	ServerReceivedAt time.Time
}

// Store is a concurrency-safe map of probe ID to latest entry.
//
// A plain RWMutex is deliberate. Pushes arrive roughly once per 10 seconds per
// probe and scrapes once per 15-30 seconds, so contention is not measurable;
// copy-on-write or sharded locks would add complexity for no observable gain.
type Store struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// New returns an empty store.
func New() *Store {
	return &Store{entries: make(map[string]Entry)}
}

// Put records the latest measurement for probeID.
func (s *Store) Put(probeID string, results protocol.Results, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[probeID] = Entry{Results: results, ServerReceivedAt: at}
}

// Get returns the latest entry for probeID.
func (s *Store) Get(probeID string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[probeID]
	return e, ok
}

// Snapshot returns a shallow copy of every entry. The map itself is copied so
// callers can iterate without holding the lock; the Entry values are immutable
// once stored.
func (s *Store) Snapshot() map[string]Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Entry, len(s.entries))
	for k, v := range s.entries {
		out[k] = v
	}
	return out
}

// Delete removes a probe's entry, called when the probe is deleted so a later
// probe reusing the ID cannot inherit stale data.
func (s *Store) Delete(probeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, probeID)
}
