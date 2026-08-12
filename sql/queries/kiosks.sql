-- name: UpsertKiosk :one
INSERT INTO kiosks (
    id,
    ip,
    name
) VALUES (
    ?,
    ?,
    ?
) ON CONFLICT(ip) DO UPDATE SET name = excluded.name
RETURNING id, ip, name;

-- name: GetKioskByIP :one
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE ip = ?;

-- name: GetKioskByName :one
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE name = ?;

-- name: ListKiosksByIDs :many
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE id IN (sqlc.slice('ids'))
ORDER BY name;
