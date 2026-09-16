package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Probe is a registered probe and its Gateway-assigned identity. Every field
// here is authoritative: the client never supplies any of it.
type Probe struct {
	ID                int64
	ProbeID           string
	TokenHash         string
	CampusCode        string
	CampusName        string
	BuildingGroupCode string
	BuildingGroupName string
	BuildingCode      string
	BuildingName      string
	NetworkType       string
	Enabled           bool
	Description       string
	// RotatedAt is zero until the token is first rotated. The admin list renders
	// zero as "从未轮换" rather than as a 1970 timestamp — the same rule that
	// keeps last_seen from emitting a bogus epoch value.
	RotatedAt time.Time
	// CreatedVia records whether the probe was created by an administrator or
	// through the public registration page ("admin" | "public"). It is what lets
	// the admin list distinguish "awaiting approval" from "deliberately disabled".
	CreatedVia string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func nowUnix() int64 { return time.Now().UTC().Unix() }

// unixOrZero converts a stored Unix second to a UTC time, mapping the zero
// value (and anything before it) to the zero time so callers can treat "never"
// distinctly from "1970".
func unixOrZero(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

const probeColumns = `id, probe_id, token_hash, campus_code, campus_name,
	building_group_code, building_group_name, building_code, building_name,
	network_type, enabled, description, created_at, updated_at, rotated_at, created_via`

func scanProbe(row interface{ Scan(...any) error }) (*Probe, error) {
	var p Probe
	var enabled int
	var createdAt, updatedAt int64
	var rotatedAt int64
	var createdVia string
	err := row.Scan(&p.ID, &p.ProbeID, &p.TokenHash, &p.CampusCode, &p.CampusName,
		&p.BuildingGroupCode, &p.BuildingGroupName, &p.BuildingCode, &p.BuildingName,
		&p.NetworkType, &enabled, &p.Description, &createdAt, &updatedAt,
		&rotatedAt, &createdVia)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	p.RotatedAt = unixOrZero(rotatedAt)
	p.CreatedVia = createdVia
	return &p, nil
}

// ProbeByTokenHash resolves a token hash to a probe. This is the authentication
// path, and it relies on the UNIQUE index on token_hash for an O(1) lookup.
func (s *Store) ProbeByTokenHash(hash string) (*Probe, error) {
	row := s.db.QueryRow(`SELECT `+probeColumns+` FROM probes WHERE token_hash = ?`, hash)
	p, err := scanProbe(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: lookup probe by token hash: %w", err)
	}
	return p, nil
}

// GetProbe returns a probe by its public ID.
func (s *Store) GetProbe(probeID string) (*Probe, error) {
	row := s.db.QueryRow(`SELECT `+probeColumns+` FROM probes WHERE probe_id = ?`, probeID)
	p, err := scanProbe(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get probe %s: %w", probeID, err)
	}
	return p, nil
}

// ListProbes returns every probe ordered by probe_id.
func (s *Store) ListProbes() ([]Probe, error) {
	rows, err := s.db.Query(`SELECT ` + probeColumns + ` FROM probes ORDER BY probe_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list probes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Non-nil so an empty table marshals as [] rather than null in the admin API.
	out := []Probe{}
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan probe: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate probes: %w", err)
	}
	return out, nil
}

// CreateProbe inserts a probe. The caller must set ProbeID and TokenHash.
func (s *Store) CreateProbe(p *Probe) error {
	now := nowUnix()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Unix(now, 0).UTC()
	}
	p.UpdatedAt = p.CreatedAt

	_, err := s.db.Exec(`INSERT INTO probes (
		probe_id, token_hash, campus_code, campus_name,
		building_group_code, building_group_name, building_code, building_name,
		network_type, enabled, description, created_at, updated_at, rotated_at, created_via
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		p.ProbeID, p.TokenHash, p.CampusCode, p.CampusName,
		p.BuildingGroupCode, p.BuildingGroupName, p.BuildingCode, p.BuildingName,
		p.NetworkType, boolToInt(p.Enabled), p.Description,
		p.CreatedAt.Unix(), p.UpdatedAt.Unix(), createdViaOrDefault(p.CreatedVia))
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: create probe: %w", err)
	}

	got, err := s.GetProbe(p.ProbeID)
	if err != nil {
		return err
	}
	p.ID = got.ID
	return nil
}

// SetProbeEnabled enables or disables a probe.
func (s *Store) SetProbeEnabled(probeID string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE probes SET enabled = ?, updated_at = ? WHERE probe_id = ?`,
		boolToInt(enabled), nowUnix(), probeID)
	if err != nil {
		return fmt.Errorf("store: set probe enabled: %w", err)
	}
	return requireAffected(res, probeID)
}

// UpdateProbeToken replaces a probe's token hash, revoking the previous token.
// The probe ID is untouched. Rotated_at is stamped here and only here, so it
// always answers "when was this probe's token last replaced".
func (s *Store) UpdateProbeToken(probeID, tokenHash string) error {
	res, err := s.db.Exec(`UPDATE probes SET token_hash = ?, updated_at = ?, rotated_at = ? WHERE probe_id = ?`,
		tokenHash, nowUnix(), nowUnix(), probeID)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: update probe token: %w", err)
	}
	return requireAffected(res, probeID)
}

// DeleteProbe permanently removes a probe.
func (s *Store) DeleteProbe(probeID string) error {
	res, err := s.db.Exec(`DELETE FROM probes WHERE probe_id = ?`, probeID)
	if err != nil {
		return fmt.Errorf("store: delete probe: %w", err)
	}
	return requireAffected(res, probeID)
}

func requireAffected(res sql.Result, probeID string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// createdViaOrDefault keeps the column's text domain closed: only "public" is
// special, everything else (including empty) means an administrator created it.
func createdViaOrDefault(v string) string {
	if v == "public" {
		return "public"
	}
	return "admin"
}
