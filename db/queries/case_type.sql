-- name: GetCaseType :one
SELECT id, code, name, description, created_at, updated_at
FROM case_types
WHERE id = $1;

-- name: ListCaseTypes :many
SELECT id, code, name, description, created_at, updated_at
FROM case_types
ORDER BY name, id;

-- name: CreateCaseType :one
INSERT INTO case_types (id, code, name, description)
VALUES ($1, $2, $3, $4)
RETURNING id, code, name, description, created_at, updated_at;
