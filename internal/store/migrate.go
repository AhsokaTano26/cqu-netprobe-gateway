package store

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed all:migrations
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

	return s.seed()
}

// seedTarget is one row of the built-in target set inserted on a fresh
// database. It is deliberately separate from Target (added in a later task):
// seeding writes rows directly and never goes through the CRUD API.
type seedTarget struct {
	TargetID    string
	DisplayName string
	Address     string
	Description string
	ProbeTypes  []string
}

// defaultTargets is the Target set from Protocol v1 §12. campus_dns has no
// agreed address yet, so it is seeded empty for an administrator to fill in.
var defaultTargets = []seedTarget{
	{TargetID: "campus_dns", DisplayName: "校园 DNS", Address: "", ProbeTypes: []string{"icmp", "dns"},
		Description: "地址待管理员填写"},
	{TargetID: "aliyun_dns", DisplayName: "阿里 DNS", Address: "223.5.5.5", ProbeTypes: []string{"icmp"}},
	{TargetID: "dnspod_dns", DisplayName: "DNSPod DNS", Address: "119.29.29.29", ProbeTypes: []string{"icmp"}},
	{TargetID: "cloudflare_dns", DisplayName: "Cloudflare DNS", Address: "1.1.1.1", ProbeTypes: []string{"icmp"}},
	{TargetID: "cqu_mirror", DisplayName: "CQU 镜像站", Address: "https://mirrors.cqu.edu.cn/", ProbeTypes: []string{"http"}},
}

// seed inserts the Protocol v1 §12 targets the first time the table is empty.
// It never runs again, so an administrator deleting a default target does not
// get it resurrected on the next restart.
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

	now := nowUnix()
	for _, t := range defaultTargets {
		if _, err := tx.Exec(`INSERT INTO targets (target_id, display_name, address, description,
			enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
			t.TargetID, t.DisplayName, t.Address, t.Description, now, now); err != nil {
			return fmt.Errorf("store: seed target %s: %w", t.TargetID, err)
		}
		for _, pt := range t.ProbeTypes {
			if _, err := tx.Exec(`INSERT INTO target_probe_types (target_id, probe_type) VALUES (?, ?)`,
				t.TargetID, pt); err != nil {
				return fmt.Errorf("store: seed probe type %s/%s: %w", t.TargetID, pt, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit seed: %w", err)
	}
	return nil
}
