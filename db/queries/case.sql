-- name: GetCase :one
SELECT id, case_number, case_type_id, title, description, urgency, status,
       created_by, owner_id, current_analysis_id, closed_by, close_reason,
       closed_at, created_at, updated_at
FROM cases
WHERE id = $1;

-- name: GetCaseForUpdate :one
SELECT id, case_number, case_type_id, title, description, urgency, status,
       created_by, owner_id, current_analysis_id, closed_by, close_reason,
       closed_at, created_at, updated_at
FROM cases
WHERE id = $1
FOR UPDATE;

-- name: ListCasesByOwner :many
SELECT id, case_number, case_type_id, title, description, urgency, status,
       created_by, owner_id, current_analysis_id, closed_by, close_reason,
       closed_at, created_at, updated_at
FROM cases
WHERE owner_id = $1
ORDER BY created_at DESC, id DESC;

-- name: ListCasesForUser :many
SELECT c.id, c.case_number, c.case_type_id, c.title, c.description, c.urgency, c.status,
       c.created_by, c.owner_id, c.current_analysis_id, c.closed_by, c.close_reason,
       c.closed_at, c.created_at, c.updated_at
FROM cases AS c
WHERE sqlc.arg(is_admin)::boolean
   OR EXISTS (
       SELECT 1
       FROM case_participants AS cp
       WHERE cp.case_id = c.id
         AND cp.user_id = sqlc.arg(user_id)
         AND cp.status = 'ACTIVE'
   )
ORDER BY c.updated_at DESC, c.created_at DESC, c.id DESC;

-- name: CreateCase :one
INSERT INTO cases (id, case_number, case_type_id, title, description, urgency, created_by, owner_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, case_number, case_type_id, title, description, urgency, status,
          created_by, owner_id, current_analysis_id, closed_by, close_reason,
          closed_at, created_at, updated_at;

-- name: UpdateCaseStatus :one
UPDATE cases
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING id, case_number, case_type_id, title, description, urgency, status,
          created_by, owner_id, current_analysis_id, closed_by, close_reason,
          closed_at, created_at, updated_at;

-- name: UpdateCase :one
UPDATE cases
SET case_type_id = sqlc.arg(case_type_id),
    title = sqlc.arg(title),
    description = sqlc.arg(description),
    urgency = sqlc.arg(urgency),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'DRAFT'
RETURNING id, case_number, case_type_id, title, description, urgency, status,
          created_by, owner_id, current_analysis_id, closed_by, close_reason,
          closed_at, created_at, updated_at;

-- name: CloseCase :one
UPDATE cases
SET status = 'CLOSED', closed_by = $2, close_reason = $3,
    closed_at = now(), updated_at = now()
WHERE id = $1
RETURNING id, case_number, case_type_id, title, description, urgency, status,
          created_by, owner_id, current_analysis_id, closed_by, close_reason,
          closed_at, created_at, updated_at;
