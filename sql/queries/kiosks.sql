-- name: CreateKiosk :one
INSERT INTO kiosks (
    id,
    ip,
    name,
    name_key
) VALUES (
    ?,
    ?,
    ?,
    ?
)
RETURNING id, ip, name, name_key, version;

-- name: GetKioskByIP :one
SELECT
    id,
    ip,
    name,
    name_key,
    version
FROM kiosks
WHERE ip = ?;

-- name: NameKeyHeldByOther :one
SELECT EXISTS (
    SELECT 1
    FROM kiosks
    WHERE ip != sqlc.arg('ip') AND name_key = sqlc.arg('name_key')
) AS held;

-- name: ListKiosksByIDs :many
SELECT
    id,
    ip,
    name,
    name_key,
    version
FROM kiosks
WHERE id IN (sqlc.slice('ids'))
ORDER BY name;
