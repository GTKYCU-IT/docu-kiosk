-- +goose Up
CREATE TABLE refresh_tokens (
    token text PRIMARY KEY,
    created_at integer NOT NULL,
    updated_at integer NOT NULL,
    user_id text NOT NULL,
    expires_at integer NOT NULL,
    revoked_at integer,
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE refresh_tokens;
