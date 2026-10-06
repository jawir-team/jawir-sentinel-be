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
