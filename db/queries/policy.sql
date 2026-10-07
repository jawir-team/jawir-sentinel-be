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

-- name: GetActivePolicyVersion :one
SELECT id, policy_id, version, status, index_status, index_error, index_attempt_id,
       index_started_at, indexed_at, content, file_path, effective_from,
       effective_until, created_by, approved_by, created_at, approved_at
FROM policy_versions
WHERE policy_id = $1 AND status = 'ACTIVE';

-- name: GetActivePolicyVersionForUpdate :one
SELECT id, policy_id, version, status, index_status, index_error, index_attempt_id,
       index_started_at, indexed_at, content, file_path, effective_from,
       effective_until, created_by, approved_by, created_at, approved_at
FROM policy_versions
WHERE policy_id = $1 AND status = 'ACTIVE'
FOR UPDATE;

-- name: ClaimPolicyVersionIndex :one
UPDATE policy_versions
SET index_status = 'PROCESSING',
    index_attempt_id = $2,
    index_started_at = now(),
    index_error = NULL,
    indexed_at = NULL
WHERE id = $1
RETURNING id, policy_id, version, status, index_status, index_error, index_attempt_id,
          index_started_at, indexed_at, content, file_path, effective_from,
          effective_until, created_by, approved_by, created_at, approved_at;

-- name: CompletePolicyVersionIndex :one
UPDATE policy_versions
SET index_status = $2,
    index_error = $3,
    indexed_at = CASE WHEN $2 = 'READY' THEN now() ELSE indexed_at END
WHERE id = $1 AND index_attempt_id = $4
RETURNING id, policy_id, version, status, index_status, index_error, index_attempt_id,
          index_started_at, indexed_at, content, file_path, effective_from,
          effective_until, created_by, approved_by, created_at, approved_at;

-- name: UpdatePolicyVersionStatus :one
UPDATE policy_versions
SET status = $2
WHERE id = $1
RETURNING id, policy_id, version, status, index_status, index_error, index_attempt_id,
          index_started_at, indexed_at, content, file_path, effective_from,
          effective_until, created_by, approved_by, created_at, approved_at;

-- name: DeletePolicyChunks :exec
DELETE FROM policy_chunks WHERE policy_version_id = $1;

-- name: CreatePolicyChunk :one
INSERT INTO policy_chunks (id, policy_version_id, section, chunk_index, content, embedding)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, policy_version_id, section, chunk_index, content, embedding, created_at;

-- name: SearchPolicyChunks :many
SELECT pc.id AS chunk_id, pc.policy_version_id, pc.chunk_index, pc.section, pc.content,
       (pc.embedding <=> $1::vector) AS distance,
       pv.id AS version_id, pv.version AS version,
       p.id AS policy_id, p.code AS policy_code, p.title AS policy_title
FROM policy_chunks pc
JOIN policy_versions pv ON pv.id = pc.policy_version_id
JOIN policies p ON p.id = pv.policy_id
WHERE pv.status = 'ACTIVE'
  AND pv.index_status = 'READY'
  AND (pv.effective_from IS NULL OR pv.effective_from <= now())
  AND (pv.effective_until IS NULL OR pv.effective_until > now())
  AND (p.case_type_id = $2 OR p.case_type_id IS NULL)
ORDER BY pc.embedding <=> $1::vector
LIMIT $3;

-- name: ListActiveReadyPolicyChunks :many
SELECT pc.id AS chunk_id, pc.policy_version_id, pc.chunk_index, pc.section, pc.content,
       pv.version AS version, p.id AS policy_id, p.code AS policy_code,
       p.title AS policy_title
FROM policy_chunks pc
JOIN policy_versions pv ON pv.id = pc.policy_version_id
JOIN policies p ON p.id = pv.policy_id
WHERE pv.status = 'ACTIVE'
  AND pv.index_status = 'READY'
  AND (pv.effective_from IS NULL OR pv.effective_from <= now())
  AND (pv.effective_until IS NULL OR pv.effective_until > now())
  AND (p.case_type_id = $1 OR p.case_type_id IS NULL)
ORDER BY p.code, pv.version, pc.chunk_index, pc.id;
