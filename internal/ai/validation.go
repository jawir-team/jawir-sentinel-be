package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ValidationError describes one client-safe structured-output failure. Field
// is a JSON path into CandidateAnalysis.
type ValidationError struct {
	Field  string
	Reason string
}

// ValidationErrors contains all structured-output failures found in a
// candidate.
type ValidationErrors []ValidationError

func (errs ValidationErrors) Error() string {
	if len(errs) == 0 {
		return "ai: candidate validation failed"
	}

	problems := make([]string, 0, len(errs))
	for _, err := range errs {
		problems = append(problems, fmt.Sprintf("%s: %s", err.Field, err.Reason))
	}
	return "ai: candidate validation failed: " + strings.Join(problems, "; ")
}

// Validate performs structural validation of a parsed candidate. It does not
// verify whether statements are true or grounded in the supplied evidence.
func (c CandidateAnalysis) Validate() error {
	var validationErrs ValidationErrors

	missing := c.MissingRequiredFields()
	for _, field := range missing {
		validationErrs = append(validationErrs, ValidationError{
			Field:  field,
			Reason: "is required",
		})
	}

	if enumErr := c.ValidateEnums(); enumErr != nil {
		validationErrs = append(validationErrs, enumErrors(enumErr, missing)...)
	}

	for i, fact := range c.Facts {
		prefix := fmt.Sprintf("facts[%d]", i)
		validationErrs.requireNonEmpty(prefix+".statement", fact.Statement)
		validationErrs.requireNonEmpty(prefix+".source_ref", fact.SourceRef)
	}

	for i, assumption := range c.Assumptions {
		prefix := fmt.Sprintf("assumptions[%d]", i)
		validationErrs.requireNonEmpty(prefix+".statement", assumption.Statement)
		validationErrs.requireNonEmpty(prefix+".reason", assumption.Reason)
	}

	for i, unknown := range c.Unknowns {
		prefix := fmt.Sprintf("unknowns[%d]", i)
		validationErrs.requireNonEmpty(prefix+".item", unknown.Item)
		validationErrs.requireNonEmpty(prefix+".impact", unknown.Impact)
	}

	for i, risk := range c.RiskAnalysis {
		prefix := fmt.Sprintf("risk_analysis[%d]", i)
		validationErrs.requireNonEmpty(prefix+".reason", risk.Reason)
		validationErrs.requirePresent(prefix+".evidence_refs", risk.EvidenceRefs)
		validationErrs.requirePresent(prefix+".policy_refs", risk.PolicyRefs)
	}

	if compliance := c.ComplianceAnalysis; compliance != nil {
		validationErrs.requireNonEmpty("compliance_analysis.reason", compliance.Reason)
		validationErrs.requirePresent("compliance_analysis.policy_refs", compliance.PolicyRefs)
	}

	if recommendation := c.Recommendation; recommendation != nil {
		validationErrs.requireNonEmpty("recommendation.summary", recommendation.Summary)
		validationErrs.requirePresent("recommendation.actions", recommendation.Actions)
		for i, action := range recommendation.Actions {
			prefix := fmt.Sprintf("recommendation.actions[%d]", i)
			validationErrs.requireNonEmpty(prefix+".action", action.Action)
			validationErrs.requireNonEmpty(prefix+".reason", action.Reason)
			validationErrs.requirePresent(prefix+".policy_refs", action.PolicyRefs)
			validationErrs.requirePresent(prefix+".evidence_refs", action.EvidenceRefs)
		}
		validationErrs.requirePresent("recommendation.potential_benefits", recommendation.PotentialBenefits)
		validationErrs.requirePresent("recommendation.potential_risks", recommendation.PotentialRisks)
	}

	for i, alternative := range c.Alternatives {
		prefix := fmt.Sprintf("alternatives[%d]", i)
		validationErrs.requireNonEmpty(prefix+".summary", alternative.Summary)
		validationErrs.requirePresent(prefix+".benefits", alternative.Benefits)
		validationErrs.requirePresent(prefix+".risks", alternative.Risks)
	}

	for i, information := range c.MissingInformation {
		prefix := fmt.Sprintf("missing_information[%d]", i)
		validationErrs.requireNonEmpty(prefix+".item", information.Item)
		validationErrs.requireNonEmpty(prefix+".why_needed", information.WhyNeeded)
	}

	if len(validationErrs) > 0 {
		return validationErrs
	}
	return nil
}

func (errs *ValidationErrors) requireNonEmpty(field, value string) {
	if strings.TrimSpace(value) == "" {
		*errs = append(*errs, ValidationError{Field: field, Reason: "must not be empty"})
	}
}

func (errs *ValidationErrors) requirePresent(field string, value any) {
	switch value := value.(type) {
	case []string:
		if value == nil {
			*errs = append(*errs, ValidationError{Field: field, Reason: "is required"})
		}
	case []CandidateRecommendationAction:
		if value == nil {
			*errs = append(*errs, ValidationError{Field: field, Reason: "is required"})
		}
	default:
		panic("ai: unsupported required collection type")
	}
}

func enumErrors(err error, missing []string) ValidationErrors {
	const prefix = "ai: invalid candidate enum values: "
	message := strings.TrimPrefix(err.Error(), prefix)
	missingSet := make(map[string]struct{}, len(missing))
	for _, field := range missing {
		missingSet[field] = struct{}{}
	}

	parts := strings.Split(message, ", ")
	validationErrs := make(ValidationErrors, 0, len(parts))
	for _, part := range parts {
		field, _, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if _, alreadyMissing := missingSet[field]; alreadyMissing {
			continue
		}
		validationErrs = append(validationErrs, ValidationError{
			Field:  field,
			Reason: "has invalid enum value (" + part + ")",
		})
	}
	return validationErrs
}

// ParseAndValidateCandidate is the gate for structured AI analysis output. A
// candidate may be persisted or copied into structured ai_analyses fields,
// marked COMPLETED, or allowed into CHECKING only after this function returns
// nil. This validates JSON structure and schema only; semantic truth and
// grounding are intentionally deferred to BE-030.
func ParseAndValidateCandidate(raw []byte) (CandidateAnalysis, error) {
	var candidate CandidateAnalysis
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return candidate, errors.New("ai: candidate output must be one JSON object")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		return CandidateAnalysis{}, errors.New("ai: candidate output is invalid JSON")
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CandidateAnalysis{}, errors.New("ai: candidate output contains trailing data")
	}

	if err := candidate.Validate(); err != nil {
		return CandidateAnalysis{}, err
	}
	return candidate, nil
}
