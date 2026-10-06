-- name: GetUnit :one
SELECT id, code, name, description, created_at, updated_at
FROM units
WHERE id = $1;

-- name: ListUnits :many
SELECT id, code, name, description, created_at, updated_at
FROM units
ORDER BY name, id;

-- name: CreateUnit :one
INSERT INTO units (id, code, name, description)
VALUES ($1, $2, $3, $4)
RETURNING id, code, name, description, created_at, updated_at;
