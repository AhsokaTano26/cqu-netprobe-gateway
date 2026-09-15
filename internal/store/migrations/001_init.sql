CREATE TABLE IF NOT EXISTS probes (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    probe_id            TEXT    NOT NULL UNIQUE,
    token_hash          TEXT    NOT NULL UNIQUE,
    campus_code         TEXT    NOT NULL,
    campus_name         TEXT    NOT NULL,
    building_group_code TEXT    NOT NULL,
    building_group_name TEXT    NOT NULL,
    building_code       TEXT    NOT NULL,
    building_name       TEXT    NOT NULL,
    network_type        TEXT    NOT NULL,
    enabled             INTEGER NOT NULL DEFAULT 1,
    description         TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_probes_campus ON probes(campus_code);

CREATE TABLE IF NOT EXISTS targets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    target_id    TEXT    NOT NULL UNIQUE,
    display_name TEXT    NOT NULL DEFAULT '',
    address      TEXT    NOT NULL DEFAULT '',
    description  TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS target_probe_types (
    target_id  TEXT NOT NULL REFERENCES targets(target_id) ON DELETE CASCADE,
    probe_type TEXT NOT NULL,
    PRIMARY KEY (target_id, probe_type)
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
