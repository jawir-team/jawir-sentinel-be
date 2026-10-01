# JAWIR Sentinel Backend

**Specification Version:** 2.0  
**Status:** MVP Implementation Baseline  
**Repository:** `jawir-sentinel-be`

Backend service untuk **JAWIR Sentinel**, sebuah AI-assisted governed decision workflow untuk financial operations.

Backend bertanggung jawab atas:

- business logic;
- workflow state machine;
- case lifecycle;
- SOP/policy management;
- evidence management;
- durable AI job orchestration;
- transactional outbox + RabbitMQ delivery;
- policy retrieval;
- analysis versioning;
- Checker/Signer decision flow;
- execution flow;
- audit trail;
- enforcement authentication dan authorization;
- backend deployment.

---

## 1. Prinsip Utama

> **AI generates intelligence. Backend enforces control. Humans hold authority. Data preserves accountability.**

Backend adalah sumber kebenaran untuk:

- workflow state;
- access control;
- segregation of duties;
- analysis version;
- approval validity;
- execution state;
- policy version;
- audit history.

AI dan frontend tidak dapat mengubah workflow state secara langsung.

---

## 2. Technology Stack

| Layer | Technology |
|---|---|
| Language | Golang |
| HTTP Router | Chi |
| Database | PostgreSQL |
| SQL Access | sqlc |
| Vector Search | pgvector |
| Database Service | Google Cloud SQL |
| AI | Gemini via Vertex AI |
| File Storage | Google Cloud Storage |
| Authentication | Firebase Authentication |
| Messaging | RabbitMQ quorum queue |
| Runtime API | Google Cloud Run Service |
| Runtime Worker | Google Cloud Run Worker Pool |
| CI/CD | GitHub Actions + Docker |

---

## 3. Struktur Repository

```text
jawir-sentinel-be/
├── cmd/
│   ├── api/
│   │   └── main.go
│   └── worker/
│       └── main.go
│
├── internal/
│   ├── unit/
│   ├── user/
│   ├── casetype/
│   ├── case/
│   ├── workflow/
│   ├── policy/
│   ├── evidence/
│   ├── analysis/
│   ├── review/
│   ├── execution/
│   ├── audit/
│   ├── auth/
│   ├── outbox/
│   ├── messaging/
│   └── ai/
│
├── db/
│   ├── migrations/
│   ├── queries/
│   └── seed/
│
├── ai/
│   ├── prompts/
│   ├── schemas/
│   └── evaluation/
│
├── testdata/
│   ├── cases/
│   ├── policies/
│   └── expected/
│
├── .github/
│   └── workflows/
│
├── Dockerfile
├── docker-compose.yml
├── Makefile
├── sqlc.yaml
├── .env.example
├── go.mod
├── go.sum
└── README.md
```

---

## 4. Tanggung Jawab Package

### `internal/unit`

- Unit Management
- unit lookup
- unit validation

### `internal/user`

- User Management
- user status
- Firebase identity mapping
- unit membership

### `internal/casetype`

- Case Type Management
- case type validation

### `internal/case`

- create case
- update draft
- submit case
- close case
- list/detail case
- participant assignment
- case ownership

### `internal/workflow`

- state machine
- workflow events
- transition guards
- segregation of duties
- transition validation

Semua perubahan status case wajib melalui package ini.

```text
internal/workflow/
├── states.go
├── events.go
├── guards.go
└── transition.go
```

### `internal/policy`

- policy CRUD
- policy versioning
- policy activation
- policy superseding
- policy chunking
- vector indexing
- policy retrieval metadata

### `internal/evidence`

- text evidence
- file metadata
- signed upload URL
- Cloud Storage integration
- evidence classification

### `internal/analysis`

- AI analysis lifecycle
- analysis versioning
- structured output persistence
- analysis references
- verifier result

### `internal/review`

- Checker decision
- Signer decision
- approval validation
- validasi stale analysis

### `internal/execution`

- execution start
- execution result
- blocked/failed result
- execution evidence
- DONE/re-analysis transition

### `internal/audit`

- audit event append-only
- case history
- audit serialization

### `internal/auth`

- Firebase ID Token verification
- authenticated user context
- system-role authorization
- case-role authorization middleware

### `internal/outbox`

- durable AI job intent
- outbox repository
- pending event dispatch
- publisher-confirm handling

### `internal/messaging`

- RabbitMQ connection/topology
- durable quorum queue declaration
- persistent publishing
- manual consumer acknowledgements
- message identity / redelivery handling

### `internal/ai`

- Vertex AI client
- RabbitMQ AI job consumer
- context builder
- policy retrieval orchestration
- Gemini analysis
- structured output validation
- verifier
- re-analysis orchestration

---

# 5. Workflow State Machine

Case status:

```text
DRAFT
SUBMITTED
AI_ANALYSIS
CHECKING
SIGNING
EXECUTION
DONE
CLOSED
ESCALATION_REQUIRED
```

## 5.1 Tabel Transisi

| State Saat Ini | Event | Next State |
|---|---|---|
| DRAFT | SUBMIT | SUBMITTED |
| SUBMITTED | START_ANALYSIS | AI_ANALYSIS |
| AI_ANALYSIS | ANALYSIS_SUCCESS | CHECKING |
| AI_ANALYSIS | ANALYSIS_FAILED | ESCALATION_REQUIRED |
| AI_ANALYSIS | REANALYSIS_LIMIT_REACHED | ESCALATION_REQUIRED |
| CHECKING | ALL_CHECKERS_APPROVED | SIGNING |
| CHECKING | CHECKER_REJECTED | AI_ANALYSIS |
| SIGNING | SIGNER_APPROVED | EXECUTION |
| SIGNING | SIGNER_REJECTED | AI_ANALYSIS |
| EXECUTION | EXECUTION_SUCCESS | DONE |
| EXECUTION | EXECUTION_BLOCKED | AI_ANALYSIS |
| EXECUTION | EXECUTION_FAILED | AI_ANALYSIS |

Tidak tersedia endpoint generic `change-status`.

---

# 6. Role Workflow

Role ditentukan per case:

```text
MAKER
CHECKER
SIGNER
EXECUTER
```

Segregation of duties:

```text
Maker ≠ Checker
Maker ≠ Signer
Maker ≠ Executer
Checker ≠ Signer
Checker ≠ Executer
Signer ≠ Executer
```

Setiap active user hanya boleh memiliki satu workflow role pada case yang sama.

System role `USER|ADMIN` terpisah dari workflow role. ADMIN tidak bypass SoD atau workflow authority.

Semua required Checker harus `APPROVE` sebelum workflow masuk ke `SIGNING`.

---

# 7. Desain Database

## 7.1 Gambaran ERD

```text
┌─────────────┐
│    units    │
└──────┬──────┘
       │ 1:N
       ▼
┌─────────────┐
│    users    │
└──────┬──────┘
       │
       │
       │                    ┌────────────────┐
       │                    │   case_types   │
       │                    └───────┬────────┘
       │                            │ 1:N
       │                            ▼
       │                    ┌────────────────┐
       └───────────────────►│     cases      │◄──────────────┐
                            └───────┬────────┘               │
                                    │                        │
                ┌───────────────────┼──────────────────┐     │
                │                   │                  │     │
                ▼                   ▼                  ▼     │
      ┌──────────────────┐  ┌──────────────┐  ┌────────────┐│
      │case_participants │  │case_evidences│  │ai_analyses ││
      └────────┬─────────┘  └───────┬──────┘  └─────┬──────┘│
               │                    │               │       │
               ▼                    │               ├───────────────┐
             users                  │               │               │
                                    │               ▼               ▼
                                    │      analysis_policy_refs  analysis_evidence_refs
                                    │               │               │
                                    │               ▼               ▼
                                    │         policy_versions   case_evidences
                                    │               │
                                    │               ▼
                                    │            policies
                                    │
                                    ├────────────────────────┐
                                    ▼                        ▼
                               decisions                executions
                                    │                        │
                                    ▼                        ▼
                                  users                    users

cases ───────────────────────────────► audit_events
```

---

## 7.2 Extension PostgreSQL

Migration awal harus mengaktifkan:

```sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;
```

UUID generation dapat menggunakan PostgreSQL UUID function atau application-generated UUID. Gunakan satu pendekatan secara konsisten.

---

## 7.3 `units`

```text
units
-----
id           UUID          PK
code         VARCHAR(50)   NOT NULL UNIQUE
name         VARCHAR(150)  NOT NULL
description  TEXT          NULL
created_at   TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at   TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Indexes:

```text
UNIQUE(code)
INDEX(name)
```

---

## 7.4 `users`

```text
users
-----
id            UUID          PK
unit_id        UUID          NOT NULL FK → units.id
firebase_uid   VARCHAR(128)  NOT NULL UNIQUE
name           VARCHAR(150)  NOT NULL
email          VARCHAR(255)  NOT NULL UNIQUE
status         VARCHAR(20)   NOT NULL DEFAULT 'ACTIVE'
system_role    VARCHAR(20)   NOT NULL DEFAULT 'USER'
created_at     TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at     TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Allowed status:

```text
ACTIVE
INACTIVE
```

Foreign key:

```text
unit_id → units.id
ON DELETE RESTRICT
```

Indexes:

```text
UNIQUE(firebase_uid)
UNIQUE(email)
INDEX(unit_id)
INDEX(status)
```

---

## 7.5 `case_types`

```text
case_types
----------
id           UUID          PK
code         VARCHAR(80)   NOT NULL UNIQUE
name         VARCHAR(150)  NOT NULL
description  TEXT          NULL
created_at   TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at   TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Indexes:

```text
UNIQUE(code)
INDEX(name)
```

---

## 7.6 `cases`

```text
cases
-----
id                   UUID          PK
case_number           VARCHAR(50)   NOT NULL UNIQUE
case_type_id          UUID          NOT NULL FK → case_types.id

title                 VARCHAR(255)  NOT NULL
description           TEXT          NOT NULL

urgency               VARCHAR(20)   NOT NULL
status                VARCHAR(40)   NOT NULL DEFAULT 'DRAFT'

created_by            UUID          NOT NULL FK → users.id
owner_id              UUID          NOT NULL FK → users.id

current_analysis_id   UUID          NULL FK → ai_analyses.id

closed_by             UUID          NULL FK → users.id
close_reason          TEXT          NULL
closed_at             TIMESTAMPTZ   NULL

created_at            TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at            TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Allowed urgency:

```text
LOW
MEDIUM
HIGH
CRITICAL
```

Allowed status:

```text
DRAFT
SUBMITTED
AI_ANALYSIS
CHECKING
SIGNING
EXECUTION
DONE
CLOSED
ESCALATION_REQUIRED
```

Foreign keys:

```text
case_type_id → case_types.id ON DELETE RESTRICT
created_by   → users.id      ON DELETE RESTRICT
owner_id     → users.id      ON DELETE SET NULL
closed_by    → users.id      ON DELETE SET NULL
```

Owner contract:

```text
created_by = owner_id = immutable Maker
```

No owner reassignment exists in MVP.

`current_analysis_id` FK ditambahkan setelah tabel `ai_analyses` dibuat.

Semantics:

```text
current_analysis_id
= latest COMPLETED PASS/PASS_WITH_WARNING analysis
  eligible for human review

FAILED/GENERATING attempts never replace this pointer.
Latest attempt is derived from highest ai_analyses.version.
```

Indexes:

```text
UNIQUE(case_number)
INDEX(case_type_id)
INDEX(status)
INDEX(urgency)
INDEX(created_by)
INDEX(owner_id)
INDEX(created_at DESC)
INDEX(status, created_at DESC)
```

---

## 7.7 `case_participants`

```text
case_participants
-----------------
id             UUID         PK
case_id        UUID         NOT NULL FK → cases.id
user_id        UUID         NOT NULL FK → users.id

role           VARCHAR(20)  NOT NULL
required       BOOLEAN      NOT NULL DEFAULT TRUE
status         VARCHAR(20)  NOT NULL DEFAULT 'ACTIVE'

assigned_by    UUID         NOT NULL FK → users.id
assigned_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
unassigned_at  TIMESTAMPTZ  NULL
```

Allowed role:

```text
MAKER
CHECKER
SIGNER
EXECUTER
```

Allowed status:

```text
ACTIVE
INACTIVE
```

Constraints:

```text
UNIQUE(case_id, user_id, role)
```

Cardinality indexes:

```sql
CREATE UNIQUE INDEX uq_case_active_maker
ON case_participants(case_id)
WHERE role = 'MAKER' AND status = 'ACTIVE';

CREATE UNIQUE INDEX uq_case_active_signer
ON case_participants(case_id)
WHERE role = 'SIGNER' AND status = 'ACTIVE';

CREATE UNIQUE INDEX uq_case_active_executer
ON case_participants(case_id)
WHERE role = 'EXECUTER' AND status = 'ACTIVE';

CREATE UNIQUE INDEX uq_case_one_active_role_per_user
ON case_participants(case_id, user_id)
WHERE status = 'ACTIVE';
```

Multiple active Checker diperbolehkan, tetapi satu user tidak boleh memiliki dua active role pada case yang sama.

Foreign keys:

```text
case_id     → cases.id ON DELETE CASCADE
user_id     → users.id ON DELETE RESTRICT
assigned_by → users.id ON DELETE RESTRICT
```

Indexes:

```text
INDEX(case_id)
INDEX(user_id)
INDEX(case_id, role)
INDEX(case_id, role, required, status)
INDEX(user_id, status)
```

Aturan bisnis:

```text
MAKER     exactly 1 active, creator, immutable
CHECKER   1..N active, at least 1 required at submit
SIGNER    exactly 1 active at submit
EXECUTER  exactly 1 active at submit

participant mutation only while case.status = DRAFT
same inactive user/role is reactivated in-place, not inserted again
participant set frozen after submit
```

---

## 7.8 `policies`

```text
policies
--------
id            UUID          PK
code          VARCHAR(80)   NOT NULL UNIQUE
title         VARCHAR(255)  NOT NULL
domain        VARCHAR(100)  NOT NULL
case_type_id  UUID          NULL FK → case_types.id
description   TEXT          NULL
created_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Foreign key:

```text
case_type_id → case_types.id ON DELETE SET NULL
```

Indexes:

```text
UNIQUE(code)
INDEX(domain)
INDEX(case_type_id)
```

---

## 7.9 `policy_versions`

```text
policy_versions
---------------
id               UUID          PK
policy_id        UUID          NOT NULL FK → policies.id
version          VARCHAR(30)   NOT NULL
status           VARCHAR(20)   NOT NULL DEFAULT 'DRAFT'
index_status     VARCHAR(20)   NOT NULL DEFAULT 'NOT_STARTED'
index_error      TEXT          NULL
index_attempt_id UUID          NULL
index_started_at TIMESTAMPTZ   NULL
indexed_at       TIMESTAMPTZ   NULL

content          TEXT          NOT NULL
file_path        TEXT          NULL

effective_from   TIMESTAMPTZ   NULL
effective_until  TIMESTAMPTZ   NULL

created_by       UUID          NOT NULL FK → users.id
approved_by      UUID          NULL FK → users.id

created_at       TIMESTAMPTZ   NOT NULL DEFAULT now()
approved_at      TIMESTAMPTZ   NULL
```

Allowed status:

```text
DRAFT
ACTIVE
SUPERSEDED
```

Allowed index status:

```text
NOT_STARTED
PROCESSING
READY
FAILED
```

Constraints:

```text
UNIQUE(policy_id, version)
```

Foreign keys:

```text
policy_id   → policies.id ON DELETE CASCADE
created_by  → users.id    ON DELETE RESTRICT
approved_by → users.id    ON DELETE SET NULL
```

Indexes:

```text
INDEX(policy_id)
INDEX(status)
INDEX(index_status)
INDEX(policy_id, status)
INDEX(policy_id, status, index_status)
INDEX(effective_from)
INDEX(effective_until)
```

Aturan bisnis:

```text
1 policy = maximum 1 ACTIVE version
ACTIVE version must have index_status = READY
activation target must be effective NOW
PROCESSING attempt is protected by index_attempt_id + index_started_at lease

authoritative retrieval requires:
status = ACTIVE
AND index_status = READY
AND effective window valid
```

Implementasikan melalui service transaction. Partial unique index dapat digunakan:

```sql
CREATE UNIQUE INDEX uq_policy_single_active
ON policy_versions(policy_id)
WHERE status = 'ACTIVE';
```

---

## 7.10 `policy_chunks`

```text
policy_chunks
-------------
id                 UUID          PK
policy_version_id  UUID          NOT NULL FK → policy_versions.id
section            VARCHAR(150)  NULL
chunk_index        INTEGER       NOT NULL
content            TEXT          NOT NULL
embedding          VECTOR(768)   NOT NULL
created_at         TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Foreign key:

```text
policy_version_id → policy_versions.id ON DELETE CASCADE
```

Constraints:

```text
UNIQUE(policy_version_id, chunk_index)
```

Indexes:

```text
INDEX(policy_version_id)
```

Vector index:

```sql
CREATE INDEX idx_policy_chunks_embedding_hnsw
ON policy_chunks
USING hnsw (embedding vector_cosine_ops);
```

Embedding contract:

```text
model                  = gemini-embedding-001
output dimensionality  = 768
document task          = RETRIEVAL_DOCUMENT
query task             = RETRIEVAL_QUERY
distance               = cosine
index                  = HNSW
```

---

## 7.11 `case_evidences`

```text
case_evidences
--------------
id              UUID          PK
case_id         UUID          NOT NULL FK → cases.id
source_type     VARCHAR(20)   NOT NULL
source_user_id  UUID          NULL FK → users.id
evidence_type   VARCHAR(30)   NOT NULL
title           VARCHAR(255)  NULL
content         TEXT          NULL
file_path       TEXT          NULL
mime_type       VARCHAR(100)  NULL
created_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Allowed source type:

```text
MAKER
CHECKER
SIGNER
EXECUTER
SYSTEM
```

Allowed evidence type:

```text
COMMENT
DOCUMENT
LOG
SCREENSHOT
REFERENCE
EXECUTION_RESULT
```

Foreign keys:

```text
case_id        → cases.id ON DELETE CASCADE
source_user_id → users.id ON DELETE SET NULL
```

Indexes:

```text
INDEX(case_id)
INDEX(source_type)
INDEX(evidence_type)
INDEX(case_id, created_at DESC)
```

---

## 7.12 `ai_analyses`

```text
ai_analyses
-----------
id                    UUID          PK
case_id               UUID          NOT NULL FK → cases.id
version               INTEGER       NOT NULL
status                VARCHAR(20)   NOT NULL
technical_retry_count INTEGER       NOT NULL DEFAULT 0
worker_attempt_id     UUID          NULL
worker_started_at     TIMESTAMPTZ   NULL

summary               TEXT          NULL

facts                 JSONB         NULL
assumptions           JSONB         NULL
unknowns              JSONB         NULL

risk_analysis         JSONB         NULL
compliance_analysis   JSONB         NULL

recommendation        JSONB         NULL
alternatives          JSONB         NULL
missing_information   JSONB         NULL

policy_status         VARCHAR(40)   NULL
evidence_quality      VARCHAR(20)   NULL
uncertainty           VARCHAR(20)   NULL

verification_status   VARCHAR(30)   NULL
verification_notes    JSONB         NULL

model_name            VARCHAR(100)  NOT NULL
prompt_version        VARCHAR(50)   NOT NULL

created_at            TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Allowed status:

```text
GENERATING
COMPLETED
FAILED
```

Allowed policy status:

```text
POLICY_FOUND
POLICY_PARTIAL
NO_POLICY_FOUND
INSUFFICIENT_EVIDENCE
POLICY_CONFLICT
```

Allowed verification status:

```text
PASS
PASS_WITH_WARNING
FAIL
```

Allowed evidence quality:

```text
LOW
MEDIUM
HIGH
```

Allowed uncertainty:

```text
LOW
MEDIUM
HIGH
```

Constraints:

```text
UNIQUE(case_id, version)
```

Foreign key:

```text
case_id → cases.id ON DELETE CASCADE
```

Indexes:

```text
INDEX(case_id)
INDEX(case_id, version DESC)
INDEX(status)
INDEX(policy_status)
INDEX(verification_status)
```

Setelah tabel ini tersedia:

```text
cases.current_analysis_id → ai_analyses.id ON DELETE SET NULL
```

---

## 7.13 `analysis_policy_refs`

```text
analysis_policy_refs
--------------------
id                 UUID          PK
analysis_id        UUID          NOT NULL FK → ai_analyses.id
policy_version_id  UUID          NOT NULL FK → policy_versions.id
section            VARCHAR(150)  NULL
excerpt            TEXT          NULL
relevance_score    NUMERIC(6,5)  NULL
created_at         TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Foreign keys:

```text
analysis_id       → ai_analyses.id      ON DELETE CASCADE
policy_version_id → policy_versions.id  ON DELETE RESTRICT
```

Indexes:

```text
INDEX(analysis_id)
INDEX(policy_version_id)
```

---

## 7.14 `analysis_evidence_refs`

```text
analysis_evidence_refs
----------------------
id           UUID         PK
analysis_id  UUID         NOT NULL FK → ai_analyses.id
evidence_id  UUID         NOT NULL FK → case_evidences.id
usage_type   VARCHAR(40)  NOT NULL
created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
```

Allowed usage type:

```text
SUPPORTING_FACT
CONTEXT
EXECUTION_FEEDBACK
REVIEW_FEEDBACK
```

Foreign keys:

```text
analysis_id → ai_analyses.id    ON DELETE CASCADE
evidence_id → case_evidences.id ON DELETE RESTRICT
```

Constraints:

```text
UNIQUE(analysis_id, evidence_id, usage_type)
```

Indexes:

```text
INDEX(analysis_id)
INDEX(evidence_id)
```

---

## 7.15 `decisions`

```text
decisions
---------
id           UUID         PK
case_id      UUID         NOT NULL FK → cases.id
analysis_id  UUID         NOT NULL FK → ai_analyses.id
actor_id     UUID         NOT NULL FK → users.id
actor_role   VARCHAR(20)  NOT NULL
decision     VARCHAR(20)  NOT NULL
reason       TEXT         NULL
comment      TEXT         NULL
created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
```

Allowed actor role:

```text
CHECKER
SIGNER
```

Allowed decision:

```text
APPROVE
REJECT
```

Constraints:

```text
UNIQUE(analysis_id, actor_id, actor_role)
```

Foreign keys:

```text
case_id     → cases.id       ON DELETE CASCADE
analysis_id → ai_analyses.id ON DELETE RESTRICT
actor_id    → users.id       ON DELETE RESTRICT
```

Indexes:

```text
INDEX(case_id)
INDEX(analysis_id)
INDEX(actor_id)
INDEX(analysis_id, actor_role)
```

---

## 7.16 `executions`

```text
executions
----------
id            UUID         PK
case_id       UUID         NOT NULL FK → cases.id
analysis_id   UUID         NOT NULL FK → ai_analyses.id
executer_id   UUID         NOT NULL FK → users.id
status        VARCHAR(20)  NOT NULL
action_taken  TEXT         NULL
result        TEXT         NULL
blocker       TEXT         NULL
started_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
completed_at  TIMESTAMPTZ  NULL
created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
```

Allowed status:

```text
IN_PROGRESS
SUCCESS
BLOCKED
FAILED
```

Foreign keys:

```text
case_id     → cases.id       ON DELETE CASCADE
analysis_id → ai_analyses.id ON DELETE RESTRICT
executer_id → users.id       ON DELETE RESTRICT
```

Constraint:

```text
UNIQUE(case_id, analysis_id)
```

Satu signed analysis hanya boleh membuat satu execution attempt.

Indexes:

```text
INDEX(case_id)
INDEX(analysis_id)
INDEX(executer_id)
INDEX(case_id, created_at DESC)
```

---

## 7.17 `outbox_events`

```text
outbox_events
-------------
id            UUID          PK
case_id       UUID          NOT NULL FK → cases.id
analysis_id   UUID          NOT NULL FK → ai_analyses.id
event_type    VARCHAR(60)   NOT NULL
payload       JSONB         NOT NULL DEFAULT '{}'
status        VARCHAR(20)   NOT NULL DEFAULT 'PENDING'
attempt_count INTEGER       NOT NULL DEFAULT 0
published_at  TIMESTAMPTZ   NULL
last_error    TEXT          NULL
created_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
updated_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Contract:

```text
event_type = AI_ANALYSIS_REQUESTED
status     = PENDING | PUBLISHED
UNIQUE(event_type, analysis_id)
```

Outbox row dibuat secara atomic bersama GENERATING analysis. Dispatcher mem-publish persistent message dengan `message_id=outbox.id`, menunggu publisher confirm, lalu menandai PUBLISHED.

Duplicate publish/redelivery diperbolehkan. Worker melakukan claim pada GENERATING analysis menggunakan `worker_attempt_id + worker_started_at`; finalization wajib cocok dengan claim saat ini. Duplicate delivery baru saat active non-stale claim masih berlaku menjadi no-op, sedangkan redelivery/stale-lease recovery memutar claim token agar pekerjaan lama kehilangan write authority.

---

## 7.18 `audit_events`

Satu append-only audit table digunakan dengan scope eksplisit.

```text
audit_events
------------
id                 UUID          PK
scope_type         VARCHAR(20)   NOT NULL

case_id            UUID          NULL FK → cases.id
policy_id          UUID          NULL FK → policies.id
policy_version_id  UUID          NULL FK → policy_versions.id

event_type         VARCHAR(60)   NOT NULL
actor_id           UUID          NULL FK → users.id
actor_role         VARCHAR(20)   NULL
analysis_id        UUID          NULL FK → ai_analyses.id

metadata           JSONB         NOT NULL DEFAULT '{}'
created_at         TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Allowed scope:

```text
CASE
POLICY
```

Scope rules:

```text
CASE:
- case_id required
- policy_id/policy_version_id null
- analysis_id optional
- actor_role workflow role or null for SYSTEM

POLICY:
- case_id null
- policy_id required
- policy_version_id optional
- analysis_id null
- actor_role null
```

Foreign keys:

```text
case_id           → cases.id           ON DELETE CASCADE
policy_id         → policies.id        ON DELETE RESTRICT
policy_version_id → policy_versions.id ON DELETE RESTRICT
actor_id          → users.id           ON DELETE SET NULL
analysis_id       → ai_analyses.id     ON DELETE SET NULL
```

Indexes:

```text
INDEX(scope_type)
INDEX(event_type)
INDEX(actor_id)
INDEX(case_id)
INDEX(case_id, created_at ASC)
INDEX(policy_id)
INDEX(policy_id, created_at ASC)
INDEX(policy_version_id)
```

Escalation metadata:

```text
AI_ANALYSIS_FAILED.failure_type =
  VERIFIER_FAIL
  | TECHNICAL_RETRY_EXHAUSTED

REANALYSIS_LIMIT_REACHED:
  latest_analysis_version
  max_reanalysis
```

Audit bersifat append-only. Policy event tidak pernah memakai fake `case_id`.
---

# 8. Aturan Mutation Database

## 8.1 Tidak Ada Hard Delete untuk Data Workflow

Tidak ada hard delete melalui application API untuk:

```text
cases
case_evidences
ai_analyses
decisions
executions
audit_events
policy_versions
```

State/history dipertahankan untuk auditability.

---

## 8.2 Batas Critical Transaction

AI job enqueue intent menjadi bagian dari DB transaction yang sama dengan workflow mutation.

Initial submit:

```text
BEGIN
validate + freeze case
DRAFT → SUBMITTED → AI_ANALYSIS
create analysis v1 GENERATING
audit CASE_SUBMITTED
audit AI_ANALYSIS_STARTED
insert outbox AI_ANALYSIS_REQUESTED
COMMIT
```

Governed reject/block/fail:

```text
BEGIN
persist business action
transition → AI_ANALYSIS
check MAX_REANALYSIS

quota available:
  create next GENERATING analysis
  audit AI_ANALYSIS_STARTED
  insert outbox AI_ANALYSIS_REQUESTED

quota exhausted:
  audit REANALYSIS_LIMIT_REACHED
  → ESCALATION_REQUIRED
  no analysis/outbox row

COMMIT
```

Tidak ada network call Vertex atau RabbitMQ di dalam business transaction.

---

# 9. Aturan Concurrency

Semua decision request membawa `analysis_id`.

Backend wajib memastikan:

```text
request.analysis_id == case.current_analysis_id
```

Jika berbeda:

```http
409 Conflict
```

Error:

```text
STALE_ANALYSIS
```

Ketika analysis version baru berhasil dibuat:

```text
cases.current_analysis_id = new_analysis_id
```

Approval analysis lama otomatis obsolete karena tetap terikat ke old `analysis_id`.

---

# 10. Policy Retrieval

Authoritative candidates:

```text
policy_versions.status = ACTIVE
AND policy_versions.index_status = READY
AND (effective_from <= now() OR effective_from IS NULL)
AND (effective_until > now() OR effective_until IS NULL)
AND (policies.case_type_id = requested_case_type_id OR policies.case_type_id IS NULL)
```

`policies.domain` is metadata only and is not a hard filter in MVP.

Retrieval pipeline:

```text
case_type_id + retrieval_text
  ↓
ACTIVE + READY + Effective filter
  ↓
matching case_type OR generic policy
  ↓
RETRIEVAL_QUERY embedding / 768
  ↓
HNSW cosine search
  ↓
TOP_K = 8
  ↓
chunks + provenance + distance/relevance
```

Tidak ada similarity threshold dan second reranker pada MVP.

---

# 11. Kontrak Analisis AI

Gemini analysis wajib menghasilkan structured JSON.

## 11.1 Output Analysis

```json
{
  "summary": "string",
  "facts": [
    {
      "statement": "string",
      "source_type": "CASE|EVIDENCE|POLICY",
      "source_ref": "string|null"
    }
  ],
  "assumptions": [
    {
      "statement": "string",
      "reason": "string"
    }
  ],
  "unknowns": [
    {
      "item": "string",
      "impact": "string"
    }
  ],
  "policy_status": "POLICY_FOUND|POLICY_PARTIAL|NO_POLICY_FOUND|INSUFFICIENT_EVIDENCE|POLICY_CONFLICT",
  "risk_analysis": [
    {
      "type": "OPERATIONAL|COMPLIANCE|FINANCIAL|OTHER",
      "level": "LOW|MEDIUM|HIGH|CRITICAL",
      "reason": "string",
      "evidence_refs": ["string"],
      "policy_refs": ["string"]
    }
  ],
  "compliance_analysis": {
    "status": "NO_ISSUE_IDENTIFIED|POTENTIAL_CONCERN|REQUIRES_REVIEW",
    "reason": "string",
    "policy_refs": ["string"]
  },
  "recommendation": {
    "type": "POLICY_BASED|NON_POLICY_RECOMMENDATION",
    "summary": "string",
    "actions": [
      {
        "order": 1,
        "action": "string",
        "reason": "string",
        "policy_refs": ["string"],
        "evidence_refs": ["string"]
      }
    ],
    "potential_benefits": ["string"],
    "potential_risks": ["string"]
  },
  "alternatives": [
    {
      "summary": "string",
      "benefits": ["string"],
      "risks": ["string"]
    }
  ],
  "missing_information": [
    {
      "item": "string",
      "why_needed": "string"
    }
  ],
  "evidence_quality": "LOW|MEDIUM|HIGH",
  "uncertainty": "LOW|MEDIUM|HIGH"
}
```

Backend wajib validate JSON sebelum persist sebagai completed analysis.

---

## 11.2 Output Verifier

```json
{
  "status": "PASS|PASS_WITH_WARNING|FAIL",
  "issues": [
    {
      "type": "UNSUPPORTED_CLAIM|HALLUCINATED_EVIDENCE|POLICY_CONTRADICTION|EVIDENCE_MISMATCH|MISSING_CRITICAL_INFORMATION|POLICY_CONFLICT|RECOMMENDATION_POLICY_MISMATCH",
      "severity": "WARNING|ERROR",
      "description": "string",
      "related_refs": ["string"]
    }
  ]
}
```

Behavior:

```text
PASS
  → persist complete schema-valid output as COMPLETED
  → transition CHECKING

PASS_WITH_WARNING
  → persist complete schema-valid output as COMPLETED
  → transition CHECKING

FAIL
  → persist FAILED
  → preserve schema-valid analysis fields already produced
  → persist verification_status = FAIL + verification_notes
  → do not update current_analysis_id
  → audit AI_ANALYSIS_FAILED / VERIFIER_FAIL
  → ANALYSIS_FAILED
  → ESCALATION_REQUIRED
```

Persistence semantics:

```text
NULL = field was not produced by a valid stage
[] / {} = valid output was produced and is empty
```

Jika provider/model/validation gagal sebelum valid structured analysis tersedia, result field tetap NULL. Jangan membuat placeholder business value.

---

# 12. Konteks Re-analysis AI

Re-analysis context terdiri dari:

```text
Current Case Snapshot
Current Evidence
Current ACTIVE + READY Policy Chunks
Latest Reviewer Feedback
Latest Execution Feedback
Previous Analysis Summary
Current Workflow State
```

Previous analysis tidak dianggap sumber kebenaran.

---

# 13. Batas Re-analysis

Config:

```text
MAX_REANALYSIS=3
```

Semantics:

```text
v1 = initial analysis, reanalysis_count 0
v2 = re-analysis #1
v3 = re-analysis #2
v4 = re-analysis #3
```

MVP formula:

```text
reanalysis_count = latest_analysis_version - 1
```

Jika governed business action membutuhkan analysis berikutnya setelah quota habis:

```text
Persist triggering reject/block/fail action
↓
Audit REANALYSIS_LIMIT_REACHED
↓
case.status = ESCALATION_REQUIRED
↓
Do not create a new analysis version
Do not call AI
```

Business endpoint tetap dianggap berhasil; response mengembalikan final `case_status = ESCALATION_REQUIRED`, bukan 409.

Technical Vertex/Gemini retry tidak membuat version baru dan tidak mengonsumsi quota re-analysis.

Technical retry budget berasal dari application config yang dibaca dari environment:

```env
AI_TECHNICAL_MAX_RETRIES=2
```

Nilainya adalah jumlah retry setelah initial attempt. Tidak ada angka retry yang di-hard-code pada orchestration layer. Jika budget habis, analysis menjadi FAILED, audit `AI_ANALYSIS_FAILED` ditulis dengan `failure_type=TECHNICAL_RETRY_EXHAUSTED`, event `ANALYSIS_FAILED` memindahkan case ke `ESCALATION_REQUIRED`. Verifier `FAIL` bukan technical retry condition; verifier FAIL juga terminal dan masuk `ESCALATION_REQUIRED` dengan `failure_type=VERIFIER_FAIL`.

---

# 14. Side Effect Workflow

## 14.1 Submit Case

```text
Validate + freeze governance context
↓
BEGIN
DRAFT → SUBMITTED → AI_ANALYSIS
Create v1 GENERATING
Audit CASE_SUBMITTED
Audit AI_ANALYSIS_STARTED
Insert PENDING outbox AI_ANALYSIS_REQUESTED
COMMIT
↓
API returns AI_ANALYSIS
↓
worker dispatches/consumes asynchronously through RabbitMQ
```

---

## 14.2 Analysis Berhasil

```text
Persist ai_analyses
Persist policy refs
Persist evidence refs
Update cases.current_analysis_id
Audit AI_ANALYSIS_COMPLETED
Transition AI_ANALYSIS → CHECKING
```

---

## 14.3 Checker Approve

```text
Validate CHECKING
Validate assigned Checker
Validate current analysis
↓
Insert APPROVE decision
Audit CHECKER_APPROVED
↓
If all required Checkers approved:
    Transition → SIGNING
Else:
    Remain CHECKING
```

---

## 14.4 Checker Reject

```text
Validate CHECKING
Validate assigned Checker
Validate current analysis
↓
Insert REJECT decision
Insert REVIEW_FEEDBACK evidence
Audit CHECKER_REJECTED
Transition → AI_ANALYSIS
Check MAX_REANALYSIS
↓
quota tersedia
→ Allocate next GENERATING analysis
→ Audit AI_ANALYSIS_STARTED
→ Insert PENDING outbox AI_ANALYSIS_REQUESTED
→ COMMIT
→ sentinel-worker mengirim job melalui RabbitMQ

quota habis
→ Audit REANALYSIS_LIMIT_REACHED
→ ESCALATION_REQUIRED
→ COMMIT
→ tidak membuat analysis/outbox baru
```

---

## 14.5 Signer Approve

```text
Validate SIGNING
Validate assigned Signer
Validate current analysis
Validate all required Checker approvals
↓
Insert APPROVE decision
Create decision snapshot in audit metadata
Audit SIGNER_APPROVED
Transition → EXECUTION
```

---

## 14.6 Signer Reject

```text
Validate SIGNING
Validate assigned Signer
Validate current analysis
↓
Insert REJECT decision
Insert REVIEW_FEEDBACK evidence
Audit SIGNER_REJECTED
Transition → AI_ANALYSIS
Check MAX_REANALYSIS
↓
quota tersedia
→ Allocate next GENERATING analysis
→ Audit AI_ANALYSIS_STARTED
→ Insert PENDING outbox AI_ANALYSIS_REQUESTED
→ COMMIT
→ sentinel-worker mengirim job melalui RabbitMQ

quota habis
→ Audit REANALYSIS_LIMIT_REACHED
→ ESCALATION_REQUIRED
→ COMMIT
→ tidak membuat analysis/outbox baru
```

---

## 14.7 Execution Berhasil

```text
Validate EXECUTION
Validate assigned Executer
Validate current signed analysis
↓
Update execution → SUCCESS
Audit EXECUTION_SUCCESS
Transition → DONE
Audit CASE_DONE
```

---

## 14.8 Execution Blocked / Failed

```text
Validate EXECUTION
Validate assigned Executer
↓
Update execution → BLOCKED / FAILED
Insert EXECUTION_RESULT evidence
Audit EXECUTION_BLOCKED / EXECUTION_FAILED
Transition → AI_ANALYSIS
Check MAX_REANALYSIS
↓
quota tersedia
→ Allocate next GENERATING analysis
→ Audit AI_ANALYSIS_STARTED
→ Insert PENDING outbox AI_ANALYSIS_REQUESTED
→ COMMIT
→ sentinel-worker mengirim job melalui RabbitMQ

quota habis
→ Audit REANALYSIS_LIMIT_REACHED
→ ESCALATION_REQUIRED
→ COMMIT
→ tidak membuat analysis/outbox baru
```

---

# 15. Authentication

Firebase Authentication digunakan sebagai identity provider.

Request:

```http
Authorization: Bearer <firebase-id-token>
```

Backend:

1. verify Firebase ID Token;
2. extract Firebase UID;
3. query `users.firebase_uid`;
4. validate `users.status = ACTIVE`;
5. attach internal user ke request context.

---

# 16. Authorization

Two independent role dimensions:

```text
System role:
USER | ADMIN

Case workflow role:
MAKER | CHECKER | SIGNER | EXECUTER
```

Authorization:

```text
safe master-data/user directory reads → any ACTIVE authenticated user
master-data/user writes              → ADMIN
policy create/version/activate        → ADMIN

case read                            → participant OR ADMIN
case workflow mutation               → exact assigned case role
ADMIN alone                           → no workflow-action authority
```

Frontend tidak menjadi security boundary.

---

# 17. Kontrak API

Base path:

```http
/api/v1
```

---

## 17.1 GET `/me`

Response:

```json
{
  "data": {
    "id": "uuid",
    "name": "Ferdian",
    "email": "ferdian@example.com",
    "unit": {
      "id": "uuid",
      "code": "OPS",
      "name": "Operations"
    },
    "system_role": "USER"
  }
}
```

---

## 17.2 POST `/cases`

Request:

```json
{
  "case_type_id": "uuid",
  "title": "Settlement reconciliation mismatch",
  "description": "37 transactions failed reconciliation.",
  "urgency": "HIGH"
}
```

Response:

```json
{
  "data": {
    "id": "uuid",
    "case_number": "CASE-2026-000001",
    "status": "DRAFT"
  }
}
```

---

## 17.3 POST `/cases/{case_id}/participants`

Request:

```json
{
  "user_id": "uuid",
  "role": "CHECKER",
  "required": true
}
```

Response:

```json
{
  "data": {
    "id": "uuid",
    "case_id": "uuid",
    "user_id": "uuid",
    "role": "CHECKER",
    "required": true,
    "status": "ACTIVE"
  }
}
```

---

## 17.4 POST `/cases/{case_id}/submit`

Request:

```json
{}
```

Response:

```json
{
  "data": {
    "id": "uuid",
    "status": "AI_ANALYSIS"
  }
}
```

---

## 17.5 GET `/cases/{case_id}`

Response:

```json
{
  "data": {
    "id": "uuid",
    "case_number": "CASE-2026-000001",
    "title": "Settlement reconciliation mismatch",
    "description": "37 transactions failed reconciliation.",
    "urgency": "HIGH",
    "status": "CHECKING",
    "case_type": {
      "id": "uuid",
      "code": "SETTLEMENT_EXCEPTION",
      "name": "Settlement Exception"
    },
    "maker": {
      "id": "uuid",
      "name": "Operations User"
    },
    "participants": [
      {
        "id": "uuid",
        "user_id": "uuid",
        "name": "Risk User",
        "role": "CHECKER",
        "required": true,
        "status": "ACTIVE"
      }
    ],
    "current_analysis": {
      "id": "uuid",
      "version": 2,
      "verification_status": "PASS"
    },
    "created_at": "2026-10-01T10:00:00Z"
  }
}
```

---

## 17.6 POST `/cases/{case_id}/evidences`

Request:

```json
{
  "evidence_type": "COMMENT",
  "title": "Additional context",
  "content": "Upstream batch arrived 20 minutes late."
}
```

---

## 17.7 GET `/cases/{case_id}/analyses/current`

Response:

```json
{
  "data": {
    "id": "uuid",
    "version": 2,
    "status": "COMPLETED",
    "summary": "string",
    "facts": [],
    "assumptions": [],
    "unknowns": [],
    "policy_status": "POLICY_PARTIAL",
    "risk_analysis": [],
    "compliance_analysis": {},
    "recommendation": {},
    "alternatives": [],
    "missing_information": [],
    "evidence_quality": "HIGH",
    "uncertainty": "MEDIUM",
    "verification": {
      "status": "PASS_WITH_WARNING",
      "issues": []
    },
    "created_at": "2026-10-01T10:01:00Z"
  }
}
```

---

## 17.8 POST `/cases/{case_id}/checker-decisions`

Approve:

```json
{
  "analysis_id": "uuid",
  "decision": "APPROVE",
  "comment": "Reasoning and evidence are acceptable."
}
```

Reject:

```json
{
  "analysis_id": "uuid",
  "decision": "REJECT",
  "reason": "Relevant SOP was not considered.",
  "comment": "Include SOP-RISK-004 before continuing.",
  "evidence_ids": ["uuid"]
}
```

---

## 17.9 GET `/cases/{case_id}/checker-status`

Response:

```json
{
  "data": {
    "analysis_id": "uuid",
    "required": 2,
    "approved": 1,
    "rejected": 0,
    "pending": 1,
    "checkers": [
      {
        "user_id": "uuid",
        "name": "Risk User",
        "status": "APPROVED"
      },
      {
        "user_id": "uuid",
        "name": "Dev User",
        "status": "PENDING"
      }
    ]
  }
}
```

---

## 17.10 POST `/cases/{case_id}/signer-decision`

Approve:

```json
{
  "analysis_id": "uuid",
  "decision": "APPROVE",
  "comment": "Authorized for execution."
}
```

Reject:

```json
{
  "analysis_id": "uuid",
  "decision": "REJECT",
  "reason": "Operational impact is too high.",
  "comment": "Provide a lower-impact alternative."
}
```

---

## 17.11 POST `/cases/{case_id}/executions`

Request:

```json
{
  "analysis_id": "uuid"
}
```

Response:

```json
{
  "data": {
    "id": "uuid",
    "status": "IN_PROGRESS"
  }
}
```

---

## 17.12 POST `/cases/{case_id}/executions/{execution_id}/result`

Success:

```json
{
  "status": "SUCCESS",
  "action_taken": "Affected transactions were isolated.",
  "result": "Reconciliation completed successfully."
}
```

Blocked:

```json
{
  "status": "BLOCKED",
  "action_taken": "Attempted reconciliation retry.",
  "blocker": "Upstream settlement file is unavailable."
}
```

Failed:

```json
{
  "status": "FAILED",
  "action_taken": "Triggered reconciliation retry.",
  "result": "Retry failed.",
  "blocker": "Dependency service unavailable."
}
```

---

## 17.13 POST `/policies`

Request:

```json
{
  "code": "SOP-OPS-001",
  "title": "Settlement Exception Handling",
  "domain": "SETTLEMENT",
  "case_type_id": "uuid",
  "description": "Operational procedure for settlement exceptions."
}
```

---

## 17.14 POST `/policies/{policy_id}/versions`

Request:

```json
{
  "version": "1.0",
  "content": "Policy content...",
  "effective_from": "2026-10-01T00:00:00Z",
  "effective_until": null
}
```

Created version status:

```text
DRAFT
```

---

## 17.15 POST `/policies/{policy_id}/versions/{version_id}/activate`

Behavior:

```text
validate target effective NOW
↓
claim index_attempt_id + index_started_at
target DRAFT → index_status PROCESSING
↓
policy chunks generated
embeddings generated
↓
target index_status → READY
↓
current ACTIVE version → SUPERSEDED
target DRAFT version → ACTIVE
audit events written
```

Chunking/embedding dilakukan sebelum final activation. Update READY/FAILED wajib memakai `index_attempt_id` saat ini. PROCESSING yang lebih lama dari `POLICY_INDEX_LEASE_SECONDS` dapat di-reclaim dengan attempt token baru; attempt lama yang terlambat tidak dapat finalize. Final activation memvalidasi ulang DRAFT + READY + effective NOW sebelum men-supercede ACTIVE saat ini. Aktivasi future/expired ditolak. External Vertex call tidak berada dalam open DB transaction.

---

# 18. Response API Standar

Success:

```json
{
  "data": {}
}
```

Error:

```json
{
  "error": {
    "code": "INVALID_STATE_TRANSITION",
    "message": "Case must be in CHECKING state.",
    "details": {}
  }
}
```

Pagination:

```json
{
  "data": [],
  "pagination": {
    "page": 1,
    "limit": 20,
    "total": 120
  }
}
```

---

# 19. Mapping Error

| Error Code | HTTP Status |
|---|---:|
| INVALID_REQUEST | 400 |
| UNAUTHORIZED | 401 |
| FORBIDDEN | 403 |
| SEGREGATION_OF_DUTIES_VIOLATION | 403 |
| USER_NOT_FOUND | 404 |
| CASE_NOT_FOUND | 404 |
| POLICY_NOT_FOUND | 404 |
| ANALYSIS_NOT_FOUND | 404 |
| INVALID_STATE_TRANSITION | 409 |
| STALE_ANALYSIS | 409 |
| POLICY_INDEXING_FAILED | 502 |
| INTERNAL_ERROR | 500 |

---

# 20. Katalog Event Audit

```text
CASE_CREATED
CASE_UPDATED
CASE_SUBMITTED
CASE_CLOSED
CASE_DONE

PARTICIPANT_ASSIGNED
PARTICIPANT_UNASSIGNED

EVIDENCE_ADDED

AI_ANALYSIS_STARTED
AI_ANALYSIS_COMPLETED
AI_ANALYSIS_FAILED
REANALYSIS_LIMIT_REACHED

CHECKER_APPROVED
CHECKER_REJECTED

SIGNER_APPROVED
SIGNER_REJECTED

EXECUTION_STARTED
EXECUTION_BLOCKED
EXECUTION_FAILED
EXECUTION_SUCCESS

POLICY_CREATED
POLICY_VERSION_CREATED
POLICY_ACTIVATED
POLICY_SUPERSEDED
```

---

# 21. File Upload

```text
Client requests signed upload URL
↓
Backend validates actor/state/MIME
↓
Client uploads directly to GCS
↓
Client registers file key
↓
Backend revalidates actor/state
↓
Verify case-scoped key + object exists + MIME
↓
Persist evidence
```

Supported MVP MIME:

```text
application/pdf
image/jpeg
image/png
```

`mime_type` yang tersimpan digunakan AI context builder. File yang didukung dikirim langsung ke Gemini menggunakan GCS URI `gs://`; tidak diperlukan custom OCR pipeline.

---

# 22. Environment Variable

```env
APP_ENV=development
APP_PORT=8080

DATABASE_URL=postgres://...

FIREBASE_PROJECT_ID=...

GCP_PROJECT_ID=...
GCP_REGION=...
GCS_BUCKET=...

VERTEX_AI_LOCATION=...
VERTEX_AI_MODEL=...
VERTEX_EMBEDDING_MODEL=...

MAX_REANALYSIS=3
AI_TECHNICAL_MAX_RETRIES=2
POLICY_RETRIEVAL_TOP_K=8
POLICY_INDEX_LEASE_SECONDS=900
AI_WORKER_LEASE_SECONDS=900

RABBITMQ_URL=amqps://...
RABBITMQ_AI_QUEUE=sentinel.ai.analysis
```

Credential Google Cloud menggunakan Application Default Credentials / service account.

Secret tidak disimpan di repository.

---

# 23. Seed Data

MVP seed minimal:

## Units

```text
OPS  → Operations
RISK → Risk Management
DEV  → Application Development
MGT  → Management
```

## Users

```text
ops.user@example.com      → Operations
risk.user@example.com     → Risk Management
dev.user@example.com      → Application Development
manager.user@example.com  → Management
```

## Case Types

```text
SETTLEMENT_EXCEPTION
PRODUCTION_INCIDENT
COMPLIANCE_EXCEPTION
OPERATIONAL_INCIDENT
```

## Policy Synthetic

Minimal:

```text
SOP-OPS-001 Settlement Exception Handling
SOP-RISK-001 Operational Risk Escalation
SOP-COMP-001 Evidence and Approval Requirement
```

## Case Demo

```text
Case Type:
SETTLEMENT_EXCEPTION

Title:
Settlement reconciliation mismatch

Facts:
- 37 unmatched transactions
- settlement cutoff in 90 minutes
- 8 transactions missing approval evidence
- downstream process is waiting
```

---

# 24. Matriks Pengujian

## 24.1 Workflow

```text
Draft → Submit
Analysis → Checking
Checker Approve
Checker Reject
All Checker Approve → Signing
Signer Approve
Signer Reject
Execution Success
Execution Blocked
Execution Failed
Close Case
```

## 24.2 Authorization

```text
Maker cannot Checker own case
Maker cannot Sign own case
Checker cannot Execute
Signer cannot Execute
Checker cannot Sign same case
Unassigned user cannot review
```

## 24.3 Analysis

```text
Policy Found
Policy Partial
No Policy
Insufficient Evidence
Policy Conflict
Verifier Warning
Verifier Failure
```

## 24.4 Concurrency

```text
Approve current analysis
Reject stale analysis
Double decision attempt
Old version approval after re-analysis
```

## 24.5 Policy

```text
Create draft
Activate first version
Activate new version
Previous version superseded
Only one active version
Retrieve active policy only
```

---

# 25. Logging

Structured logging fields:

```text
timestamp
level
request_id
case_id
user_id
component
message
```

AI invocation log:

```text
case_id
analysis_version
model_name
latency_ms
status
token_usage
```

Sensitive evidence content tidak ditulis ke application logs.

---

# 26. Development Lokal

Requirements:

```text
Go
Docker
PostgreSQL
pgvector
Google Cloud credentials
Firebase project configuration
```

Clone:

```bash
git clone <repository-url>
cd jawir-sentinel-be
```

Environment:

```bash
cp .env.example .env
```

Start dependencies:

```bash
docker compose up -d
```

Migration:

```bash
make migrate-up
```

Seed:

```bash
make seed
```

Generate sqlc:

```bash
make sqlc
```

Run:

```bash
make run
```

API:

```text
http://localhost:8080
```

---

# 27. Command Make

```bash
make run
make test
make test-integration
make lint

make migrate-up
make migrate-down
make seed

make sqlc

make docker-build
```

---

# 28. Migration Database

Migration:

```text
db/migrations/
```

Naming:

```text
000001_enable_extensions.up.sql
000001_enable_extensions.down.sql

000002_create_master_tables.up.sql
000002_create_master_tables.down.sql

000003_create_case_tables.up.sql
000003_create_case_tables.down.sql

000004_create_policy_tables.up.sql
000004_create_policy_tables.down.sql

000005_create_analysis_tables.up.sql
000005_create_analysis_tables.down.sql
```

Schema change selalu melalui migration.

---

# 29. Layout Query SQL

```text
db/queries/
├── unit.sql
├── user.sql
├── case_type.sql
├── case.sql
├── participant.sql
├── policy.sql
├── evidence.sql
├── analysis.sql
├── decision.sql
├── execution.sql
└── audit.sql
```

Generated sqlc code tidak diedit manual.

---

# 30. Deployment

Satu container image mendukung:

```text
cmd/api    → sentinel-api   → Cloud Run Service
cmd/worker → sentinel-worker → Cloud Run Worker Pool
```

Production dependencies:

```text
Cloud SQL PostgreSQL + pgvector
Cloud Storage
Vertex AI
Firebase Auth
RabbitMQ durable/fault-tolerant broker
```

RabbitMQ queue contract:

```text
durable quorum queue
persistent messages
publisher confirms
manual consumer acknowledgements
```

Docker Compose lokal mencakup PostgreSQL + pgvector + RabbitMQ.

---

# 31. Definition of Done Backend

Backend MVP dianggap selesai ketika flow berikut berjalan end-to-end melalui API:

```text
Create Case
↓
Assign Participants
↓
Submit Case
↓
Generate AI Analysis v1
↓
Checker Reject
↓
Generate AI Analysis v2
↓
All Checkers Approve
↓
Signer Approve
↓
Start Execution
↓
Execution BLOCKED
↓
Generate AI Analysis v3
↓
All Checkers Approve
↓
Signer Approve
↓
Execution SUCCESS
↓
Case DONE
```

Seluruh workflow harus:

- mengikuti state machine;
- menjaga segregation of duties;
- menjaga analysis versioning;
- menolak stale approval;
- mencatat audit history;
- menggunakan active policy;
- menjaga transaction integrity;
- dapat direkonstruksi dari persisted data.

---

# 32. Kontrak Dokumentasi

Product specification, ERD, state machine, dan API contract utama berada pada repository:

```text
jawir-sentinel-docs
```

Backend implementation mengikuti contract tersebut.

Perubahan API, workflow, atau data contract harus direvisi pada docs sebelum baseline backend berikutnya dibuat.
