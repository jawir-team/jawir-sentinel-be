-- JAWIR Sentinel MVP schema.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE units (
    id UUID PRIMARY KEY,
    code VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_units_name ON units (name);

CREATE TABLE users (
    id UUID PRIMARY KEY,
    unit_id UUID NOT NULL REFERENCES units (id) ON DELETE RESTRICT,
    firebase_uid VARCHAR(128) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    system_role VARCHAR(20) NOT NULL DEFAULT 'USER',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_status_check CHECK (status IN ('ACTIVE', 'INACTIVE')),
    CONSTRAINT users_system_role_check CHECK (system_role IN ('USER', 'ADMIN'))
);

CREATE INDEX idx_users_unit_id ON users (unit_id);
CREATE INDEX idx_users_status ON users (status);
CREATE INDEX idx_users_system_role ON users (system_role);
CREATE INDEX idx_users_unit_status ON users (unit_id, status);

CREATE TABLE case_types (
    id UUID PRIMARY KEY,
    code VARCHAR(80) NOT NULL UNIQUE,
    name VARCHAR(150) NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_case_types_name ON case_types (name);

INSERT INTO case_types (id, code, name)
VALUES
    (uuid_generate_v4(), 'SETTLEMENT_EXCEPTION', 'Settlement Exception'),
    (uuid_generate_v4(), 'PRODUCTION_INCIDENT', 'Production Incident'),
    (uuid_generate_v4(), 'COMPLIANCE_EXCEPTION', 'Compliance Exception'),
    (uuid_generate_v4(), 'OPERATIONAL_INCIDENT', 'Operational Incident');

CREATE TABLE policies (
    id UUID PRIMARY KEY,
    code VARCHAR(80) NOT NULL UNIQUE,
    title VARCHAR(255) NOT NULL,
    domain VARCHAR(100) NOT NULL,
    case_type_id UUID REFERENCES case_types (id) ON DELETE SET NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_policies_domain ON policies (domain);
CREATE INDEX idx_policies_case_type_id ON policies (case_type_id);
CREATE INDEX idx_policies_domain_case_type ON policies (domain, case_type_id);

CREATE TABLE policy_versions (
    id UUID PRIMARY KEY,
    policy_id UUID NOT NULL REFERENCES policies (id) ON DELETE CASCADE,
    version VARCHAR(30) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'DRAFT',
    index_status VARCHAR(20) NOT NULL DEFAULT 'NOT_STARTED',
    index_error TEXT,
    index_attempt_id UUID,
    index_started_at TIMESTAMPTZ,
    indexed_at TIMESTAMPTZ,
    content TEXT NOT NULL,
    file_path TEXT,
    effective_from TIMESTAMPTZ,
    effective_until TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    approved_by UUID REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at TIMESTAMPTZ,
    CONSTRAINT policy_versions_version_unique UNIQUE (policy_id, version),
    CONSTRAINT policy_versions_id_policy_unique UNIQUE (id, policy_id),
    CONSTRAINT policy_versions_status_check CHECK (status IN ('DRAFT', 'ACTIVE', 'SUPERSEDED')),
    CONSTRAINT policy_versions_index_status_check CHECK (index_status IN ('NOT_STARTED', 'PROCESSING', 'READY', 'FAILED')),
    CONSTRAINT policy_versions_effective_range_check CHECK (
        effective_until IS NULL OR effective_from IS NULL OR effective_until > effective_from
    ),
    CONSTRAINT policy_versions_ready_fields_check CHECK (
        index_status <> 'READY' OR (indexed_at IS NOT NULL AND index_error IS NULL)
    ),
    CONSTRAINT policy_versions_processing_fields_check CHECK (
        index_status <> 'PROCESSING' OR (index_attempt_id IS NOT NULL AND index_started_at IS NOT NULL)
    ),
    CONSTRAINT policy_versions_failed_fields_check CHECK (
        index_status <> 'FAILED' OR (status = 'DRAFT' AND index_error IS NOT NULL)
    ),
    CONSTRAINT policy_versions_active_ready_check CHECK (
        status <> 'ACTIVE' OR index_status = 'READY'
    )
);

CREATE UNIQUE INDEX uq_policy_single_active
    ON policy_versions (policy_id)
    WHERE status = 'ACTIVE';
CREATE INDEX idx_policy_versions_policy_id ON policy_versions (policy_id);
CREATE INDEX idx_policy_versions_status ON policy_versions (status);
CREATE INDEX idx_policy_versions_index_status ON policy_versions (index_status);
CREATE INDEX idx_policy_versions_index_started_at ON policy_versions (index_started_at);
CREATE INDEX idx_policy_versions_policy_status ON policy_versions (policy_id, status);
CREATE INDEX idx_policy_versions_policy_status_index_status
    ON policy_versions (policy_id, status, index_status);
CREATE INDEX idx_policy_versions_effective_from ON policy_versions (effective_from);
CREATE INDEX idx_policy_versions_effective_until ON policy_versions (effective_until);

CREATE TABLE cases (
    id UUID PRIMARY KEY,
    case_number VARCHAR(50) NOT NULL UNIQUE,
    case_type_id UUID NOT NULL REFERENCES case_types (id) ON DELETE RESTRICT,
    title VARCHAR(255) NOT NULL,
    description TEXT NOT NULL,
    urgency VARCHAR(20) NOT NULL,
    status VARCHAR(40) NOT NULL DEFAULT 'DRAFT',
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    owner_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    current_analysis_id UUID,
    closed_by UUID REFERENCES users (id) ON DELETE SET NULL,
    close_reason TEXT,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT cases_urgency_check CHECK (urgency IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    CONSTRAINT cases_status_check CHECK (
        status IN (
            'DRAFT', 'SUBMITTED', 'AI_ANALYSIS', 'CHECKING', 'SIGNING',
            'EXECUTION', 'DONE', 'CLOSED', 'ESCALATION_REQUIRED'
        )
    ),
    CONSTRAINT cases_owner_created_by_check CHECK (owner_id = created_by),
    CONSTRAINT cases_closed_fields_check CHECK (
        status <> 'CLOSED' OR (
            closed_by IS NOT NULL AND close_reason IS NOT NULL AND closed_at IS NOT NULL
        )
    )
);

CREATE INDEX idx_cases_case_type_id ON cases (case_type_id);
CREATE INDEX idx_cases_status ON cases (status);
CREATE INDEX idx_cases_urgency ON cases (urgency);
CREATE INDEX idx_cases_created_by ON cases (created_by);
CREATE INDEX idx_cases_owner_id ON cases (owner_id);
CREATE INDEX idx_cases_created_at ON cases (created_at DESC);
CREATE INDEX idx_cases_status_created_at ON cases (status, created_at DESC);
CREATE INDEX idx_cases_case_type_status ON cases (case_type_id, status);

CREATE TABLE case_participants (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role VARCHAR(20) NOT NULL,
    required BOOLEAN NOT NULL DEFAULT TRUE,
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    assigned_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    unassigned_at TIMESTAMPTZ,
    CONSTRAINT case_participants_user_role_unique UNIQUE (case_id, user_id, role),
    CONSTRAINT case_participants_role_check CHECK (role IN ('MAKER', 'CHECKER', 'SIGNER', 'EXECUTER')),
    CONSTRAINT case_participants_status_check CHECK (status IN ('ACTIVE', 'INACTIVE'))
);

CREATE UNIQUE INDEX uq_case_active_maker
    ON case_participants (case_id)
    WHERE role = 'MAKER' AND status = 'ACTIVE';
CREATE UNIQUE INDEX uq_case_active_signer
    ON case_participants (case_id)
    WHERE role = 'SIGNER' AND status = 'ACTIVE';
CREATE UNIQUE INDEX uq_case_active_executer
    ON case_participants (case_id)
    WHERE role = 'EXECUTER' AND status = 'ACTIVE';
CREATE UNIQUE INDEX uq_case_one_active_role_per_user
    ON case_participants (case_id, user_id)
    WHERE status = 'ACTIVE';
CREATE INDEX idx_case_participants_case_id ON case_participants (case_id);
CREATE INDEX idx_case_participants_user_id ON case_participants (user_id);
CREATE INDEX idx_case_participants_case_role ON case_participants (case_id, role);
CREATE INDEX idx_case_participants_case_role_required_status
    ON case_participants (case_id, role, required, status);
CREATE INDEX idx_case_participants_user_status ON case_participants (user_id, status);

CREATE TABLE policy_chunks (
    id UUID PRIMARY KEY,
    policy_version_id UUID NOT NULL REFERENCES policy_versions (id) ON DELETE CASCADE,
    section VARCHAR(150),
    chunk_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding VECTOR(768) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT policy_chunks_version_index_unique UNIQUE (policy_version_id, chunk_index)
);

CREATE INDEX idx_policy_chunks_policy_version_id ON policy_chunks (policy_version_id);
CREATE INDEX idx_policy_chunks_embedding_hnsw
    ON policy_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE case_evidences (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    source_type VARCHAR(20) NOT NULL,
    source_user_id UUID REFERENCES users (id) ON DELETE SET NULL,
    evidence_type VARCHAR(30) NOT NULL,
    title VARCHAR(255),
    content TEXT,
    file_path TEXT,
    mime_type VARCHAR(100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT case_evidences_source_type_check CHECK (
        source_type IN ('MAKER', 'CHECKER', 'SIGNER', 'EXECUTER', 'SYSTEM')
    ),
    CONSTRAINT case_evidences_evidence_type_check CHECK (
        evidence_type IN ('COMMENT', 'DOCUMENT', 'LOG', 'SCREENSHOT', 'REFERENCE', 'EXECUTION_RESULT')
    ),
    CONSTRAINT case_evidences_content_or_file_check CHECK (content IS NOT NULL OR file_path IS NOT NULL),
    CONSTRAINT case_evidences_file_mime_check CHECK (file_path IS NULL OR mime_type IS NOT NULL),
    CONSTRAINT case_evidences_mime_type_check CHECK (
        mime_type IS NULL OR mime_type IN ('application/pdf', 'image/jpeg', 'image/png')
    )
);

CREATE INDEX idx_case_evidences_case_id ON case_evidences (case_id);
CREATE INDEX idx_case_evidences_source_type ON case_evidences (source_type);
CREATE INDEX idx_case_evidences_evidence_type ON case_evidences (evidence_type);
CREATE INDEX idx_case_evidences_case_created_at ON case_evidences (case_id, created_at DESC);

CREATE TABLE ai_analyses (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    status VARCHAR(20) NOT NULL,
    technical_retry_count INTEGER NOT NULL DEFAULT 0,
    worker_attempt_id UUID,
    worker_started_at TIMESTAMPTZ,
    summary TEXT,
    facts JSONB,
    assumptions JSONB,
    unknowns JSONB,
    risk_analysis JSONB,
    compliance_analysis JSONB,
    recommendation JSONB,
    alternatives JSONB,
    missing_information JSONB,
    policy_status VARCHAR(40),
    evidence_quality VARCHAR(20),
    uncertainty VARCHAR(20),
    verification_status VARCHAR(30),
    verification_notes JSONB,
    model_name VARCHAR(100) NOT NULL,
    prompt_version VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ai_analyses_case_version_unique UNIQUE (case_id, version),
    CONSTRAINT ai_analyses_id_case_unique UNIQUE (id, case_id),
    CONSTRAINT ai_analyses_status_check CHECK (status IN ('GENERATING', 'COMPLETED', 'FAILED')),
    CONSTRAINT ai_analyses_retry_count_check CHECK (technical_retry_count >= 0),
    CONSTRAINT ai_analyses_policy_status_check CHECK (
        policy_status IS NULL OR policy_status IN (
            'POLICY_FOUND', 'POLICY_PARTIAL', 'NO_POLICY_FOUND',
            'INSUFFICIENT_EVIDENCE', 'POLICY_CONFLICT'
        )
    ),
    CONSTRAINT ai_analyses_evidence_quality_check CHECK (
        evidence_quality IS NULL OR evidence_quality IN ('LOW', 'MEDIUM', 'HIGH')
    ),
    CONSTRAINT ai_analyses_uncertainty_check CHECK (
        uncertainty IS NULL OR uncertainty IN ('LOW', 'MEDIUM', 'HIGH')
    ),
    CONSTRAINT ai_analyses_verification_status_check CHECK (
        verification_status IS NULL OR verification_status IN ('PASS', 'PASS_WITH_WARNING', 'FAIL')
    ),
    CONSTRAINT ai_analyses_completed_fields_check CHECK (
        status <> 'COMPLETED' OR (
            summary IS NOT NULL AND facts IS NOT NULL AND assumptions IS NOT NULL
            AND unknowns IS NOT NULL AND risk_analysis IS NOT NULL
            AND compliance_analysis IS NOT NULL AND recommendation IS NOT NULL
            AND alternatives IS NOT NULL AND missing_information IS NOT NULL
            AND policy_status IS NOT NULL AND evidence_quality IS NOT NULL
            AND uncertainty IS NOT NULL
            AND verification_status IS NOT NULL
            AND verification_status IN ('PASS', 'PASS_WITH_WARNING')
            AND verification_notes IS NOT NULL
        )
    )
);

ALTER TABLE cases
    ADD CONSTRAINT cases_current_analysis_fk
    FOREIGN KEY (current_analysis_id) REFERENCES ai_analyses (id) ON DELETE SET NULL;

CREATE INDEX idx_ai_analyses_case_id ON ai_analyses (case_id);
CREATE INDEX idx_ai_analyses_case_version ON ai_analyses (case_id, version DESC);
CREATE INDEX idx_ai_analyses_status ON ai_analyses (status);
CREATE INDEX idx_ai_analyses_worker_started_at ON ai_analyses (worker_started_at);
CREATE INDEX idx_ai_analyses_policy_status ON ai_analyses (policy_status);
CREATE INDEX idx_ai_analyses_verification_status ON ai_analyses (verification_status);

CREATE TABLE analysis_policy_refs (
    id UUID PRIMARY KEY,
    analysis_id UUID NOT NULL REFERENCES ai_analyses (id) ON DELETE CASCADE,
    policy_version_id UUID NOT NULL REFERENCES policy_versions (id) ON DELETE RESTRICT,
    section VARCHAR(150),
    excerpt TEXT,
    relevance_score NUMERIC(6, 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT analysis_policy_refs_relevance_check CHECK (
        relevance_score IS NULL OR (relevance_score >= 0 AND relevance_score <= 1)
    )
);

CREATE INDEX idx_analysis_policy_refs_analysis_id ON analysis_policy_refs (analysis_id);
CREATE INDEX idx_analysis_policy_refs_policy_version_id ON analysis_policy_refs (policy_version_id);
CREATE INDEX idx_analysis_policy_refs_analysis_policy_version
    ON analysis_policy_refs (analysis_id, policy_version_id);

CREATE TABLE analysis_evidence_refs (
    id UUID PRIMARY KEY,
    analysis_id UUID NOT NULL REFERENCES ai_analyses (id) ON DELETE CASCADE,
    evidence_id UUID NOT NULL REFERENCES case_evidences (id) ON DELETE RESTRICT,
    usage_type VARCHAR(40) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT analysis_evidence_refs_unique UNIQUE (analysis_id, evidence_id, usage_type),
    CONSTRAINT analysis_evidence_refs_usage_type_check CHECK (
        usage_type IN ('SUPPORTING_FACT', 'CONTEXT', 'EXECUTION_FEEDBACK', 'REVIEW_FEEDBACK')
    )
);

CREATE INDEX idx_analysis_evidence_refs_analysis_id ON analysis_evidence_refs (analysis_id);
CREATE INDEX idx_analysis_evidence_refs_evidence_id ON analysis_evidence_refs (evidence_id);

CREATE TABLE decisions (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    analysis_id UUID NOT NULL REFERENCES ai_analyses (id) ON DELETE RESTRICT,
    actor_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    actor_role VARCHAR(20) NOT NULL,
    decision VARCHAR(20) NOT NULL,
    reason TEXT,
    comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT decisions_actor_role_check CHECK (actor_role IN ('CHECKER', 'SIGNER')),
    CONSTRAINT decisions_decision_check CHECK (decision IN ('APPROVE', 'REJECT')),
    CONSTRAINT decisions_unique UNIQUE (analysis_id, actor_id, actor_role)
);

CREATE INDEX idx_decisions_case_id ON decisions (case_id);
CREATE INDEX idx_decisions_analysis_id ON decisions (analysis_id);
CREATE INDEX idx_decisions_actor_id ON decisions (actor_id);
CREATE INDEX idx_decisions_analysis_role ON decisions (analysis_id, actor_role);
CREATE INDEX idx_decisions_case_analysis ON decisions (case_id, analysis_id);

CREATE TABLE executions (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    analysis_id UUID NOT NULL REFERENCES ai_analyses (id) ON DELETE RESTRICT,
    executer_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    status VARCHAR(20) NOT NULL,
    action_taken TEXT,
    result TEXT,
    blocker TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT executions_unique UNIQUE (case_id, analysis_id),
    CONSTRAINT executions_status_check CHECK (status IN ('IN_PROGRESS', 'SUCCESS', 'BLOCKED', 'FAILED')),
    CONSTRAINT executions_success_fields_check CHECK (
        status <> 'SUCCESS' OR (action_taken IS NOT NULL AND result IS NOT NULL AND completed_at IS NOT NULL)
    ),
    CONSTRAINT executions_blocked_fields_check CHECK (
        status <> 'BLOCKED' OR (blocker IS NOT NULL AND completed_at IS NOT NULL)
    ),
    CONSTRAINT executions_failed_fields_check CHECK (
        status <> 'FAILED' OR (
            action_taken IS NOT NULL AND result IS NOT NULL AND blocker IS NOT NULL AND completed_at IS NOT NULL
        )
    )
);

CREATE INDEX idx_executions_case_id ON executions (case_id);
CREATE INDEX idx_executions_analysis_id ON executions (analysis_id);
CREATE INDEX idx_executions_executer_id ON executions (executer_id);
CREATE INDEX idx_executions_status ON executions (status);
CREATE INDEX idx_executions_case_created_at ON executions (case_id, created_at DESC);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES cases (id) ON DELETE CASCADE,
    analysis_id UUID NOT NULL REFERENCES ai_analyses (id) ON DELETE CASCADE,
    event_type VARCHAR(60) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT outbox_events_event_analysis_unique UNIQUE (event_type, analysis_id),
    CONSTRAINT outbox_events_event_type_check CHECK (event_type IN ('AI_ANALYSIS_REQUESTED')),
    CONSTRAINT outbox_events_status_check CHECK (status IN ('PENDING', 'PUBLISHED')),
    CONSTRAINT outbox_events_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT outbox_events_published_fields_check CHECK (
        status <> 'PUBLISHED' OR published_at IS NOT NULL
    )
);

CREATE INDEX idx_outbox_events_pending_dispatch
    ON outbox_events (created_at, id)
    WHERE status = 'PENDING';
CREATE INDEX idx_outbox_events_status_created_at ON outbox_events (status, created_at);
CREATE INDEX idx_outbox_events_case_id ON outbox_events (case_id);
CREATE INDEX idx_outbox_events_analysis_id ON outbox_events (analysis_id);

CREATE TABLE audit_events (
    id UUID PRIMARY KEY,
    scope_type VARCHAR(20) NOT NULL,
    case_id UUID REFERENCES cases (id) ON DELETE CASCADE,
    policy_id UUID REFERENCES policies (id) ON DELETE RESTRICT,
    policy_version_id UUID,
    event_type VARCHAR(60) NOT NULL,
    actor_id UUID REFERENCES users (id) ON DELETE SET NULL,
    actor_role VARCHAR(20),
    analysis_id UUID REFERENCES ai_analyses (id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_scope_type_check CHECK (scope_type IN ('CASE', 'POLICY')),
    CONSTRAINT audit_events_scope_check CHECK (
        (
            scope_type = 'CASE'
            AND case_id IS NOT NULL
            AND policy_id IS NULL
            AND policy_version_id IS NULL
        )
        OR (
            scope_type = 'POLICY'
            AND case_id IS NULL
            AND policy_id IS NOT NULL
            AND analysis_id IS NULL
            AND actor_role IS NULL
        )
    ),
    CONSTRAINT audit_events_actor_role_check CHECK (
        actor_role IS NULL OR actor_role IN ('MAKER', 'CHECKER', 'SIGNER', 'EXECUTER')
    ),
    CONSTRAINT audit_events_policy_version_fk
        FOREIGN KEY (policy_version_id, policy_id)
        REFERENCES policy_versions (id, policy_id)
        ON DELETE RESTRICT
);

CREATE INDEX idx_audit_events_scope_type ON audit_events (scope_type);
CREATE INDEX idx_audit_events_event_type ON audit_events (event_type);
CREATE INDEX idx_audit_events_actor_id ON audit_events (actor_id);
CREATE INDEX idx_audit_events_case_id ON audit_events (case_id);
CREATE INDEX idx_audit_events_case_created_at ON audit_events (case_id, created_at ASC);
CREATE INDEX idx_audit_events_policy_id ON audit_events (policy_id);
CREATE INDEX idx_audit_events_policy_created_at ON audit_events (policy_id, created_at ASC);
CREATE INDEX idx_audit_events_policy_version_id ON audit_events (policy_version_id);
