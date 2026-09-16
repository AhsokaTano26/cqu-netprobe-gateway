package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Campus is a top-level location. Code is a Prometheus label value and a
// probe_id prefix, so it is immutable once created; Name is display-only.
type Campus struct {
	Code      string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Building belongs to exactly one campus and carries its group inline. Two
// buildings in the same group repeat the same group code and name.
type Building struct {
	Code              string
	CampusCode        string
	BuildingGroupCode string
	BuildingGroupName string
	Name              string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// BuildingGroup is a distinct group, used to populate the group picker so an
// administrator reuses an existing label value instead of retyping it.
type BuildingGroup struct {
	Code string
	Name string
}

const campusColumns = `code, name, created_at, updated_at`
const buildingColumns = `code, campus_code, building_group_code, building_group_name, name, created_at, updated_at`

func scanCampus(row interface{ Scan(...any) error }) (*Campus, error) {
	var c Campus
	var createdAt, updatedAt int64
	if err := row.Scan(&c.Code, &c.Name, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(createdAt, 0).UTC()
	c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &c, nil
}

func scanBuilding(row interface{ Scan(...any) error }) (*Building, error) {
	var b Building
	var createdAt, updatedAt int64
	if err := row.Scan(&b.Code, &b.CampusCode, &b.BuildingGroupCode, &b.BuildingGroupName,
		&b.Name, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	b.CreatedAt = time.Unix(createdAt, 0).UTC()
	b.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &b, nil
}

// ListCampuses returns every campus ordered by code.
func (s *Store) ListCampuses() ([]Campus, error) {
	rows, err := s.db.Query(`SELECT ` + campusColumns + ` FROM campuses ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("store: list campuses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Campus{}
	for rows.Next() {
		c, err := scanCampus(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan campus: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetCampus returns one campus by code.
func (s *Store) GetCampus(code string) (*Campus, error) {
	row := s.db.QueryRow(`SELECT `+campusColumns+` FROM campuses WHERE code = ?`, code)
	c, err := scanCampus(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get campus %s: %w", code, err)
	}
	return c, nil
}

// CreateCampus inserts a campus.
func (s *Store) CreateCampus(c *Campus) error {
	now := nowUnix()
	_, err := s.db.Exec(`INSERT INTO campuses (code, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		c.Code, c.Name, now, now)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: create campus: %w", err)
	}
	c.CreatedAt = time.Unix(now, 0).UTC()
	c.UpdatedAt = c.CreatedAt
	return nil
}

// UpdateCampusName changes only the display name. The code is deliberately not
// updatable: it is a Prometheus label value and a probe_id prefix, so changing
// it would orphan series history and desynchronise existing probe IDs.
func (s *Store) UpdateCampusName(code, name string) error {
	res, err := s.db.Exec(`UPDATE campuses SET name = ?, updated_at = ? WHERE code = ?`,
		name, nowUnix(), code)
	if err != nil {
		return fmt.Errorf("store: update campus: %w", err)
	}
	return requireAffected(res, code)
}

// DeleteCampus removes a campus, refusing while probes still reference it.
func (s *Store) DeleteCampus(code string) error {
	n, err := s.CampusProbeCount(code)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	if _, err := s.db.Exec(`DELETE FROM buildings WHERE campus_code = ?`, code); err != nil {
		return fmt.Errorf("store: delete campus buildings: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM campuses WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("store: delete campus: %w", err)
	}
	return requireAffected(res, code)
}

// ListBuildings returns every building ordered by campus then code.
func (s *Store) ListBuildings() ([]Building, error) {
	rows, err := s.db.Query(`SELECT ` + buildingColumns + ` FROM buildings ORDER BY campus_code, code`)
	if err != nil {
		return nil, fmt.Errorf("store: list buildings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Building{}
	for rows.Next() {
		b, err := scanBuilding(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan building: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListBuildingsByCampus returns one campus's buildings, ordered by code.
func (s *Store) ListBuildingsByCampus(campusCode string) ([]Building, error) {
	rows, err := s.db.Query(`SELECT `+buildingColumns+` FROM buildings WHERE campus_code = ? ORDER BY code`, campusCode)
	if err != nil {
		return nil, fmt.Errorf("store: list buildings for campus: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Building{}
	for rows.Next() {
		b, err := scanBuilding(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan building: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// GetBuilding returns one building by code.
func (s *Store) GetBuilding(code string) (*Building, error) {
	row := s.db.QueryRow(`SELECT `+buildingColumns+` FROM buildings WHERE code = ?`, code)
	b, err := scanBuilding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get building %s: %w", code, err)
	}
	return b, nil
}

// CreateBuilding inserts a building. The campus must already exist — the
// foreign key enforces it, and a failure here means the caller skipped the
// catalog check.
func (s *Store) CreateBuilding(b *Building) error {
	now := nowUnix()
	_, err := s.db.Exec(`INSERT INTO buildings (code, campus_code, building_group_code,
		building_group_name, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.Code, b.CampusCode, b.BuildingGroupCode, b.BuildingGroupName, b.Name, now, now)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: create building: %w", err)
	}
	b.CreatedAt = time.Unix(now, 0).UTC()
	b.UpdatedAt = b.CreatedAt
	return nil
}

// UpdateBuilding changes a building's display fields and group. As with
// campuses the code is not updatable.
func (s *Store) UpdateBuilding(b *Building) error {
	res, err := s.db.Exec(`UPDATE buildings SET building_group_code = ?, building_group_name = ?,
		name = ?, updated_at = ? WHERE code = ?`,
		b.BuildingGroupCode, b.BuildingGroupName, b.Name, nowUnix(), b.Code)
	if err != nil {
		return fmt.Errorf("store: update building: %w", err)
	}
	return requireAffected(res, b.Code)
}

// DeleteBuilding removes a building, refusing while probes still reference it.
func (s *Store) DeleteBuilding(code string) error {
	n, err := s.BuildingProbeCount(code)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.db.Exec(`DELETE FROM buildings WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("store: delete building: %w", err)
	}
	return requireAffected(res, code)
}

// CampusProbeCount reports how many probes use a campus.
func (s *Store) CampusProbeCount(code string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM probes WHERE campus_code = ?`, code).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count probes for campus: %w", err)
	}
	return n, nil
}

// BuildingProbeCount reports how many probes use a building.
func (s *Store) BuildingProbeCount(code string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM probes WHERE building_code = ?`, code).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count probes for building: %w", err)
	}
	return n, nil
}

// BuildingGroups returns the distinct groups currently in use, so the building
// form can offer them instead of inviting a retyped label value.
func (s *Store) BuildingGroups() ([]BuildingGroup, error) {
	rows, err := s.db.Query(`SELECT building_group_code, MAX(building_group_name)
		FROM buildings GROUP BY building_group_code ORDER BY building_group_code`)
	if err != nil {
		return nil, fmt.Errorf("store: list building groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []BuildingGroup{}
	for rows.Next() {
		var g BuildingGroup
		if err := rows.Scan(&g.Code, &g.Name); err != nil {
			return nil, fmt.Errorf("store: scan building group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
