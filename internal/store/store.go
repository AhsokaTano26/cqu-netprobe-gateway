// Package store persists control-plane data (probes, targets, settings) in
// SQLite. It never stores measurement history: Protocol v1 leaves that to
// Prometheus.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Sentinel errors returned by probe and target lookups.
var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
	// ErrInUse is returned when deleting a catalog entry that probes still
	// reference. Deleting it silently would leave those probes pointing at a
	// building the dropdowns no longer offer.
	ErrInUse = errors.New("in use")
)

// Store is a handle to the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path, applies pragmas and
// runs pending migrations. It is safe to call on an existing database; both
// migrations and seeding are idempotent.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// SQLite serialises writers; a single connection avoids SQLITE_BUSY
	// entirely and this workload is tiny.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// modernc.org/sqlite surfaces constraint failures as text; matching on the
	// message avoids depending on its internal error types.
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
