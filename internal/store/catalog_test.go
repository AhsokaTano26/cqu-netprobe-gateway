package store

import (
	"database/sql"
	"errors"
	"testing"
)

// TestLegacyDatabaseUpgradesWithZeroRotatedAt pins the upgrade path the brief's
// other tests cannot reach: a database that stopped at migration 001 and already
// holds probe rows. ALTER TABLE supplies DEFAULT 0 for those existing rows, and
// unixOrZero must turn that 0 into the zero time rather than 1970, or the admin
// list would render a bogus epoch instead of "从未轮换".
func TestLegacyDatabaseUpgradesWithZeroRotatedAt(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	// Apply only migration 001, the schema a pre-catalog deployment would have.
	for _, m := range migrations {
		if m.version != 1 {
			continue
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`,
		nowUnix()); err != nil {
		t.Fatalf("record migration 001: %v", err)
	}
	now := nowUnix()
	if _, err := raw.Exec(`INSERT INTO probes (probe_id, token_hash, campus_code, campus_name,
		building_group_code, building_group_name, building_code, building_name,
		network_type, enabled, description, created_at, updated_at)
		VALUES ('hx-sy01-aaaaaa', 'hash-a', 'hx', '虎溪', 'sy', '松园', 'sy01', '松园一栋',
		        'wired', 1, '', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert pre-upgrade probe: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Opening applies migration 002 on top of the populated table.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() legacy database error = %v", err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if !got.RotatedAt.IsZero() {
		t.Errorf("RotatedAt = %v (%d), want the zero time for a row predating the column",
			got.RotatedAt, got.RotatedAt.Unix())
	}
	if got.CreatedVia != "admin" {
		t.Errorf("CreatedVia = %q, want admin from the column default", got.CreatedVia)
	}
	if got.TokenHash != "hash-a" {
		t.Errorf("TokenHash = %q; the upgrade must not disturb existing rows", got.TokenHash)
	}
}

func TestBackfillSeedsCatalogFromExistingProbes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/backfill.db"

	// A pre-upgrade deployment: probes exist, the catalog does not.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := s1.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s1.CreateProbe(sampleProbe("hx-sy02-bbbbbb", "hash-b")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if campuses, err := s1.ListCampuses(); err != nil || len(campuses) != 0 {
		t.Fatalf("ListCampuses() before backfill = %v, %v; want empty", campuses, err)
	}
	_ = s1.Close()

	// Reopening runs the backfill, which reconciles the catalog from the probes.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = s2.Close() }()

	campuses, err := s2.ListCampuses()
	if err != nil {
		t.Fatalf("ListCampuses() error = %v", err)
	}
	if len(campuses) != 1 || campuses[0].Code != "hx" || campuses[0].Name != "虎溪" {
		t.Fatalf("campuses = %+v, want one entry hx/虎溪", campuses)
	}
	buildings, err := s2.ListBuildings()
	if err != nil {
		t.Fatalf("ListBuildings() error = %v", err)
	}
	if len(buildings) != 1 || buildings[0].Code != "sy01" || buildings[0].CampusCode != "hx" {
		t.Fatalf("buildings = %+v, want one entry sy01 under hx", buildings)
	}
	if buildings[0].BuildingGroupCode != "sy" || buildings[0].Name != "松园一栋" {
		t.Errorf("backfilled building lost its group or name: %+v", buildings[0])
	}
}

func TestBackfillDoesNotResurrectDeletedCampuses(t *testing.T) {
	// Once the catalog has entries, the backfill must not run again — otherwise
	// an administrator who deletes a campus would see it come back on restart.
	s := newTestStore(t)
	if err := s.CreateCampus(&Campus{Code: "manual", Name: "手工添加"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s.backfillCatalog(); err != nil {
		t.Fatalf("backfillCatalog() error = %v", err)
	}
	campuses, err := s.ListCampuses()
	if err != nil {
		t.Fatalf("ListCampuses() error = %v", err)
	}
	if len(campuses) != 1 || campuses[0].Code != "manual" {
		t.Fatalf("campuses = %+v; a non-empty catalog must suppress the backfill", campuses)
	}
}

func TestCatalogCRUD(t *testing.T) {
	s := newTestStore(t)

	c := &Campus{Code: "hx", Name: "虎溪"}
	if err := s.CreateCampus(c); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := s.CreateCampus(&Campus{Code: "hx", Name: "dup"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate campus error = %v, want ErrDuplicate", err)
	}

	if err := s.UpdateCampusName("hx", "虎溪校区"); err != nil {
		t.Fatalf("UpdateCampusName() error = %v", err)
	}
	got, err := s.GetCampus("hx")
	if err != nil {
		t.Fatalf("GetCampus() error = %v", err)
	}
	if got.Name != "虎溪校区" {
		t.Errorf("Name = %q, want 虎溪校区", got.Name)
	}
	if err := s.UpdateCampusName("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown campus update = %v, want ErrNotFound", err)
	}

	b := &Building{Code: "sy01", CampusCode: "hx", BuildingGroupCode: "sy",
		BuildingGroupName: "松园", Name: "松园一栋"}
	if err := s.CreateBuilding(b); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	if err := s.CreateBuilding(&Building{Code: "sy02", CampusCode: "nope",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园二栋"}); err == nil {
		t.Fatal("a building under an unknown campus must be rejected")
	}

	byCampus, err := s.ListBuildingsByCampus("hx")
	if err != nil {
		t.Fatalf("ListBuildingsByCampus() error = %v", err)
	}
	if len(byCampus) != 1 || byCampus[0].Code != "sy01" {
		t.Fatalf("byCampus = %+v, want [sy01]", byCampus)
	}
	if other, err := s.ListBuildingsByCampus("nope"); err != nil || len(other) != 0 {
		t.Fatalf("ListBuildingsByCampus(nope) = %v, %v; want empty, nil", other, err)
	}

	if err := s.UpdateBuilding(&Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋(改)"}); err != nil {
		t.Fatalf("UpdateBuilding() error = %v", err)
	}
	gotB, err := s.GetBuilding("sy01")
	if err != nil {
		t.Fatalf("GetBuilding() error = %v", err)
	}
	if gotB.Name != "松园一栋(改)" {
		t.Errorf("Name = %q", gotB.Name)
	}
}

func TestCatalogDeleteBlockedWhileReferenced(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateCampus(&Campus{Code: "hx", Name: "虎溪"}); err != nil {
		t.Fatalf("CreateCampus() error = %v", err)
	}
	if err := s.CreateBuilding(&Building{Code: "sy01", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	p := sampleProbe("hx-sy01-aaaaaa", "hash-a")
	if err := s.CreateProbe(p); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}

	if err := s.DeleteBuilding("sy01"); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteBuilding() with a probe referencing it = %v, want ErrInUse", err)
	}
	if err := s.DeleteCampus("hx"); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteCampus() with a probe referencing it = %v, want ErrInUse", err)
	}
	if n, err := s.BuildingProbeCount("sy01"); err != nil || n != 1 {
		t.Errorf("BuildingProbeCount() = %d, %v; want 1, nil", n, err)
	}
	if n, err := s.CampusProbeCount("hx"); err != nil || n != 1 {
		t.Errorf("CampusProbeCount() = %d, %v; want 1, nil", n, err)
	}

	// A building with no probes deletes, and so does a campus whose buildings
	// are gone.
	if err := s.CreateBuilding(&Building{Code: "sy09", CampusCode: "hx",
		BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园九栋"}); err != nil {
		t.Fatalf("CreateBuilding() error = %v", err)
	}
	if err := s.DeleteBuilding("sy09"); err != nil {
		t.Fatalf("DeleteBuilding() unreferenced = %v, want nil", err)
	}
}

func TestBuildingGroupsAreDistinctAndSorted(t *testing.T) {
	s := newTestStore(t)
	for _, b := range []Building{
		{Code: "sy01", CampusCode: "hx", BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园一栋"},
		{Code: "sy02", CampusCode: "hx", BuildingGroupCode: "sy", BuildingGroupName: "松园", Name: "松园二栋"},
		{Code: "ml01", CampusCode: "hx", BuildingGroupCode: "ml", BuildingGroupName: "梅园", Name: "梅园一栋"},
	} {
		if err := s.CreateCampus(&Campus{Code: "hx", Name: "虎溪"}); err != nil && !errors.Is(err, ErrDuplicate) {
			t.Fatalf("CreateCampus() error = %v", err)
		}
		bb := b
		if err := s.CreateBuilding(&bb); err != nil {
			t.Fatalf("CreateBuilding(%s) error = %v", b.Code, err)
		}
	}
	groups, err := s.BuildingGroups()
	if err != nil {
		t.Fatalf("BuildingGroups() error = %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want 2 distinct", groups)
	}
	if groups[0].Code != "ml" || groups[1].Code != "sy" {
		t.Errorf("groups = %+v, want sorted ml then sy", groups)
	}
	if groups[1].Name != "松园" {
		t.Errorf("groups[1].Name = %q, want 松园", groups[1].Name)
	}
}

func TestProbeRotationAndCreatedViaRoundTrip(t *testing.T) {
	s := newTestStore(t)

	p := sampleProbe("hx-sy01-aaaaaa", "hash-a")
	p.CreatedVia = "public"
	if err := s.CreateProbe(p); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	got, err := s.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.CreatedVia != "public" {
		t.Errorf("CreatedVia = %q, want public", got.CreatedVia)
	}
	if !got.RotatedAt.IsZero() {
		t.Errorf("RotatedAt = %v, want zero before any rotation", got.RotatedAt)
	}

	if err := s.UpdateProbeToken("hx-sy01-aaaaaa", "hash-b"); err != nil {
		t.Fatalf("UpdateProbeToken() error = %v", err)
	}
	got, err = s.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() after rotate error = %v", err)
	}
	if got.RotatedAt.IsZero() {
		t.Error("RotatedAt is still zero after a rotation")
	}
	if got.CreatedVia != "public" {
		t.Errorf("CreatedVia changed to %q on rotation, want public", got.CreatedVia)
	}
}

func TestCreateProbeDefaultsCreatedViaToAdmin(t *testing.T) {
	s := newTestStore(t)
	p := sampleProbe("hx-sy01-aaaaaa", "hash-a")
	p.CreatedVia = "" // callers that predate the column leave it empty
	if err := s.CreateProbe(p); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	got, err := s.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.CreatedVia != "admin" {
		t.Errorf("CreatedVia = %q, want admin", got.CreatedVia)
	}
}
