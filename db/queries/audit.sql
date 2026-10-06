-- name: ListCaseAuditEvents :many
SELECT id, scope_type, case_id, policy_id, policy_version_id, event_type,
       actor_id, actor_role, analysis_id, metadata, created_at
FROM audit_events
WHERE case_id = $1
ORDER BY created_at, id;

-- name: AppendCaseAuditEvent :one
INSERT INTO audit_events
    (id, scope_type, case_id, event_type, actor_id, actor_role, analysis_id, metadata)
VALUES ($1, 'CASE', $2, $3, $4, $5, $6, $7)
RETURNING id, scope_type, case_id, policy_id, policy_version_id, event_type,
          actor_id, actor_role, analysis_id, metadata, created_at;

-- name: AppendPolicyAuditEvent :one
INSERT INTO audit_events
    (id, scope_type, policy_id, policy_version_id, event_type, actor_id, metadata)
VALUES ($1, 'POLICY', $2, $3, $4, $5, $6)
RETURNING id, scope_type, case_id, policy_id, policy_version_id, event_type,
          actor_id, actor_role, analysis_id, metadata, created_at;
