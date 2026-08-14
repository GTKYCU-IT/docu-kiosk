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
    ?
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

-- name: RotateRefreshToken :one
UPDATE refresh_tokens
SET
    token = hex(randomblob(32)),
    created_at = datetime('now'),
    updated_at = datetime('now'),
    expires_at = ?
WHERE token = ?
  AND revoked_at IS NULL
  AND expires_at > datetime('now')
RETURNING *;

-- name: RevokeCurrentRefreshToken :one
UPDATE refresh_tokens
SET
    revoked_at = datetime('now'),
    updated_at = datetime('now')
WHERE token = ?
  AND revoked_at IS NULL
  AND expires_at > datetime('now')
RETURNING *;
