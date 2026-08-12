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

-- name: NameHeldByOther :one
SELECT EXISTS (
    SELECT 1
    FROM kiosks
    WHERE ip != sqlc.arg('ip') AND name = sqlc.arg('name')
) AS held;

-- name: ListKiosksByIDs :many
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE id IN (sqlc.slice('ids'))
ORDER BY name;
