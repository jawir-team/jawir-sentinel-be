# Synthetic AI evaluation

This package exercises the analysis prompt, structured candidate contract, and
grounding verifier with data about the fictional **Bank Nusantara Fiktif**.
Fixtures contain no customer or real-bank data. Expectations cover structured
properties only; generated prose is never compared.

## Scenario matrix

| Scenario | Policy status | Evidence quality / uncertainty | Verification |
|---|---|---|---|
| Complete policy and evidence | `POLICY_FOUND` | `HIGH` / `LOW` | `PASS` |
| Partially applicable policy | `POLICY_PARTIAL` | `MEDIUM` / `MEDIUM` | `PASS` |
| No applicable policy | `NO_POLICY_FOUND` | `HIGH` / `LOW` | `PASS_WITH_WARNING` |
| Missing required evidence | `INSUFFICIENT_EVIDENCE` | `LOW` / `HIGH` | `PASS_WITH_WARNING` |
| Conflicting policy chunks | `POLICY_CONFLICT` | `HIGH` / `LOW` | `PASS_WITH_WARNING` |
| Unsupported grounding | `POLICY_FOUND` | `HIGH` / `LOW` | `PASS_WITH_WARNING` |
| Hallucinated evidence reference | `POLICY_FOUND` | `HIGH` / `LOW` | `FAIL` |
| Policy-verdict contradiction | `POLICY_FOUND` | `HIGH` / `LOW` | `FAIL` |

The offline analyzer derives policy status from the scenario tag and derives
`evidence_quality` plus `uncertainty` from required evidence-type coverage. It
uses `ai.NewBuilder`, `ai.RenderPrompt`, candidate schema validation, and the
production `ai.VerifyCandidate` grounding rules. The dataset does not define or
use numeric AI confidence scores.

## Run

Run the deterministic suite (the normal CI path):

```sh
go test ./internal/aieval/...
```

Run the same fixtures against Vertex AI:

```sh
GCP_PROJECT_ID=your-project \
VERTEX_AI_LOCATION=your-location \
VERTEX_AI_MODEL=your-model \
EVAL_LIVE=1 \
go test -v ./internal/aieval -run TestLiveDataset
```

Live execution uses Application Default Credentials with the Cloud Platform
scope. It is informational: structured-property deviations are logged, not
failed, because model output can vary. Configuration, transport, and invalid
structured-output errors still fail the explicitly requested live run.
