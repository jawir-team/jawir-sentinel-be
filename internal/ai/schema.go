package ai

import (
	"fmt"
	"strings"
)

// FactSourceType identifies the authoritative context section supporting a fact.
type FactSourceType string

const (
	FactSourceCase     FactSourceType = "CASE"
	FactSourceEvidence FactSourceType = "EVIDENCE"
	FactSourcePolicy   FactSourceType = "POLICY"
)

// PolicyStatus describes how completely the supplied policies address the case.
type PolicyStatus string

const (
	PolicyStatusFound                PolicyStatus = "POLICY_FOUND"
	PolicyStatusPartial              PolicyStatus = "POLICY_PARTIAL"
	PolicyStatusNotFound             PolicyStatus = "NO_POLICY_FOUND"
	PolicyStatusInsufficientEvidence PolicyStatus = "INSUFFICIENT_EVIDENCE"
	PolicyStatusConflict             PolicyStatus = "POLICY_CONFLICT"
)

// RiskType classifies an identified risk.
type RiskType string

const (
	RiskTypeOperational RiskType = "OPERATIONAL"
	RiskTypeCompliance  RiskType = "COMPLIANCE"
	RiskTypeFinancial   RiskType = "FINANCIAL"
	RiskTypeOther       RiskType = "OTHER"
)

// RiskLevel describes the severity of an identified risk.
type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "LOW"
	RiskLevelMedium   RiskLevel = "MEDIUM"
	RiskLevelHigh     RiskLevel = "HIGH"
	RiskLevelCritical RiskLevel = "CRITICAL"
)

// RecommendationType records whether the recommendation is grounded in policy.
type RecommendationType string

const (
	RecommendationTypePolicyBased RecommendationType = "POLICY_BASED"
	RecommendationTypeNonPolicy   RecommendationType = "NON_POLICY_RECOMMENDATION"
)

// QualityLevel is shared by evidence_quality and uncertainty.
type QualityLevel string

const (
	QualityLow    QualityLevel = "LOW"
	QualityMedium QualityLevel = "MEDIUM"
	QualityHigh   QualityLevel = "HIGH"
)

// ComplianceStatus is the result of comparing the case with applicable policy.
type ComplianceStatus string

const (
	ComplianceNoIssueIdentified ComplianceStatus = "NO_ISSUE_IDENTIFIED"
	CompliancePotentialConcern  ComplianceStatus = "POTENTIAL_CONCERN"
	ComplianceRequiresReview    ComplianceStatus = "REQUIRES_REVIEW"
)

type CandidateFact struct {
	Statement  string         `json:"statement"`
	SourceType FactSourceType `json:"source_type"`
	SourceRef  string         `json:"source_ref"`
}

type CandidateAssumption struct {
	Statement string `json:"statement"`
	Reason    string `json:"reason"`
}

type CandidateUnknown struct {
	Item   string `json:"item"`
	Impact string `json:"impact"`
}

type CandidateRisk struct {
	Type         RiskType  `json:"type"`
	Level        RiskLevel `json:"level"`
	Reason       string    `json:"reason"`
	EvidenceRefs []string  `json:"evidence_refs"`
	PolicyRefs   []string  `json:"policy_refs"`
}

type CandidateCompliance struct {
	Status     ComplianceStatus `json:"status"`
	Reason     string           `json:"reason"`
	PolicyRefs []string         `json:"policy_refs"`
}

type CandidateRecommendationAction struct {
	Order        int      `json:"order"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason"`
	PolicyRefs   []string `json:"policy_refs"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type CandidateRecommendation struct {
	Type              RecommendationType              `json:"type"`
	Summary           string                          `json:"summary"`
	Actions           []CandidateRecommendationAction `json:"actions"`
	PotentialBenefits []string                        `json:"potential_benefits"`
	PotentialRisks    []string                        `json:"potential_risks"`
}

type CandidateAlternative struct {
	Summary  string   `json:"summary"`
	Benefits []string `json:"benefits"`
	Risks    []string `json:"risks"`
}

type CandidateMissingInformation struct {
	Item      string `json:"item"`
	WhyNeeded string `json:"why_needed"`
}

// CandidateAnalysis is the complete, unverified model output. Pointer fields
// distinguish a present (including empty) JSON object from an omitted or null
// required object. Nil slices likewise distinguish missing/null from [].
type CandidateAnalysis struct {
	Summary            string                        `json:"summary"`
	Facts              []CandidateFact               `json:"facts"`
	Assumptions        []CandidateAssumption         `json:"assumptions"`
	Unknowns           []CandidateUnknown            `json:"unknowns"`
	PolicyStatus       PolicyStatus                  `json:"policy_status"`
	RiskAnalysis       []CandidateRisk               `json:"risk_analysis"`
	ComplianceAnalysis *CandidateCompliance          `json:"compliance_analysis"`
	Recommendation     *CandidateRecommendation      `json:"recommendation"`
	Alternatives       []CandidateAlternative        `json:"alternatives"`
	MissingInformation []CandidateMissingInformation `json:"missing_information"`
	EvidenceQuality    QualityLevel                  `json:"evidence_quality"`
	Uncertainty        QualityLevel                  `json:"uncertainty"`
}

// NewCandidateAnalysis initializes every collection so encoding/json emits []
// rather than null. Callers still need to populate all scalar/object fields.
func NewCandidateAnalysis() CandidateAnalysis {
	return CandidateAnalysis{
		Facts:              []CandidateFact{},
		Assumptions:        []CandidateAssumption{},
		Unknowns:           []CandidateUnknown{},
		RiskAnalysis:       []CandidateRisk{},
		ComplianceAnalysis: &CandidateCompliance{PolicyRefs: []string{}},
		Recommendation: &CandidateRecommendation{
			Actions:           []CandidateRecommendationAction{},
			PotentialBenefits: []string{},
			PotentialRisks:    []string{},
		},
		Alternatives:       []CandidateAlternative{},
		MissingInformation: []CandidateMissingInformation{},
	}
}

// Complete reports whether all required top-level fields are present. It does
// not replace semantic validation of field contents or provenance.
func (c CandidateAnalysis) Complete() bool {
	return len(c.MissingRequiredFields()) == 0
}

// MissingRequiredFields returns required top-level JSON fields that are absent.
// Empty arrays are present and valid; nil arrays represent an omitted or null
// field and are therefore incomplete.
func (c CandidateAnalysis) MissingRequiredFields() []string {
	missing := make([]string, 0, 12)
	if strings.TrimSpace(c.Summary) == "" {
		missing = append(missing, "summary")
	}
	if c.Facts == nil {
		missing = append(missing, "facts")
	}
	if c.Assumptions == nil {
		missing = append(missing, "assumptions")
	}
	if c.Unknowns == nil {
		missing = append(missing, "unknowns")
	}
	if c.PolicyStatus == "" {
		missing = append(missing, "policy_status")
	}
	if c.RiskAnalysis == nil {
		missing = append(missing, "risk_analysis")
	}
	if c.ComplianceAnalysis == nil {
		missing = append(missing, "compliance_analysis")
	}
	if c.Recommendation == nil {
		missing = append(missing, "recommendation")
	}
	if c.Alternatives == nil {
		missing = append(missing, "alternatives")
	}
	if c.MissingInformation == nil {
		missing = append(missing, "missing_information")
	}
	if c.EvidenceQuality == "" {
		missing = append(missing, "evidence_quality")
	}
	if c.Uncertainty == "" {
		missing = append(missing, "uncertainty")
	}
	return missing
}

func (v FactSourceType) Valid() bool {
	return v == FactSourceCase || v == FactSourceEvidence || v == FactSourcePolicy
}

func (v PolicyStatus) Valid() bool {
	switch v {
	case PolicyStatusFound, PolicyStatusPartial, PolicyStatusNotFound, PolicyStatusInsufficientEvidence, PolicyStatusConflict:
		return true
	default:
		return false
	}
}

func (v RiskType) Valid() bool {
	return v == RiskTypeOperational || v == RiskTypeCompliance || v == RiskTypeFinancial || v == RiskTypeOther
}

func (v RiskLevel) Valid() bool {
	return v == RiskLevelLow || v == RiskLevelMedium || v == RiskLevelHigh || v == RiskLevelCritical
}

func (v RecommendationType) Valid() bool {
	return v == RecommendationTypePolicyBased || v == RecommendationTypeNonPolicy
}

func (v QualityLevel) Valid() bool {
	return v == QualityLow || v == QualityMedium || v == QualityHigh
}

func (v ComplianceStatus) Valid() bool {
	return v == ComplianceNoIssueIdentified || v == CompliancePotentialConcern || v == ComplianceRequiresReview
}

// ValidateEnums performs the basic enum compatibility check for a complete
// candidate. Deeper schema/provenance verification belongs to the verifier.
func (c CandidateAnalysis) ValidateEnums() error {
	var invalid []string
	if !c.PolicyStatus.Valid() {
		invalid = append(invalid, fmt.Sprintf("policy_status=%q", c.PolicyStatus))
	}
	if !c.EvidenceQuality.Valid() {
		invalid = append(invalid, fmt.Sprintf("evidence_quality=%q", c.EvidenceQuality))
	}
	if !c.Uncertainty.Valid() {
		invalid = append(invalid, fmt.Sprintf("uncertainty=%q", c.Uncertainty))
	}
	for i, fact := range c.Facts {
		if !fact.SourceType.Valid() {
			invalid = append(invalid, fmt.Sprintf("facts[%d].source_type=%q", i, fact.SourceType))
		}
	}
	for i, risk := range c.RiskAnalysis {
		if !risk.Type.Valid() {
			invalid = append(invalid, fmt.Sprintf("risk_analysis[%d].type=%q", i, risk.Type))
		}
		if !risk.Level.Valid() {
			invalid = append(invalid, fmt.Sprintf("risk_analysis[%d].level=%q", i, risk.Level))
		}
	}
	if c.ComplianceAnalysis != nil && !c.ComplianceAnalysis.Status.Valid() {
		invalid = append(invalid, fmt.Sprintf("compliance_analysis.status=%q", c.ComplianceAnalysis.Status))
	}
	if c.Recommendation != nil && !c.Recommendation.Type.Valid() {
		invalid = append(invalid, fmt.Sprintf("recommendation.type=%q", c.Recommendation.Type))
	}
	if len(invalid) > 0 {
		return fmt.Errorf("ai: invalid candidate enum values: %s", strings.Join(invalid, ", "))
	}
	return nil
}
