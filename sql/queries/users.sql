-- name: CreateUser :one
INSERT INTO users (id, username, password) VALUES (?, ?, ?) RETURNING *;

-- name: GetUser :one
SELECT
    id,
    username,
    password
FROM users
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT
    id,
    username,
    password
FROM users
WHERE username = ?;
