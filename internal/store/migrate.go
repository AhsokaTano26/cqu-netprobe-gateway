package store

import (
	"embed"
	"fmt"
	"sort"
)

// Plain "migrations" (not "all:migrations") deliberately excludes dotfiles:
// the loader rejects any filename that is not NNN_*, so a stray .DS_Store in
// this directory would otherwise make Open fail with a confusing version error.
//
//go:embed migrations
var migrationFS embed.FS

// migration is one numbered schema step. Version 1 is the initial schema.
type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var version int
		if _, err := fmt.Sscanf(name, "%03d_", &version); err != nil {
			return nil, fmt.Errorf("store: migration %q does not start with a 3-digit version", name)
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate creates the schema_migrations bookkeeping table and applies every
// migration whose version is not yet recorded, in ascending order.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: iterate schema_migrations: %w", err)
	}
	_ = rows.Close()

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, nowUnix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", m.version, err)
		}
	}

	if err := s.backfillCatalog(); err != nil {
		return err
	}
	return s.seed()
}

// backfillCatalog seeds the catalog from probes that predate it, so an upgraded
// gateway keeps offering its current campuses and buildings in the dropdowns
// instead of presenting an empty list and making existing locations
// unselectable.
//
// It runs on every Open but acts only when the catalog is empty, which mirrors
// the target seeding rule: an administrator who deletes a campus does not get it
// resurrected by the next restart. It is deliberately not part of migration 002
// — a migration runs once, so a database already at 002 could never be
// reconciled.
func (s *Store) backfillCatalog() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM campuses`).Scan(&n); err != nil {
		return fmt.Errorf("store: count campuses: %w", err)
	}
	if n > 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// GROUP BY picks one name per code. A code whose names disagree across probe
	// rows is a pre-existing inconsistency this reconciliation cannot resolve; it
	// keeps the first row SQLite returns.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO campuses (code, name, created_at, updated_at)
		SELECT campus_code, campus_name, ?, ? FROM probes GROUP BY campus_code`,
		nowUnix(), nowUnix()); err != nil {
		return fmt.Errorf("store: backfill campuses: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO buildings (code, campus_code, building_group_code,
		building_group_name, name, created_at, updated_at)
		SELECT building_code, campus_code, building_group_code, building_group_name,
		       building_name, ?, ? FROM probes GROUP BY building_code`,
		nowUnix(), nowUnix()); err != nil {
		return fmt.Errorf("store: backfill buildings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit backfill: %w", err)
	}
	return nil
}

// defaultTargets is the Target set from Protocol v1 §12. Every entry carries an
// explicit Enabled: true, because the zero value of Target.Enabled is false and
// seeding is what makes these targets live.
//
// campus_dns has no agreed address yet, so it is seeded empty for an
// administrator to fill in.
var defaultTargets = []Target{
	{TargetID: "campus_dns", DisplayName: "校园 DNS", Address: "", ProbeTypes: []string{"icmp", "dns"},
		Description: "地址待管理员填写", Enabled: true},
	{TargetID: "aliyun_dns", DisplayName: "阿里 DNS", Address: "223.5.5.5", ProbeTypes: []string{"icmp"}, Enabled: true},
	{TargetID: "dnspod_dns", DisplayName: "DNSPod DNS", Address: "119.29.29.29", ProbeTypes: []string{"icmp"}, Enabled: true},
	{TargetID: "cloudflare_dns", DisplayName: "Cloudflare DNS", Address: "1.1.1.1", ProbeTypes: []string{"icmp"}, Enabled: true},
	{TargetID: "cqu_mirror", DisplayName: "CQU 镜像站", Address: "https://mirrors.cqu.edu.cn/", ProbeTypes: []string{"http"}, Enabled: true},
}

// seed inserts the Protocol v1 §12 targets, but only while the targets table is
// completely empty: the guard is COUNT(*) == 0, so the guarantee is "runs once
// while any target exists", not "runs once ever". Deleting a single target is
// therefore permanent, but deleting every target re-seeds the full default set
// on the next Open.
//
// The five defaults are seeded in a single transaction, so a failure partway
// through commits nothing and the next Open retries the whole set: the
// COUNT(*) == 0 guard would never re-seed the entries a partial failure had
// already skipped, leaving the gateway permanently rejecting them with 400
// invalid_target.
func (s *Store) seed() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM targets`).Scan(&count); err != nil {
		return fmt.Errorf("store: count targets: %w", err)
	}
	if count > 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin seed: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i := range defaultTargets {
		// Copy: createTargetTx stamps CreatedAt/UpdatedAt on the value it is
		// given, and the package-level defaults must not be mutated.
		target := defaultTargets[i]
		if err := createTargetTx(tx, &target); err != nil {
			return fmt.Errorf("store: seed target %s: %w", target.TargetID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit seed: %w", err)
	}
	return nil
}
