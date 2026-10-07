-- name: GetAnalysis :one
SELECT id, case_id, version, status, technical_retry_count, worker_attempt_id,
       worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
       compliance_analysis, recommendation, alternatives, missing_information,
       policy_status, evidence_quality, uncertainty, verification_status,
       verification_notes, model_name, prompt_version, created_at
FROM ai_analyses
WHERE id = $1;

-- name: GetAnalysisForUpdate :one
SELECT id, case_id, version, status, technical_retry_count, worker_attempt_id,
       worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
       compliance_analysis, recommendation, alternatives, missing_information,
       policy_status, evidence_quality, uncertainty, verification_status,
       verification_notes, model_name, prompt_version, created_at
FROM ai_analyses
WHERE id = $1
FOR UPDATE;

-- name: CreateAnalysis :one
INSERT INTO ai_analyses (id, case_id, version, status, model_name, prompt_version)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: UpdateAnalysisResult :one
UPDATE ai_analyses
SET status = $2, summary = $3, facts = $4, assumptions = $5, unknowns = $6,
    risk_analysis = $7, compliance_analysis = $8, recommendation = $9,
    alternatives = $10, missing_information = $11, policy_status = $12,
    evidence_quality = $13, uncertainty = $14, verification_status = $15,
    verification_notes = $16
WHERE id = $1
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: ExistsGeneratingAnalysis :one
SELECT EXISTS(SELECT 1 FROM ai_analyses WHERE case_id = $1 AND status = 'GENERATING');

-- name: GetLatestAnalysisForCase :one
SELECT id, case_id, version, status, technical_retry_count, worker_attempt_id,
       worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
       compliance_analysis, recommendation, alternatives, missing_information,
       policy_status, evidence_quality, uncertainty, verification_status,
       verification_notes, model_name, prompt_version, created_at
FROM ai_analyses
WHERE case_id = $1
ORDER BY version DESC, created_at DESC
LIMIT 1;

-- name: LockAnalysisVersionSeq :exec
SELECT pg_advisory_xact_lock(
    hashtext('ai_analysis_version:' || sqlc.arg(case_id)::uuid::text)
);

-- name: MaxAnalysisVersion :one
SELECT COALESCE(MAX(version), 0)::integer
FROM ai_analyses
WHERE case_id = $1;

-- name: CreateGeneratingAnalysis :one
INSERT INTO ai_analyses (
    id, case_id, version, status, technical_retry_count,
    worker_attempt_id, worker_started_at,
    summary, facts, assumptions, unknowns, risk_analysis,
    compliance_analysis, recommendation, alternatives, missing_information,
    policy_status, evidence_quality, uncertainty,
    verification_status, verification_notes,
    model_name, prompt_version
)
VALUES (
    $1, $2, $3, 'GENERATING', 0,
    $4, now(),
    NULL, NULL, NULL, NULL, NULL,
    NULL, NULL, NULL, NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    $5, $6
)
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: ReclaimGeneratingAnalysis :one
UPDATE ai_analyses
SET worker_attempt_id = $2, worker_started_at = now()
WHERE id = $1 AND status = 'GENERATING'
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: BumpTechnicalRetry :one
UPDATE ai_analyses
SET technical_retry_count = technical_retry_count + 1,
    worker_attempt_id = $3,
    worker_started_at = now()
WHERE id = $1
  AND status = 'GENERATING'
  AND worker_attempt_id = $2
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: FinalizeAnalysis :one
UPDATE ai_analyses
SET status = $3,
    summary = $4,
    facts = $5,
    assumptions = $6,
    unknowns = $7,
    risk_analysis = $8,
    compliance_analysis = $9,
    recommendation = $10,
    alternatives = $11,
    missing_information = $12,
    policy_status = $13,
    evidence_quality = $14,
    uncertainty = $15,
    verification_status = $16,
    verification_notes = $17
WHERE id = $1
  AND status = 'GENERATING'
  AND worker_attempt_id = $2
RETURNING id, case_id, version, status, technical_retry_count, worker_attempt_id,
          worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
          compliance_analysis, recommendation, alternatives, missing_information,
          policy_status, evidence_quality, uncertainty, verification_status,
          verification_notes, model_name, prompt_version, created_at;

-- name: LockCaseForAnalysis :one
SELECT id
FROM cases
WHERE id = $1
FOR UPDATE;

-- name: SetCaseCurrentAnalysis :exec
UPDATE cases AS c
SET current_analysis_id = $2, updated_at = now()
WHERE c.id = $1
  AND EXISTS (
      SELECT 1
      FROM ai_analyses AS a
      WHERE a.id = $2 AND a.case_id = c.id AND a.status = 'COMPLETED'
  );

-- name: ListAnalysisHistory :many
SELECT id, case_id, version, status, technical_retry_count, worker_attempt_id,
       worker_started_at, summary, facts, assumptions, unknowns, risk_analysis,
       compliance_analysis, recommendation, alternatives, missing_information,
       policy_status, evidence_quality, uncertainty, verification_status,
       verification_notes, model_name, prompt_version, created_at
FROM ai_analyses
WHERE case_id = $1
ORDER BY version DESC;

-- name: GetCurrentAnalysis :one
SELECT a.id, a.case_id, a.version, a.status, a.technical_retry_count,
       a.worker_attempt_id, a.worker_started_at, a.summary, a.facts,
       a.assumptions, a.unknowns, a.risk_analysis, a.compliance_analysis,
       a.recommendation, a.alternatives, a.missing_information,
       a.policy_status, a.evidence_quality, a.uncertainty,
       a.verification_status, a.verification_notes, a.model_name,
       a.prompt_version, a.created_at
FROM cases c
JOIN ai_analyses a ON a.id = c.current_analysis_id
WHERE c.id = $1;
