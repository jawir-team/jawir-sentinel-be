package ai

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/retrieval"
)

var (
	ErrInvalidProvenance    = errors.New("analysis provenance is invalid")
	ErrDuplicateEvidenceRef = errors.New("duplicate analysis evidence reference")
)

type EvidenceUsageType string

const (
	EvidenceUsageSupportingFact    EvidenceUsageType = "SUPPORTING_FACT"
	EvidenceUsageContext           EvidenceUsageType = "CONTEXT"
	EvidenceUsageExecutionFeedback EvidenceUsageType = "EXECUTION_FEEDBACK"
	EvidenceUsageReviewFeedback    EvidenceUsageType = "REVIEW_FEEDBACK"
)

func (v EvidenceUsageType) Valid() bool {
	switch v {
	case EvidenceUsageSupportingFact,
		EvidenceUsageContext,
		EvidenceUsageExecutionFeedback,
		EvidenceUsageReviewFeedback:
		return true
	default:
		return false
	}
}

// PolicyRef is the exact policy provenance of one retrieved policy chunk.
type PolicyRef struct {
	PolicyVersionID pgtype.UUID
	Section         pgtype.Text
	Excerpt         pgtype.Text
	RelevanceScore  pgtype.Numeric
}

// EvidenceRef is one evidence usage row.
type EvidenceRef struct {
	EvidenceID pgtype.UUID
	UsageType  EvidenceUsageType
}

// AnalysisProvenance groups the exact refs used by one analysis version.
type AnalysisProvenance struct {
	PolicyRefs   []PolicyRef
	EvidenceRefs []EvidenceRef
}

type EvidenceUsageInput struct {
	SupportingFactIDs    []pgtype.UUID
	ContextIDs           []pgtype.UUID
	ExecutionFeedbackIDs []pgtype.UUID
	ReviewFeedbackIDs    []pgtype.UUID
}

func NewPolicyRefFromChunk(chunk retrieval.ScoredChunk) (PolicyRef, error) {
	if !validProvenanceUUID(chunk.PolicyVersionID) {
		return PolicyRef{}, fmt.Errorf("%w: policy version ID is invalid", ErrInvalidProvenance)
	}
	if chunk.Section.Valid && utf8.RuneCountInString(chunk.Section.String) > 150 {
		return PolicyRef{}, fmt.Errorf("%w: policy section exceeds 150 characters", ErrInvalidProvenance)
	}
	if math.IsNaN(chunk.RelevanceScore) || math.IsInf(chunk.RelevanceScore, 0) || chunk.RelevanceScore < 0 || chunk.RelevanceScore > 1 {
		return PolicyRef{}, fmt.Errorf("%w: policy relevance score must be between 0 and 1", ErrInvalidProvenance)
	}

	return PolicyRef{
		PolicyVersionID: chunk.PolicyVersionID,
		Section:         chunk.Section,
		Excerpt: pgtype.Text{
			String: chunk.Content,
			Valid:  chunk.Content != "",
		},
		RelevanceScore: pgtype.Numeric{
			Int:   big.NewInt(int64(math.Round(chunk.RelevanceScore * 100000))),
			Exp:   -5,
			Valid: true,
		},
	}, nil
}

func NewEvidenceRef(evidenceID pgtype.UUID, usage EvidenceUsageType) (EvidenceRef, error) {
	ref := EvidenceRef{EvidenceID: evidenceID, UsageType: usage}
	if err := validateEvidenceRef(ref); err != nil {
		return EvidenceRef{}, err
	}
	return ref, nil
}

func BuildEvidenceRefs(in EvidenceUsageInput) []EvidenceRef {
	groups := []struct {
		ids   []pgtype.UUID
		usage EvidenceUsageType
	}{
		{in.SupportingFactIDs, EvidenceUsageSupportingFact},
		{in.ContextIDs, EvidenceUsageContext},
		{in.ExecutionFeedbackIDs, EvidenceUsageExecutionFeedback},
		{in.ReviewFeedbackIDs, EvidenceUsageReviewFeedback},
	}

	refs := make([]EvidenceRef, 0)
	seen := make(map[evidenceRefKey]struct{})
	for _, group := range groups {
		for _, id := range group.ids {
			if !validProvenanceUUID(id) {
				continue
			}
			key := evidenceRefKey{id: id.Bytes, usage: group.usage}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, EvidenceRef{EvidenceID: id, UsageType: group.usage})
		}
	}
	return refs
}

func (p *AnalysisProvenance) Validate() error {
	if p == nil {
		return nil
	}
	for i, ref := range p.PolicyRefs {
		if err := validatePolicyRef(ref); err != nil {
			return fmt.Errorf("policy ref %d: %w", i, err)
		}
	}

	seen := make(map[evidenceRefKey]struct{}, len(p.EvidenceRefs))
	for i, ref := range p.EvidenceRefs {
		if err := validateEvidenceRef(ref); err != nil {
			return fmt.Errorf("evidence ref %d: %w", i, err)
		}
		key := evidenceRefKey{id: ref.EvidenceID.Bytes, usage: ref.UsageType}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: %w: evidence ref %d repeats an evidence ID and usage type", ErrInvalidProvenance, ErrDuplicateEvidenceRef, i)
		}
		seen[key] = struct{}{}
	}
	return nil
}

type evidenceRefKey struct {
	id    [16]byte
	usage EvidenceUsageType
}

func validatePolicyRef(ref PolicyRef) error {
	if !validProvenanceUUID(ref.PolicyVersionID) {
		return fmt.Errorf("%w: policy version ID is invalid", ErrInvalidProvenance)
	}
	if ref.Section.Valid && utf8.RuneCountInString(ref.Section.String) > 150 {
		return fmt.Errorf("%w: policy section exceeds 150 characters", ErrInvalidProvenance)
	}
	if !ref.RelevanceScore.Valid {
		return nil
	}
	value, err := ref.RelevanceScore.Float64Value()
	if err != nil || !value.Valid || math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0) || value.Float64 < 0 || value.Float64 > 1 {
		return fmt.Errorf("%w: policy relevance score must be between 0 and 1", ErrInvalidProvenance)
	}
	return nil
}

func validateEvidenceRef(ref EvidenceRef) error {
	if !validProvenanceUUID(ref.EvidenceID) {
		return fmt.Errorf("%w: evidence ID is invalid", ErrInvalidProvenance)
	}
	if !ref.UsageType.Valid() {
		return fmt.Errorf("%w: evidence usage type %q is invalid", ErrInvalidProvenance, ref.UsageType)
	}
	return nil
}

func validProvenanceUUID(id pgtype.UUID) bool {
	return id.Valid && id.Bytes != [16]byte{}
}
