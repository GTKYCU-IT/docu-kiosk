-- +goose Up
CREATE TABLE refresh_tokens (
    token text PRIMARY KEY,
    created_at datetime NOT NULL,
    updated_at datetime NOT NULL,
    user_id text NOT NULL,
    expires_at datetime NOT NULL,
    revoked_at datetime,
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE refresh_tokens;
