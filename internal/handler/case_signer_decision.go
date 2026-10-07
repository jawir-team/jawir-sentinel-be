package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/workflow"
)

const (
	signerEventApproved = "SIGNER_APPROVED"
	signerEventRejected = "SIGNER_REJECTED"
)

var signerDecisionConfig = reviewerDecisionConfig{
	actorRole:                   caseRoleSigner,
	requiredState:               workflow.StateSigning,
	approvedEvent:               signerEventApproved,
	rejectedEvent:               signerEventRejected,
	feedbackTitle:               "Signer rejection feedback",
	duplicateActorLabel:         "Signer",
	requireApprovedCheckerRound: true,
	unassignedErrorCode:         httpapi.CodeForbidden,
}

type SignerDecider struct {
	Store      CheckerDecisionStore
	Reanalysis ReanalysisOrchestrator
}

// RecordSignerDecision records the assigned signer's decision for the current
// analysis. Approval advances to execution; rejection atomically persists
// feedback and delegates the re-analysis or escalation outcome.
func RecordSignerDecision(decider SignerDecider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.NewError(httpapi.CodeUnauthorized, "", nil))
			return
		}
		if decider.Store == nil || isNilInterface(decider.Store) || decider.Reanalysis == nil || isNilInterface(decider.Reanalysis) {
			logging.With(r.Context()).Error("record signer decision: dependencies are not configured")
			httpapi.WriteError(w, nil)
			return
		}

		caseID, err := parseUserUUID(chi.URLParam(r, "id"))
		if err != nil {
			writeInvalidCaseRequest(w, "A valid case ID is required.")
			return
		}
		request, apiErr := parseReviewerDecisionRequest(w, r)
		if apiErr != nil {
			httpapi.WriteError(w, apiErr)
			return
		}

		var decision db.Decision
		var caseStatus string
		if request.decision == checkerDecisionApprove {
			apiErr = recordSignerApproval(r.Context(), decider.Store, caseID, actor, request, &decision, &caseStatus)
		} else {
			apiErr = recordReviewerRejection(r.Context(), decider.Reanalysis, caseID, actor, request, workflow.EventSignerRejected, signerDecisionConfig, &decision, &caseStatus)
		}
		if apiErr != nil {
			if apiErr.Code == httpapi.CodeInternalError {
				logging.With(r.Context()).Error("record signer decision", "case_id", caseID.String(), "error", apiErr)
			}
			httpapi.WriteError(w, apiErr)
			return
		}

		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: reviewerDecisionResponse{
			DecisionID: decision.ID.String(),
			Decision:   decision.Decision,
			CaseStatus: caseStatus,
		}})
	}
}

func recordSignerApproval(
	ctx context.Context,
	store CheckerDecisionStore,
	caseID pgtype.UUID,
	actor auth.User,
	request parsedReviewerDecision,
	decision *db.Decision,
	caseStatus *string,
) *httpapi.APIError {
	var apiErr *httpapi.APIError
	txErr := store.RunCheckerDecisionTx(ctx, func(ctx context.Context, q CheckerDecisionTxQueries) error {
		var stored db.Case
		var participants []db.CaseParticipant
		var decisions []db.Decision
		stored, participants, decisions, *decision, apiErr = persistReviewerDecision(ctx, q, caseID, actor, request, signerDecisionConfig)
		if apiErr != nil {
			return apiErr
		}
		if apiErr = appendSignerApprovalAudit(ctx, q, stored, participants, decisions, *decision, actor, request); apiErr != nil {
			return apiErr
		}

		next, err := workflow.Transition(workflow.State(stored.Status), workflow.EventSignerApproved)
		if workflow.IsInvalidTransition(err) {
			apiErr = participantAPIError(httpapi.CodeInvalidStateTransition, "", err)
			return apiErr
		}
		if err != nil {
			apiErr = participantInternalError(err)
			return apiErr
		}
		updated, err := q.UpdateCaseStatus(ctx, db.UpdateCaseStatusParams{ID: caseID, Status: string(next)})
		if err != nil {
			apiErr = participantInternalError(err)
			return apiErr
		}
		*caseStatus = updated.Status
		return nil
	})
	if apiErr != nil {
		return apiErr
	}
	if txErr != nil {
		var wrapped *httpapi.APIError
		if errors.As(txErr, &wrapped) {
			return wrapped
		}
		return participantInternalError(txErr)
	}
	return nil
}

type signerApprovalAuditMetadata struct {
	AnalysisID       string                 `json:"analysis_id"`
	Decision         string                 `json:"decision"`
	Reason           string                 `json:"reason"`
	Comment          string                 `json:"comment"`
	EvidenceIDs      []string               `json:"evidence_ids"`
	DecisionSnapshot signerDecisionSnapshot `json:"decision_snapshot"`
}

type signerDecisionSnapshot struct {
	Case               signerCaseSnapshot             `json:"case"`
	Analysis           signerAnalysisSnapshot         `json:"analysis"`
	PolicyVersions     []signerPolicyVersionSnapshot  `json:"policy_versions"`
	EvidenceReferences []signerEvidenceRefSnapshot    `json:"evidence_references"`
	CitedEvidenceIDs   []string                       `json:"cited_evidence_ids"`
	CheckerApprovals   signerCheckerApprovalsSnapshot `json:"checker_approvals"`
	Signer             signerActorSnapshot            `json:"signer"`
	Recommendation     json.RawMessage                `json:"recommendation"`
	ApprovedAt         time.Time                      `json:"approved_at"`
}

type signerCaseSnapshot struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CaseTypeID   string `json:"case_type_id"`
	CaseTypeCode string `json:"case_type_code"`
	CaseTypeName string `json:"case_type_name"`
	Status       string `json:"status"`
}

type signerAnalysisSnapshot struct {
	ID      string `json:"id"`
	Version int32  `json:"version"`
}

type signerPolicyVersionSnapshot struct {
	AnalysisPolicyRefID string   `json:"analysis_policy_ref_id"`
	PolicyID            string   `json:"policy_id"`
	PolicyVersionID     string   `json:"policy_version_id"`
	Version             string   `json:"version"`
	Status              string   `json:"status"`
	Section             *string  `json:"section"`
	Excerpt             *string  `json:"excerpt"`
	RelevanceScore      *float64 `json:"relevance_score"`
}

type signerEvidenceRefSnapshot struct {
	AnalysisEvidenceRefID string `json:"analysis_evidence_ref_id"`
	EvidenceID            string `json:"evidence_id"`
	UsageType             string `json:"usage_type"`
}

type signerCheckerApprovalsSnapshot struct {
	RequiredCount int                             `json:"required_count"`
	ApprovedCount int                             `json:"approved_count"`
	Approvals     []signerCheckerApprovalSnapshot `json:"approvals"`
}

type signerCheckerApprovalSnapshot struct {
	ActorID    string    `json:"actor_id"`
	Role       string    `json:"role"`
	DecisionID string    `json:"decision_id"`
	Decision   string    `json:"decision"`
	DecidedAt  time.Time `json:"decided_at"`
}

type signerActorSnapshot struct {
	ActorID string `json:"actor_id"`
	Role    string `json:"role"`
}

func appendSignerApprovalAudit(
	ctx context.Context,
	q DecisionTxQueries,
	stored db.Case,
	participants []db.CaseParticipant,
	decisions []db.Decision,
	decision db.Decision,
	actor auth.User,
	request parsedReviewerDecision,
) *httpapi.APIError {
	snapshot, err := buildSignerDecisionSnapshot(ctx, q, stored, participants, decisions, decision, actor, request)
	if err != nil {
		return participantInternalError(err)
	}
	evidenceIDs := make([]string, 0, len(request.evidenceIDs))
	for _, id := range request.evidenceIDs {
		evidenceIDs = append(evidenceIDs, id.String())
	}
	metadata, err := json.Marshal(signerApprovalAuditMetadata{
		AnalysisID:       request.analysisID.String(),
		Decision:         request.decision,
		Reason:           request.reason,
		Comment:          request.comment,
		EvidenceIDs:      evidenceIDs,
		DecisionSnapshot: snapshot,
	})
	if err != nil {
		return participantInternalError(err)
	}
	auditID, err := newUnitUUID()
	if err != nil {
		return participantInternalError(err)
	}
	if _, err := q.AppendCaseAuditEvent(ctx, db.AppendCaseAuditEventParams{
		ID:         auditID,
		CaseID:     stored.ID,
		EventType:  signerEventApproved,
		ActorID:    actor.ID,
		ActorRole:  nullableWorkflowActorRole(caseRoleSigner),
		AnalysisID: request.analysisID,
		Metadata:   metadata,
	}); err != nil {
		return participantInternalError(err)
	}
	return nil
}

func buildSignerDecisionSnapshot(
	ctx context.Context,
	q DecisionTxQueries,
	stored db.Case,
	participants []db.CaseParticipant,
	decisions []db.Decision,
	decision db.Decision,
	actor auth.User,
	request parsedReviewerDecision,
) (signerDecisionSnapshot, error) {
	caseType, err := q.GetCaseType(ctx, stored.CaseTypeID)
	if err != nil {
		return signerDecisionSnapshot{}, fmt.Errorf("get case type for signer decision snapshot: %w", err)
	}
	analysis, err := q.GetAnalysis(ctx, request.analysisID)
	if err != nil {
		return signerDecisionSnapshot{}, fmt.Errorf("get analysis for signer decision snapshot: %w", err)
	}
	if analysis.ID != request.analysisID || analysis.CaseID != stored.ID {
		return signerDecisionSnapshot{}, errors.New("analysis for signer decision snapshot does not belong to case")
	}

	policyRefs, err := q.ListAnalysisPolicyRefs(ctx, request.analysisID)
	if err != nil {
		return signerDecisionSnapshot{}, fmt.Errorf("list policy references for signer decision snapshot: %w", err)
	}
	policyVersions := make([]signerPolicyVersionSnapshot, 0, len(policyRefs))
	for _, ref := range policyRefs {
		if ref.AnalysisID != request.analysisID {
			continue
		}
		version, err := q.GetPolicyVersion(ctx, ref.PolicyVersionID)
		if err != nil {
			return signerDecisionSnapshot{}, fmt.Errorf("get referenced policy version for signer decision snapshot: %w", err)
		}
		if version.ID != ref.PolicyVersionID {
			return signerDecisionSnapshot{}, errors.New("policy version for signer decision snapshot does not match reference")
		}
		relevance, err := signerSnapshotNumeric(ref.RelevanceScore)
		if err != nil {
			return signerDecisionSnapshot{}, fmt.Errorf("convert policy relevance for signer decision snapshot: %w", err)
		}
		policyVersions = append(policyVersions, signerPolicyVersionSnapshot{
			AnalysisPolicyRefID: ref.ID.String(),
			PolicyID:            version.PolicyID.String(),
			PolicyVersionID:     ref.PolicyVersionID.String(),
			Version:             version.Version,
			Status:              version.Status,
			Section:             signerSnapshotText(ref.Section),
			Excerpt:             signerSnapshotText(ref.Excerpt),
			RelevanceScore:      relevance,
		})
	}

	evidenceRefs, err := q.ListAnalysisEvidenceRefs(ctx, request.analysisID)
	if err != nil {
		return signerDecisionSnapshot{}, fmt.Errorf("list evidence references for signer decision snapshot: %w", err)
	}
	evidenceReferences := make([]signerEvidenceRefSnapshot, 0, len(evidenceRefs))
	for _, ref := range evidenceRefs {
		if ref.AnalysisID != request.analysisID {
			continue
		}
		evidenceReferences = append(evidenceReferences, signerEvidenceRefSnapshot{
			AnalysisEvidenceRefID: ref.ID.String(),
			EvidenceID:            ref.EvidenceID.String(),
			UsageType:             ref.UsageType,
		})
	}

	checkerRound := summarizeCheckerRound(request.analysisID, participants, decisions)
	checkerApprovals := make([]signerCheckerApprovalSnapshot, 0, checkerRound.Approved)
	for _, checkerDecision := range decisions {
		if checkerDecision.AnalysisID != request.analysisID || checkerDecision.ActorRole != caseRoleChecker {
			continue
		}
		checkerApprovals = append(checkerApprovals, signerCheckerApprovalSnapshot{
			ActorID:    checkerDecision.ActorID.String(),
			Role:       checkerDecision.ActorRole,
			DecisionID: checkerDecision.ID.String(),
			Decision:   checkerDecision.Decision,
			DecidedAt:  checkerDecision.CreatedAt,
		})
	}

	citedEvidenceIDs := make([]string, 0, len(request.evidenceIDs))
	for _, id := range request.evidenceIDs {
		citedEvidenceIDs = append(citedEvidenceIDs, id.String())
	}
	recommendation := json.RawMessage("null")
	if len(analysis.Recommendation) > 0 {
		recommendation = append(json.RawMessage(nil), analysis.Recommendation...)
	}
	return signerDecisionSnapshot{
		Case: signerCaseSnapshot{
			ID:           stored.ID.String(),
			Title:        stored.Title,
			CaseTypeID:   stored.CaseTypeID.String(),
			CaseTypeCode: caseType.Code,
			CaseTypeName: caseType.Name,
			Status:       stored.Status,
		},
		Analysis:           signerAnalysisSnapshot{ID: analysis.ID.String(), Version: analysis.Version},
		PolicyVersions:     policyVersions,
		EvidenceReferences: evidenceReferences,
		CitedEvidenceIDs:   citedEvidenceIDs,
		CheckerApprovals: signerCheckerApprovalsSnapshot{
			RequiredCount: checkerRound.Required,
			ApprovedCount: checkerRound.Approved,
			Approvals:     checkerApprovals,
		},
		Signer:         signerActorSnapshot{ActorID: actor.ID.String(), Role: caseRoleSigner},
		Recommendation: recommendation,
		ApprovedAt:     decision.CreatedAt,
	}, nil
}

func signerSnapshotText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func signerSnapshotNumeric(value pgtype.Numeric) (*float64, error) {
	if !value.Valid {
		return nil, nil
	}
	converted, err := value.Float64Value()
	if err != nil {
		return nil, err
	}
	if !converted.Valid {
		return nil, nil
	}
	return &converted.Float64, nil
}
