CREATE TABLE IF NOT EXISTS campuses (
    code       TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS buildings (
    code                TEXT PRIMARY KEY,
    campus_code         TEXT NOT NULL REFERENCES campuses(code) ON DELETE RESTRICT,
    building_group_code TEXT NOT NULL,
    building_group_name TEXT NOT NULL,
    name                TEXT NOT NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_buildings_campus ON buildings(campus_code);
CREATE INDEX IF NOT EXISTS idx_buildings_group  ON buildings(building_group_code);

ALTER TABLE probes ADD COLUMN rotated_at  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE probes ADD COLUMN created_via TEXT    NOT NULL DEFAULT 'admin';
