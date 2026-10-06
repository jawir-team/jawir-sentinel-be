-- Roll back the complete JAWIR Sentinel MVP schema.

DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS executions;
DROP TABLE IF EXISTS decisions;
DROP TABLE IF EXISTS analysis_evidence_refs;
DROP TABLE IF EXISTS analysis_policy_refs;
ALTER TABLE IF EXISTS cases DROP CONSTRAINT IF EXISTS cases_current_analysis_fk;
DROP TABLE IF EXISTS ai_analyses;
DROP TABLE IF EXISTS case_evidences;
DROP TABLE IF EXISTS policy_chunks;
DROP TABLE IF EXISTS case_participants;
DROP TABLE IF EXISTS cases;
DROP TABLE IF EXISTS policy_versions;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS case_types;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS units;

DROP EXTENSION IF EXISTS vector;
DROP EXTENSION IF EXISTS "uuid-ossp";
