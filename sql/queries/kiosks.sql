-- name: CreateKiosk :one
INSERT INTO kiosks (
    id,
    ip,
    name
) VALUES (
    ?,
    ?,
    ?
) RETURNING *;

-- name: GetKioskByIP :one
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE ip = ?;

-- name: GetKioskByID :one
SELECT
    id,
    ip,
    name
FROM kiosks
WHERE id = ?;

