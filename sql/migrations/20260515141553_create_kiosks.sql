-- +goose Up
CREATE TABLE kiosks (
    id text PRIMARY KEY,
    ip text UNIQUE NOT NULL,
    name text UNIQUE NOT NULL
);

-- +goose Down
DROP TABLE kiosks;

