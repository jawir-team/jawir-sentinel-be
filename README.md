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
- AI orchestration;
- policy retrieval;
- analysis versioning;
- Checker/Signer decision flow;
- execution flow;
- audit trail;
- authentication and authorization enforcement;
- backend deployment.

---

## 1. Core Principle

> **AI generates intelligence. Backend enforces control. Humans hold authority. Data preserves accountability.**

Backend adalah source of truth untuk:

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
| Runtime | Google Cloud Run |
| CI/CD | GitHub Actions + Docker |

---

## 3. Repository Structure

```text
jawir-sentinel-be/
├── cmd/
│   └── api/
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

## 4. Package Responsibility

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
- stale analysis validation

### `internal/execution`

- execution start
- execution result
- blocked/failed result
- execution evidence
- DONE/re-analysis transition

### `internal/audit`

- append-only audit event
- case history
- audit serialization

### `internal/auth`

- Firebase ID Token verification
- authenticated user context
- authorization middleware

### `internal/ai`

- Vertex AI client
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

## 5.1 Transition Table

| Current State | Event | Next State |
|---|---|---|
| DRAFT | SUBMIT | SUBMITTED |
| SUBMITTED | START_ANALYSIS | AI_ANALYSIS |
| AI_ANALYSIS | ANALYSIS_SUCCESS | CHECKING |
| AI_ANALYSIS | ANALYSIS_FAILED_LIMIT | ESCALATION_REQUIRED |
| CHECKING | ALL_CHECKERS_APPROVED | SIGNING |
| CHECKING | CHECKER_REJECTED | AI_ANALYSIS |
| SIGNING | SIGNER_APPROVED | EXECUTION |
| SIGNING | SIGNER_REJECTED | AI_ANALYSIS |
| EXECUTION | EXECUTION_SUCCESS | DONE |
| EXECUTION | EXECUTION_BLOCKED | AI_ANALYSIS |
| EXECUTION | EXECUTION_FAILED | AI_ANALYSIS |

Tidak tersedia endpoint generic `change-status`.

---

# 6. Workflow Roles

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
Checker ≠ Signer
Checker ≠ Executer
Signer ≠ Executer
Maker = Executer allowed
```

Semua required Checker harus `APPROVE` sebelum workflow masuk ke `SIGNING`.

---

# 7. Database Design

## 7.1 ERD Overview

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

## 7.2 PostgreSQL Extensions

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
is_admin       BOOLEAN       NOT NULL DEFAULT FALSE
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
owner_id              UUID          NULL FK → users.id

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

`current_analysis_id` FK ditambahkan setelah tabel `ai_analyses` dibuat.

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
INDEX(policy_id, status)
INDEX(effective_from, effective_until)
```

Business rule:

```text
1 policy = maximum 1 ACTIVE version
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
embedding          VECTOR        NOT NULL
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
VECTOR INDEX(embedding)
```

Vector dimension harus mengikuti embedding model yang digunakan.

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

summary               TEXT          NULL

facts                 JSONB         NOT NULL DEFAULT '[]'
assumptions           JSONB         NOT NULL DEFAULT '[]'
unknowns              JSONB         NOT NULL DEFAULT '[]'

risk_analysis         JSONB         NOT NULL DEFAULT '[]'
compliance_analysis   JSONB         NOT NULL DEFAULT '{}'

recommendation        JSONB         NOT NULL DEFAULT '{}'
alternatives          JSONB         NOT NULL DEFAULT '[]'
missing_information   JSONB         NOT NULL DEFAULT '[]'

policy_status         VARCHAR(40)   NOT NULL
evidence_quality      VARCHAR(20)   NOT NULL
uncertainty           VARCHAR(20)   NOT NULL

verification_status   VARCHAR(30)   NOT NULL
verification_notes    JSONB         NOT NULL DEFAULT '[]'

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

Indexes:

```text
INDEX(case_id)
INDEX(analysis_id)
INDEX(executer_id)
INDEX(case_id, created_at DESC)
```

---

## 7.17 `audit_events`

```text
audit_events
------------
id                UUID          PK
case_id           UUID          NOT NULL FK → cases.id
event_type        VARCHAR(60)   NOT NULL
actor_id          UUID          NULL FK → users.id
actor_role        VARCHAR(20)   NULL
analysis_id       UUID          NULL FK → ai_analyses.id
metadata          JSONB         NOT NULL DEFAULT '{}'
created_at        TIMESTAMPTZ   NOT NULL DEFAULT now()
```

Foreign keys:

```text
case_id     → cases.id       ON DELETE CASCADE
actor_id    → users.id       ON DELETE SET NULL
analysis_id → ai_analyses.id ON DELETE SET NULL
```

Indexes:

```text
INDEX(case_id)
INDEX(event_type)
INDEX(case_id, created_at ASC)
INDEX(actor_id)
```

Audit event bersifat append-only pada application layer.

---

# 8. Database Mutation Rules

## 8.1 No Hard Delete for Workflow Data

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

## 8.2 Critical Transaction Boundary

Workflow mutation harus atomic.

Contoh Checker reject:

```text
BEGIN

1. Validate current case state
2. Validate actor assignment
3. Validate current analysis ID
4. Insert decision(REJECT)
5. Insert review feedback evidence
6. Insert audit event
7. Transition case → AI_ANALYSIS

COMMIT

8. Trigger re-analysis
```

Gemini tidak dipanggil saat DB transaction masih terbuka.

---

# 9. Concurrency Rules

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

Hanya policy yang memenuhi seluruh kondisi berikut yang boleh masuk authoritative retrieval:

```text
policy_versions.status = ACTIVE
effective_from <= now() OR effective_from IS NULL
effective_until > now() OR effective_until IS NULL
```

Retrieval pipeline:

```text
Case Context
  ↓
Case Type / Domain Metadata Filter
  ↓
ACTIVE + Effective Policy Filter
  ↓
Embedding Query
  ↓
pgvector Similarity Search
  ↓
Top Relevant Policy Chunks
```

Retrieved chunk harus menyimpan:

```text
policy_id
policy_version_id
policy_code
version
section
content
relevance_score
```

---

# 11. AI Analysis Contract

Gemini analysis wajib menghasilkan structured JSON.

## 11.1 Analysis Output

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

## 11.2 Verifier Output

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
  → persist COMPLETED
  → transition CHECKING

PASS_WITH_WARNING
  → persist COMPLETED
  → transition CHECKING

FAIL
  → persist FAILED
  → no transition to CHECKING
```

---

# 12. AI Re-analysis Context

Re-analysis context terdiri dari:

```text
Current Case Snapshot
Current Evidence
Current ACTIVE Policy Chunks
Latest Reviewer Feedback
Latest Execution Feedback
Previous Analysis Summary
Current Workflow State
```

Previous analysis tidak dianggap source of truth.

---

# 13. Re-analysis Limit

Config:

```text
MAX_REANALYSIS=3
```

Jika limit tercapai:

```text
case.status = ESCALATION_REQUIRED
```

Audit event:

```text
REANALYSIS_LIMIT_REACHED
```

---

# 14. Workflow Side Effects

## 14.1 Submit Case

```text
Validate Maker
Validate DRAFT
Validate required participants
Validate segregation of duties
↓
Update case → SUBMITTED
Audit CASE_SUBMITTED
↓ COMMIT
Apply START_ANALYSIS
↓
AI_ANALYSIS
Trigger analysis
```

---

## 14.2 Analysis Success

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
↓ COMMIT
Trigger re-analysis
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
↓ COMMIT
Trigger re-analysis
```

---

## 14.7 Execution Success

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
↓ COMMIT
Trigger re-analysis
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

Authorization server-side berdasarkan:

```text
authenticated user
case participant assignment
case role
current state
current analysis version
segregation-of-duties rules
admin flag
```

Frontend tidak menjadi security boundary.

---

# 17. API Contract

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
    "is_admin": false
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
current ACTIVE version → SUPERSEDED
target DRAFT version → ACTIVE
policy chunks generated
embeddings generated
audit events written
```

Seluruh activation metadata update dilakukan dalam transaction. Chunking/embedding dilakukan setelah policy version berhasil diaktifkan; retrieval hanya menggunakan active version yang sudah memiliki index siap pakai.

---

# 18. Standard API Response

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

# 19. Error Mapping

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
| POLICY_CONFLICT | 409 |
| REANALYSIS_LIMIT_REACHED | 409 |
| AI_OUTPUT_INVALID | 502 |
| AI_ANALYSIS_FAILED | 502 |
| INTERNAL_ERROR | 500 |

---

# 20. Audit Event Catalog

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

Binary disimpan langsung ke Google Cloud Storage menggunakan signed URL.

Flow:

```text
Client
  ↓
POST upload-url
  ↓
Backend returns signed URL + file key
  ↓
Client uploads to Cloud Storage
  ↓
POST evidence/file
  ↓
Backend registers evidence metadata
```

Backend tidak menerima large binary melalui API utama.

---

# 22. Environment Variables

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
POLICY_RETRIEVAL_TOP_K=8
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

## Synthetic Policies

Minimal:

```text
SOP-OPS-001 Settlement Exception Handling
SOP-RISK-001 Operational Risk Escalation
SOP-COMP-001 Evidence and Approval Requirement
```

## Demo Case

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

# 24. Test Matrix

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

# 26. Local Development

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

# 27. Make Commands

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

# 28. Database Migration

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

# 29. SQL Query Layout

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

Pipeline:

```text
GitHub
  ↓
GitHub Actions
  ↓
Test
  ↓
Docker Build
  ↓
Push Image
  ↓
Cloud Run Deploy
```

Service:

```text
sentinel-api
```

Infrastructure:

```text
Cloud Run
Cloud SQL PostgreSQL
Cloud Storage
Vertex AI
Firebase Authentication
```

Environment:

```text
DEV
PROD
```

---

# 31. Backend Definition of Done

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

# 32. Documentation Contract

Product specification, ERD, state machine, dan API contract utama berada pada repository:

```text
jawir-sentinel-docs
```

Backend implementation mengikuti contract tersebut.

Perubahan API, workflow, atau data contract harus direvisi pada docs sebelum baseline backend berikutnya dibuat.
