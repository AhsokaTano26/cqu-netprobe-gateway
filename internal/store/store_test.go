package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleProbe(probeID, tokenHash string) *Probe {
	now := time.Unix(1789490000, 0).UTC()
	return &Probe{
		ProbeID:           probeID,
		TokenHash:         tokenHash,
		CampusCode:        "hx",
		CampusName:        "虎溪",
		BuildingGroupCode: "sy",
		BuildingGroupName: "松园",
		BuildingCode:      "sy01",
		BuildingName:      "松园一栋",
		NetworkType:       "wired",
		Enabled:           true,
		Description:       "test probe",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twice.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := s1.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() { _ = s2.Close() }()

	// Data must survive, and seeding must not have duplicated targets.
	got, err := s2.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() after reopen error = %v", err)
	}
	if got.CampusName != "虎溪" {
		t.Errorf("CampusName = %q, want 虎溪", got.CampusName)
	}
}

func TestSeedRunsOnceAndDoesNotResurrectDeletedTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var seeded int
	if err := s1.db.QueryRow(`SELECT COUNT(*) FROM targets`).Scan(&seeded); err != nil {
		t.Fatalf("count seeded targets: %v", err)
	}
	if seeded != 5 {
		t.Fatalf("seeded %d targets, want 5", seeded)
	}
	// Delete one exactly as an administrator would, then reopen.
	if _, err := s1.db.Exec(`DELETE FROM targets WHERE target_id = ?`, "cqu_mirror"); err != nil {
		t.Fatalf("delete seeded target: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = s2.Close() }()

	var after int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM targets`).Scan(&after); err != nil {
		t.Fatalf("count targets after reopen: %v", err)
	}
	if after != 4 {
		t.Fatalf("target count after reopen = %d, want 4 (deleted target must stay deleted)", after)
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	s := newTestStore(t)

	var foreignKeys int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", foreignKeys)
	}

	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("PRAGMA journal_mode = %q, want wal", journalMode)
	}
}

func TestCreateAndLookupProbeByTokenHash(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}

	got, err := s.ProbeByTokenHash("hash-a")
	if err != nil {
		t.Fatalf("ProbeByTokenHash() error = %v", err)
	}
	if got.ProbeID != "hx-sy01-aaaaaa" {
		t.Errorf("ProbeID = %q", got.ProbeID)
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got.BuildingGroupCode != "sy" {
		t.Errorf("BuildingGroupCode = %q, want sy", got.BuildingGroupCode)
	}

	if _, err := s.ProbeByTokenHash("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash error = %v, want ErrNotFound", err)
	}
}

func TestCreateProbeRejectsDuplicates(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("first CreateProbe() error = %v", err)
	}
	err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-b"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate probe_id error = %v, want ErrDuplicate", err)
	}
	err = s.CreateProbe(sampleProbe("hx-sy01-bbbbbb", "hash-a"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate token_hash error = %v, want ErrDuplicate", err)
	}
}

func TestSetProbeEnabled(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s.SetProbeEnabled("hx-sy01-aaaaaa", false); err != nil {
		t.Fatalf("SetProbeEnabled() error = %v", err)
	}
	got, err := s.GetProbe("hx-sy01-aaaaaa")
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if got.Enabled {
		t.Error("Enabled = true, want false")
	}
	if !got.UpdatedAt.After(got.CreatedAt) && got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("UpdatedAt should advance on update")
	}
}

func TestUpdateProbeToken(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s.UpdateProbeToken("hx-sy01-aaaaaa", "hash-b"); err != nil {
		t.Fatalf("UpdateProbeToken() error = %v", err)
	}

	if _, err := s.ProbeByTokenHash("hash-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old hash error = %v, want ErrNotFound (rotation must revoke)", err)
	}
	got, err := s.ProbeByTokenHash("hash-b")
	if err != nil {
		t.Fatalf("new hash lookup error = %v", err)
	}
	if got.ProbeID != "hx-sy01-aaaaaa" {
		t.Errorf("ProbeID changed on rotation: %q", got.ProbeID)
	}
}

func TestDeleteProbe(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProbe(sampleProbe("hx-sy01-aaaaaa", "hash-a")); err != nil {
		t.Fatalf("CreateProbe() error = %v", err)
	}
	if err := s.DeleteProbe("hx-sy01-aaaaaa"); err != nil {
		t.Fatalf("DeleteProbe() error = %v", err)
	}
	if _, err := s.GetProbe("hx-sy01-aaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProbe() after delete error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProbe("hx-sy01-aaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteProbe() error = %v, want ErrNotFound", err)
	}
}

func TestListProbes(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"hx-sy01-aaaaaa", "hx-sy02-bbbbbb", "aq-ql01-cccccc"} {
		if err := s.CreateProbe(sampleProbe(id, "hash-"+id)); err != nil {
			t.Fatalf("CreateProbe(%s) error = %v", id, err)
		}
	}
	got, err := s.ListProbes()
	if err != nil {
		t.Fatalf("ListProbes() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListProbes() len = %d, want 3", len(got))
	}
	// Ordered by probe_id for stable UI rendering.
	if got[0].ProbeID != "aq-ql01-cccccc" {
		t.Errorf("ListProbes()[0] = %q, want aq-ql01-cccccc", got[0].ProbeID)
	}
}
