-- name: MakeRefreshToken :one
INSERT INTO refresh_tokens (
    token,
    created_at,
    updated_at,
    user_id,
    expires_at
) VALUES (
    hex(randomblob(32)),
    datetime('now'),
    datetime('now'),
    ?,
    datetime('now', '+60 days')
)
RETURNING *;

-- name: GetRefreshToken :one
SELECT
    token,
    created_at,
    updated_at,
    user_id,
    expires_at,
    revoked_at
FROM refresh_tokens
WHERE token = ?;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET
    revoked_at = datetime('now'),
    updated_at = datetime('now')
WHERE token = ?;
