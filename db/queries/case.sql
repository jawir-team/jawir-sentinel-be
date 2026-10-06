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
