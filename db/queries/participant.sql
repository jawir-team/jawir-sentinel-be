-- name: ListCaseParticipants :many
SELECT id, case_id, user_id, role, required, status, assigned_by,
       assigned_at, unassigned_at
FROM case_participants
WHERE case_id = $1
ORDER BY assigned_at, id;

-- name: GetActiveParticipantForUpdate :one
SELECT id, case_id, user_id, role, required, status, assigned_by,
       assigned_at, unassigned_at
FROM case_participants
WHERE id = $1 AND status = 'ACTIVE'
FOR UPDATE;

-- name: CreateCaseParticipant :one
INSERT INTO case_participants (id, case_id, user_id, role, required, assigned_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, case_id, user_id, role, required, status, assigned_by,
          assigned_at, unassigned_at;

-- name: UnassignCaseParticipant :one
UPDATE case_participants
SET status = 'INACTIVE', unassigned_at = now()
WHERE id = $1 AND status = 'ACTIVE'
RETURNING id, case_id, user_id, role, required, status, assigned_by,
          assigned_at, unassigned_at;
