-- name: ListCaseParticipants :many
SELECT id, case_id, user_id, role, required, status, assigned_by,
       assigned_at, unassigned_at
FROM case_participants
WHERE case_id = $1
ORDER BY assigned_at, id;

-- name: GetParticipantByCaseUserRole :one
SELECT id, case_id, user_id, role, required, status, assigned_by,
       assigned_at, unassigned_at
FROM case_participants
WHERE case_id = sqlc.arg(case_id)
  AND user_id = sqlc.arg(user_id)
  AND role = sqlc.arg(role);

-- name: GetActiveParticipantForUpdate :one
SELECT id, case_id, user_id, role, required, status, assigned_by,
       assigned_at, unassigned_at
FROM case_participants
WHERE id = $1 AND status = 'ACTIVE'
FOR UPDATE;

-- name: IsActiveCaseParticipant :one
SELECT EXISTS (
    SELECT 1
    FROM case_participants
    WHERE case_id = sqlc.arg(case_id)
      AND user_id = sqlc.arg(user_id)
      AND status = 'ACTIVE'
) AS is_participant;

-- name: CreateCaseParticipant :one
INSERT INTO case_participants (id, case_id, user_id, role, required, assigned_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, case_id, user_id, role, required, status, assigned_by,
          assigned_at, unassigned_at;

-- name: ReactivateCaseParticipant :one
UPDATE case_participants
SET status = 'ACTIVE',
    unassigned_at = NULL,
    assigned_by = sqlc.arg(assigned_by),
    assigned_at = now()
WHERE id = sqlc.arg(id) AND status = 'INACTIVE'
RETURNING id, case_id, user_id, role, required, status, assigned_by,
          assigned_at, unassigned_at;

-- name: CountActiveParticipantsByRole :one
SELECT count(*)
FROM case_participants
WHERE case_id = sqlc.arg(case_id)
  AND role = sqlc.arg(role)
  AND status = 'ACTIVE';

-- name: UnassignCaseParticipant :one
UPDATE case_participants
SET status = 'INACTIVE', unassigned_at = now()
WHERE id = $1 AND status = 'ACTIVE'
RETURNING id, case_id, user_id, role, required, status, assigned_by,
          assigned_at, unassigned_at;
