-- name: ListDecisionsByAnalysis :many
SELECT id, case_id, analysis_id, actor_id, actor_role, decision, reason, comment, created_at
FROM decisions
WHERE analysis_id = $1
ORDER BY created_at, id;

-- name: CreateDecision :one
INSERT INTO decisions (id, case_id, analysis_id, actor_id, actor_role, decision, reason, comment)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, case_id, analysis_id, actor_id, actor_role, decision, reason, comment, created_at;
