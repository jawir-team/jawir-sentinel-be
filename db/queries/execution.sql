-- name: GetExecutionForUpdate :one
SELECT id, case_id, analysis_id, executer_id, status, action_taken, result,
       blocker, started_at, completed_at, created_at
FROM executions
WHERE id = $1
FOR UPDATE;

-- name: CreateExecution :one
INSERT INTO executions (id, case_id, analysis_id, executer_id, status)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, case_id, analysis_id, executer_id, status, action_taken, result,
          blocker, started_at, completed_at, created_at;

-- name: UpdateExecution :one
UPDATE executions
SET status = $2, action_taken = $3, result = $4, blocker = $5, completed_at = $6
WHERE id = $1
RETURNING id, case_id, analysis_id, executer_id, status, action_taken, result,
          blocker, started_at, completed_at, created_at;

-- name: ExistsRunningExecution :one
SELECT EXISTS(SELECT 1 FROM executions WHERE case_id = $1 AND status = 'IN_PROGRESS');
