package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Setting reads a value from the settings table. The boolean reports whether
// the key exists at all, which callers need to distinguish "unset" from "empty".
func (s *Store) Setting(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read setting %s: %w", key, err)
	}
	return value, true, nil
}

// SetSetting writes a value, replacing any existing one.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowUnix())
	if err != nil {
		return fmt.Errorf("store: write setting %s: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a key. Deleting a missing key is not an error, because
// the documented password-reset procedure runs it unconditionally.
func (s *Store) DeleteSetting(key string) error {
	if _, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %s: %w", key, err)
	}
	return nil
}
