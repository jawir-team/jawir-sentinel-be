-- name: GetUser :one
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByFirebaseUID :one
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE firebase_uid = $1;

-- name: ListUsersByUnit :many
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE unit_id = $1
ORDER BY name, id;

-- name: CreateUser :one
INSERT INTO users (id, unit_id, firebase_uid, name, email)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at;
