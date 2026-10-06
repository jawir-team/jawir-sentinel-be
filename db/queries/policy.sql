-- name: GetPolicy :one
SELECT id, code, title, domain, case_type_id, description, created_at, updated_at
FROM policies
WHERE id = $1;

-- name: ListPolicies :many
SELECT id, code, title, domain, case_type_id, description, created_at, updated_at
FROM policies
ORDER BY title, id;

-- name: CreatePolicy :one
INSERT INTO policies (id, code, title, domain, case_type_id, description)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, code, title, domain, case_type_id, description, created_at, updated_at;

-- name: GetPolicyVersionForUpdate :one
SELECT id, policy_id, version, status, index_status, index_error, index_attempt_id,
       index_started_at, indexed_at, content, file_path, effective_from,
       effective_until, created_by, approved_by, created_at, approved_at
FROM policy_versions
WHERE id = $1
FOR UPDATE;

-- name: GetPolicyVersion :one
SELECT id, policy_id, version, status, index_status, index_error, index_attempt_id,
       index_started_at, indexed_at, content, file_path, effective_from,
       effective_until, created_by, approved_by, created_at, approved_at
FROM policy_versions
WHERE id = $1;

-- name: CreatePolicyVersion :one
INSERT INTO policy_versions (id, policy_id, version, content, file_path, created_by, effective_from, effective_until)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, policy_id, version, status, index_status, index_error, index_attempt_id,
          index_started_at, indexed_at, content, file_path, effective_from,
          effective_until, created_by, approved_by, created_at, approved_at;

-- name: ListPolicyVersions :many
SELECT id, policy_id, version, status, index_status, index_error, index_attempt_id,
       index_started_at, indexed_at, content, file_path, effective_from,
       effective_until, created_by, approved_by, created_at, approved_at
FROM policy_versions
WHERE policy_id = $1
ORDER BY created_at, id;

-- name: ListPolicyChunks :many
SELECT id, policy_version_id, section, chunk_index, content, embedding, created_at
FROM policy_chunks
WHERE policy_version_id = $1
ORDER BY chunk_index, id;
