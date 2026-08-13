-- +goose Up
-- Rebuild kiosks without the display-name UNIQUE constraint: global
-- uniqueness moves to the normalized full-Unicode case-folded name_key,
-- which kiosks.Migrate backfills after this structural step (SQLite cannot
-- case-fold). version is the durable identity version, initialized to 1.
CREATE TABLE kiosks_new (
    id text PRIMARY KEY,
    ip text UNIQUE NOT NULL,
    name text NOT NULL,
    name_key text NOT NULL DEFAULT '',
    version integer NOT NULL DEFAULT 1
);

INSERT INTO kiosks_new (id, ip, name, name_key, version)
SELECT id, ip, name, '', 1 FROM kiosks;

DROP TABLE kiosks;

ALTER TABLE kiosks_new RENAME TO kiosks;

-- +goose Down
CREATE TABLE kiosks_restored (
    id text PRIMARY KEY,
    ip text UNIQUE NOT NULL,
    name text UNIQUE NOT NULL
);

INSERT INTO kiosks_restored (id, ip, name)
SELECT id, ip, name FROM kiosks;

DROP TABLE kiosks;

ALTER TABLE kiosks_restored RENAME TO kiosks;
