-- +goose Up
CREATE TABLE users (
    id text PRIMARY KEY,
    username text UNIQUE NOT NULL,
    password text NOT NULL
);

-- +goose Down
DROP TABLE users;
