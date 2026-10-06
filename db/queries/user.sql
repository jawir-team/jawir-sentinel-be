-- name: GetUser :one
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserForUpdate :one
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE id = $1
FOR UPDATE;

-- name: GetUserByFirebaseUID :one
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE firebase_uid = $1;

-- name: ListUsersByUnit :many
SELECT id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at
FROM users
WHERE unit_id = $1
ORDER BY name, id;

-- name: ListUsers :many
SELECT id, unit_id, name, email, status, system_role, created_at, updated_at
FROM users
WHERE (
    sqlc.arg(search)::text = ''
    OR name ILIKE '%' || sqlc.arg(search)::text || '%'
    OR email ILIKE '%' || sqlc.arg(search)::text || '%'
)
AND (
    sqlc.arg(status)::text = ''
    OR status = sqlc.arg(status)::text
)
ORDER BY name, id
LIMIT sqlc.arg('limit')::integer
OFFSET sqlc.arg('offset')::integer;

-- name: CreateUser :one
INSERT INTO users (id, unit_id, firebase_uid, name, email, status, system_role)
VALUES (
    sqlc.arg(id),
    sqlc.arg(unit_id),
    sqlc.arg(firebase_uid),
    sqlc.arg(name),
    sqlc.arg(email),
    sqlc.arg(status),
    sqlc.arg(system_role)
)
RETURNING id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at;

-- name: CountActiveAdmins :one
SELECT count(*)
FROM users
WHERE status = 'ACTIVE' AND system_role = 'ADMIN';

-- name: UpdateUser :one
WITH active_admins AS MATERIALIZED (
    SELECT id
    FROM users
    WHERE status = 'ACTIVE'
      AND system_role = 'ADMIN'
      AND (
          sqlc.arg(status)::text <> 'ACTIVE'
          OR sqlc.arg(system_role)::text <> 'ADMIN'
      )
    ORDER BY id
    FOR UPDATE
)
UPDATE users AS target
SET unit_id = sqlc.arg(unit_id),
    name = sqlc.arg(name),
    email = sqlc.arg(email),
    status = sqlc.arg(status),
    system_role = sqlc.arg(system_role),
    updated_at = now()
WHERE target.id = sqlc.arg(id)
  AND (
      target.status <> 'ACTIVE'
      OR target.system_role <> 'ADMIN'
      OR (
          sqlc.arg(status)::text = 'ACTIVE'
          AND sqlc.arg(system_role)::text = 'ADMIN'
      )
      OR EXISTS (
          SELECT 1
          FROM active_admins
          WHERE active_admins.id <> target.id
      )
  )
RETURNING id, unit_id, firebase_uid, name, email, status, system_role, created_at, updated_at;
