-- name: ListCaseEvidence :many
SELECT id, case_id, source_type, source_user_id, evidence_type, title,
       content, file_path, mime_type, created_at
FROM case_evidences
WHERE case_id = $1
ORDER BY created_at DESC, id DESC;

-- name: CreateCaseEvidence :one
INSERT INTO case_evidences
    (id, case_id, source_type, source_user_id, evidence_type, title, content, file_path, mime_type)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, case_id, source_type, source_user_id, evidence_type, title,
          content, file_path, mime_type, created_at;
