package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// Target is a measurement destination. The Gateway never contacts a target
// itself; it only validates that probes report on allowed ones.
type Target struct {
	ID          int64
	TargetID    string
	DisplayName string
	Address     string
	Description string
	Enabled     bool
	ProbeTypes  []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ListTargets returns all targets ordered by target_id, each with its allowed
// probe types populated.
func (s *Store) ListTargets() ([]Target, error) {
	rows, err := s.db.Query(`SELECT id, target_id, display_name, address, description,
		enabled, created_at, updated_at FROM targets ORDER BY target_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list targets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Target{}
	for rows.Next() {
		var t Target
		var enabled int
		var createdAt, updatedAt int64
		if err := rows.Scan(&t.ID, &t.TargetID, &t.DisplayName, &t.Address, &t.Description,
			&enabled, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: scan target: %w", err)
		}
		t.Enabled = enabled != 0
		t.CreatedAt = time.Unix(createdAt, 0).UTC()
		t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate targets: %w", err)
	}

	for i := range out {
		types, err := s.probeTypesFor(out[i].TargetID)
		if err != nil {
			return nil, err
		}
		out[i].ProbeTypes = types
	}
	return out, nil
}

func (s *Store) probeTypesFor(targetID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT probe_type FROM target_probe_types WHERE target_id = ? ORDER BY probe_type`, targetID)
	if err != nil {
		return nil, fmt.Errorf("store: list probe types: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var pt string
		if err := rows.Scan(&pt); err != nil {
			return nil, fmt.Errorf("store: scan probe type: %w", err)
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

// CreateTarget inserts a target and its allowed probe types atomically.
func (s *Store) CreateTarget(t *Target) error {
	now := nowUnix()
	t.CreatedAt = time.Unix(now, 0).UTC()
	t.UpdatedAt = t.CreatedAt

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin create target: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`INSERT INTO targets (target_id, display_name, address, description,
		enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.TargetID, t.DisplayName, t.Address, t.Description,
		boolToInt(t.Enabled), now, now)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: create target: %w", err)
	}
	if err := insertProbeTypes(tx, t.TargetID, t.ProbeTypes); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateTarget replaces a target's metadata and probe type set.
func (s *Store) UpdateTarget(t *Target) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin update target: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`UPDATE targets SET display_name = ?, address = ?, description = ?,
		enabled = ?, updated_at = ? WHERE target_id = ?`,
		t.DisplayName, t.Address, t.Description, boolToInt(t.Enabled), nowUnix(), t.TargetID)
	if err != nil {
		return fmt.Errorf("store: update target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	if _, err := tx.Exec(`DELETE FROM target_probe_types WHERE target_id = ?`, t.TargetID); err != nil {
		return fmt.Errorf("store: clear probe types: %w", err)
	}
	if err := insertProbeTypes(tx, t.TargetID, t.ProbeTypes); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteTarget removes a target; its probe types cascade.
func (s *Store) DeleteTarget(targetID string) error {
	res, err := s.db.Exec(`DELETE FROM targets WHERE target_id = ?`, targetID)
	if err != nil {
		return fmt.Errorf("store: delete target: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func insertProbeTypes(tx *sql.Tx, targetID string, types []string) error {
	seen := map[string]bool{}
	for _, pt := range types {
		if seen[pt] {
			continue
		}
		seen[pt] = true
		if _, err := tx.Exec(`INSERT INTO target_probe_types (target_id, probe_type) VALUES (?, ?)`,
			targetID, pt); err != nil {
			return fmt.Errorf("store: insert probe type %s/%s: %w", targetID, pt, err)
		}
	}
	return nil
}

// Allowlist builds the target × probe type map that protocol validation needs.
// Disabled targets are omitted entirely, so a disabled target behaves exactly
// like an unknown one: 400 invalid_target.
func (s *Store) Allowlist() (protocol.Allowlist, error) {
	rows, err := s.db.Query(`
		SELECT t.target_id, p.probe_type
		FROM targets t
		JOIN target_probe_types p ON p.target_id = t.target_id
		WHERE t.enabled = 1
		ORDER BY t.target_id, p.probe_type`)
	if err != nil {
		return nil, fmt.Errorf("store: build allowlist: %w", err)
	}
	defer func() { _ = rows.Close() }()

	al := protocol.Allowlist{}
	for rows.Next() {
		var targetID, probeType string
		if err := rows.Scan(&targetID, &probeType); err != nil {
			return nil, fmt.Errorf("store: scan allowlist row: %w", err)
		}
		if al[targetID] == nil {
			al[targetID] = map[protocol.ProbeType]bool{}
		}
		al[targetID][protocol.ProbeType(probeType)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate allowlist: %w", err)
	}
	return al, nil
}

// ProbeTypesSorted returns a sorted copy of types, used by the admin templates.
func ProbeTypesSorted(types []string) []string {
	out := append([]string(nil), types...)
	sort.Strings(out)
	return out
}
