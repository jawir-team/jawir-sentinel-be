package ai

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// VerificationStatus is the outcome of semantic candidate verification.
type VerificationStatus string

const (
	VerificationStatusPass            VerificationStatus = "PASS"
	VerificationStatusPassWithWarning VerificationStatus = "PASS_WITH_WARNING"
	VerificationStatusFail            VerificationStatus = "FAIL"
)

// VerifierIssueType classifies a semantic verification finding.
type VerifierIssueType string

const (
	VerifierIssueUnsupportedClaim             VerifierIssueType = "UNSUPPORTED_CLAIM"
	VerifierIssueHallucinatedEvidence         VerifierIssueType = "HALLUCINATED_EVIDENCE"
	VerifierIssuePolicyContradiction          VerifierIssueType = "POLICY_CONTRADICTION"
	VerifierIssueEvidenceMismatch             VerifierIssueType = "EVIDENCE_MISMATCH"
	VerifierIssueMissingCriticalInformation   VerifierIssueType = "MISSING_CRITICAL_INFORMATION"
	VerifierIssuePolicyConflict               VerifierIssueType = "POLICY_CONFLICT"
	VerifierIssueRecommendationPolicyMismatch VerifierIssueType = "RECOMMENDATION_POLICY_MISMATCH"
)

// IssueSeverity controls the aggregate verification status. INFO does not
// change PASS, WARNING produces PASS_WITH_WARNING, and CRITICAL produces FAIL.
type IssueSeverity string

const (
	IssueSeverityInfo     IssueSeverity = "INFO"
	IssueSeverityWarning  IssueSeverity = "WARNING"
	IssueSeverityCritical IssueSeverity = "CRITICAL"
)

// VerifierIssue describes one semantic problem and preserves every candidate
// reference involved in the finding.
type VerifierIssue struct {
	Type        VerifierIssueType `json:"type"`
	Severity    IssueSeverity     `json:"severity"`
	Description string            `json:"description"`
	RelatedRefs []string          `json:"related_refs"`
}

// VerificationResult is safe to persist as verification metadata. It does not
// imply or perform any workflow transition.
type VerificationResult struct {
	Status VerificationStatus `json:"status"`
	Issues []VerifierIssue    `json:"issues"`
}

// VerificationNotesJSON serializes the issues for the ai_analyses
// verification_notes JSONB column. verification_status is persisted separately.
func (r VerificationResult) VerificationNotesJSON() ([]byte, error) {
	issues := r.Issues
	if issues == nil {
		issues = []VerifierIssue{}
	}
	return json.Marshal(issues)
}

// EvidenceReference is evidence material available to the verifier.
// SupportedClaims and ContradictedClaims are deterministic semantic annotations
// produced by the caller; Content and Excerpts also support exact normalized
// claim matches without attempting unreliable natural-language inference.
type EvidenceReference struct {
	Content            string
	Excerpts           []string
	SupportedClaims    []string
	ContradictedClaims []string
}

// PolicyReference is policy material available to the verifier. The map key in
// VerificationContext is the exact policy ref accepted from a candidate (for
// example, a policy chunk or version ID). ConflictsWith contains those exact
// refs and represents a conflict established by the supplied policy context.
type PolicyReference struct {
	Content            string
	Excerpts           []string
	SupportedClaims    []string
	ContradictedClaims []string
	ConflictsWith      []string
}

// VerificationContext is the complete authority boundary for verification.
// A nil known-ref map means that ref category was not supplied and therefore
// cannot be checked; a non-nil empty map means no refs in that category exist.
// RequiredInformation entries absent from AvailableInformation are critical.
type VerificationContext struct {
	KnownCaseRefs        map[string]struct{}
	KnownEvidenceRefs    map[string]EvidenceReference
	KnownPolicyRefs      map[string]PolicyReference
	RequiredInformation  []string
	AvailableInformation map[string]struct{}
}

// VerifyCandidate semantically checks a schema-valid analysis against only the
// supplied context and returns a defensive, unchanged copy of the candidate.
//
// This is a pure domain function. In particular, FAIL is persistence data for
// an orchestrator: this function performs no database writes, case-state
// mutation, workflow transition, audit event, or VERIFIER_FAIL dispatch.
// POLICY_CONFLICT is only a VerifierIssueType and is never a workflow event.
func VerifyCandidate(candidate CandidateAnalysis, ctx VerificationContext) (CandidateAnalysis, VerificationResult) {
	verifier := candidateVerifier{context: ctx, issues: make([]VerifierIssue, 0)}
	verifier.verifyFacts(candidate.Facts)
	verifier.verifyRisks(candidate.RiskAnalysis)
	verifier.verifyCompliance(candidate.ComplianceAnalysis)
	verifier.verifyRecommendation(candidate.Recommendation)
	verifier.verifyPolicyConflict(candidate.PolicyStatus)
	verifier.verifyMissingInformation(candidate.Unknowns, candidate.MissingInformation)

	return cloneCandidateAnalysis(candidate), VerificationResult{
		Status: statusForIssues(verifier.issues),
		Issues: verifier.issues,
	}
}

type candidateVerifier struct {
	context VerificationContext
	issues  []VerifierIssue
}

func (v *candidateVerifier) verifyFacts(facts []CandidateFact) {
	for i, fact := range facts {
		path := fmt.Sprintf("facts[%d]", i)
		ref := strings.TrimSpace(fact.SourceRef)
		switch fact.SourceType {
		case FactSourceCase:
			if v.context.KnownCaseRefs != nil && !setContains(v.context.KnownCaseRefs, ref) {
				v.hallucinated(path, []string{ref})
			}
		case FactSourceEvidence:
			if !v.knownEvidence(ref, path) {
				continue
			}
			v.checkEvidenceMatch(path, []string{ref}, fact.Statement, []string{ref})
		case FactSourcePolicy:
			if !v.knownPolicy(ref, path) {
				continue
			}
			v.checkPolicyContradiction(path, []string{ref}, fact.Statement, []string{ref})
		}
	}
}

func (v *candidateVerifier) verifyRisks(risks []CandidateRisk) {
	for i, risk := range risks {
		path := fmt.Sprintf("risk_analysis[%d]", i)
		refs := mergeRefs(risk.EvidenceRefs, risk.PolicyRefs)
		if len(refs) == 0 {
			v.unsupported(path, refs)
		}
		knownEvidence := v.validateEvidenceRefs(path, risk.EvidenceRefs)
		knownPolicies := v.validatePolicyRefs(path, risk.PolicyRefs)
		v.checkEvidenceMatch(path, knownEvidence, risk.Reason, refs)
		v.checkPolicyContradiction(path, knownPolicies, risk.Reason, refs)
	}
}

func (v *candidateVerifier) verifyCompliance(compliance *CandidateCompliance) {
	if compliance == nil {
		return
	}
	const path = "compliance_analysis"
	if len(nonEmptyRefs(compliance.PolicyRefs)) == 0 {
		v.unsupported(path, compliance.PolicyRefs)
	}
	knownPolicies := v.validatePolicyRefs(path, compliance.PolicyRefs)
	claim := string(compliance.Status) + " " + compliance.Reason
	v.checkPolicyContradiction(path, knownPolicies, claim, compliance.PolicyRefs)
}

func (v *candidateVerifier) verifyRecommendation(recommendation *CandidateRecommendation) {
	if recommendation == nil {
		return
	}
	for i, action := range recommendation.Actions {
		path := fmt.Sprintf("recommendation.actions[%d]", i)
		allRefs := mergeRefs(action.EvidenceRefs, action.PolicyRefs)
		if len(allRefs) == 0 {
			v.unsupported(path, allRefs)
		}
		knownEvidence := v.validateEvidenceRefs(path, action.EvidenceRefs)
		knownPolicies := v.validatePolicyRefs(path, action.PolicyRefs)
		claim := action.Action + " " + action.Reason
		v.checkEvidenceMatch(path, knownEvidence, claim, allRefs)
		v.checkPolicyContradiction(path, knownPolicies, claim, allRefs)
		v.checkRecommendationPolicyMatch(path, recommendation.Type, action.PolicyRefs, claim, allRefs)
	}
}

func (v *candidateVerifier) verifyPolicyConflict(status PolicyStatus) {
	if status != PolicyStatusConflict {
		return
	}
	conflicting := make(map[string]struct{})
	for ref, policy := range v.context.KnownPolicyRefs {
		for _, other := range policy.ConflictsWith {
			if _, ok := v.context.KnownPolicyRefs[other]; !ok || other == ref {
				continue
			}
			conflicting[ref] = struct{}{}
			conflicting[other] = struct{}{}
		}
	}
	if len(conflicting) == 0 {
		return
	}
	refs := sortedSet(conflicting)
	v.add(VerifierIssuePolicyConflict, IssueSeverityWarning,
		"policy_status reports a conflict confirmed by supplied policies", refs)
}

func (v *candidateVerifier) verifyMissingInformation(unknowns []CandidateUnknown, missing []CandidateMissingInformation) {
	reported := make(map[string]int, len(unknowns)+len(missing))
	for _, information := range missing {
		key := normalize(information.Item)
		if key == "" {
			continue
		}
		reported[key] = len(v.issues)
		v.add(VerifierIssueMissingCriticalInformation, IssueSeverityWarning,
			"candidate reports missing critical information: "+information.Item, nil)
	}
	for _, unknown := range unknowns {
		key := normalize(unknown.Item)
		if key == "" {
			continue
		}
		if _, duplicate := reported[key]; duplicate {
			continue
		}
		reported[key] = len(v.issues)
		v.add(VerifierIssueMissingCriticalInformation, IssueSeverityWarning,
			"candidate reports unresolved critical information: "+unknown.Item, nil)
	}
	for _, required := range v.context.RequiredInformation {
		key := normalize(required)
		if key == "" || normalizedSetContains(v.context.AvailableInformation, key) {
			continue
		}
		if issueIndex, alreadyReported := reported[key]; alreadyReported {
			v.issues[issueIndex].Severity = IssueSeverityCritical
			v.issues[issueIndex].Description = "required case information is absent: " + required
			continue
		}
		v.add(VerifierIssueMissingCriticalInformation, IssueSeverityCritical,
			"required case information is absent: "+required, nil)
	}
}

func (v *candidateVerifier) validateEvidenceRefs(path string, refs []string) []string {
	refs = nonEmptyRefs(refs)
	known := make([]string, 0, len(refs))
	missing := false
	for _, ref := range refs {
		if v.context.KnownEvidenceRefs == nil {
			known = append(known, ref)
			continue
		}
		if _, ok := v.context.KnownEvidenceRefs[ref]; ok {
			known = append(known, ref)
		} else {
			missing = true
		}
	}
	if missing {
		v.hallucinated(path, refs)
	}
	return known
}

func (v *candidateVerifier) validatePolicyRefs(path string, refs []string) []string {
	refs = nonEmptyRefs(refs)
	known := make([]string, 0, len(refs))
	missing := false
	for _, ref := range refs {
		if v.context.KnownPolicyRefs == nil {
			known = append(known, ref)
			continue
		}
		if _, ok := v.context.KnownPolicyRefs[ref]; ok {
			known = append(known, ref)
		} else {
			missing = true
		}
	}
	if missing {
		v.add(VerifierIssueHallucinatedEvidence, IssueSeverityCritical,
			path+" cites a policy ref absent from the verification context", refs)
	}
	return known
}

func (v *candidateVerifier) knownEvidence(ref, path string) bool {
	if v.context.KnownEvidenceRefs == nil {
		return true
	}
	if _, ok := v.context.KnownEvidenceRefs[ref]; ok {
		return true
	}
	v.hallucinated(path, []string{ref})
	return false
}

func (v *candidateVerifier) knownPolicy(ref, path string) bool {
	if v.context.KnownPolicyRefs == nil {
		return true
	}
	if _, ok := v.context.KnownPolicyRefs[ref]; ok {
		return true
	}
	v.add(VerifierIssueHallucinatedEvidence, IssueSeverityCritical,
		path+" cites a policy ref absent from the verification context", []string{ref})
	return false
}

func (v *candidateVerifier) hallucinated(path string, refs []string) {
	v.add(VerifierIssueHallucinatedEvidence, IssueSeverityCritical,
		path+" cites a ref absent from the verification context", refs)
}

func (v *candidateVerifier) unsupported(path string, refs []string) {
	v.add(VerifierIssueUnsupportedClaim, IssueSeverityWarning,
		path+" has no supporting evidence or policy ref", refs)
}

func (v *candidateVerifier) checkEvidenceMatch(path string, cited []string, claim string, related []string) {
	if len(cited) == 0 || v.context.KnownEvidenceRefs == nil {
		return
	}
	for _, ref := range cited {
		if claimMatches(v.context.KnownEvidenceRefs[ref].ContradictedClaims, claim) {
			v.add(VerifierIssueEvidenceMismatch, IssueSeverityWarning,
				path+" is contradicted by its cited evidence", related)
			return
		}
	}
	supporting := v.supportingEvidenceRefs(claim)
	if len(supporting) > 0 && !overlaps(cited, supporting) {
		v.add(VerifierIssueEvidenceMismatch, IssueSeverityWarning,
			path+" cites evidence that does not match the supplied support", related)
	}
}

func (v *candidateVerifier) checkPolicyContradiction(path string, cited []string, claim string, related []string) bool {
	for _, ref := range cited {
		policy, ok := v.context.KnownPolicyRefs[ref]
		if ok && claimMatches(policy.ContradictedClaims, claim) {
			v.add(VerifierIssuePolicyContradiction, IssueSeverityCritical,
				path+" contradicts a cited policy", related)
			return true
		}
	}
	return false
}
func (v *candidateVerifier) checkRecommendationPolicyMatch(path string, recommendationType RecommendationType, cited []string, claim string, related []string) {
	cited = nonEmptyRefs(cited)
	if recommendationType == RecommendationTypePolicyBased && len(cited) == 0 {
		v.add(VerifierIssueRecommendationPolicyMismatch, IssueSeverityWarning,
			path+" is policy-based but cites no policy", related)
		return
	}
	if recommendationType == RecommendationTypeNonPolicy && len(cited) > 0 {
		v.add(VerifierIssueRecommendationPolicyMismatch, IssueSeverityWarning,
			path+" cites policy despite a non-policy recommendation type", related)
		return
	}
	supporting := v.supportingPolicyRefs(claim)
	if len(supporting) > 0 && !overlaps(cited, supporting) {
		v.add(VerifierIssueRecommendationPolicyMismatch, IssueSeverityWarning,
			path+" policy refs do not match the policies supporting the action", related)
	}
}

func (v *candidateVerifier) supportingEvidenceRefs(claim string) []string {
	refs := make([]string, 0)
	for ref, evidence := range v.context.KnownEvidenceRefs {
		if referenceSupports(evidence.Content, evidence.Excerpts, evidence.SupportedClaims, claim) {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func (v *candidateVerifier) supportingPolicyRefs(claim string) []string {
	refs := make([]string, 0)
	for ref, policy := range v.context.KnownPolicyRefs {
		if referenceSupports(policy.Content, policy.Excerpts, policy.SupportedClaims, claim) {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func (v *candidateVerifier) add(issueType VerifierIssueType, severity IssueSeverity, description string, refs []string) {
	v.issues = append(v.issues, VerifierIssue{
		Type:        issueType,
		Severity:    severity,
		Description: description,
		RelatedRefs: nonEmptyRefs(refs),
	})
}

func statusForIssues(issues []VerifierIssue) VerificationStatus {
	status := VerificationStatusPass
	for _, issue := range issues {
		switch issue.Severity {
		case IssueSeverityCritical:
			return VerificationStatusFail
		case IssueSeverityWarning:
			status = VerificationStatusPassWithWarning
		}
	}
	return status
}

func referenceSupports(content string, excerpts, supported []string, claim string) bool {
	if claimMatches(supported, claim) {
		return true
	}
	claim = normalize(claim)
	if claim == "" {
		return false
	}
	for _, material := range append(slices.Clone(excerpts), content) {
		material = normalize(material)
		if material != "" && strings.Contains(material, claim) {
			return true
		}
	}
	return false
}

func claimMatches(patterns []string, claim string) bool {
	claim = normalize(claim)
	if claim == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = normalize(pattern)
		if pattern != "" && (strings.Contains(claim, pattern) || strings.Contains(pattern, claim)) {
			return true
		}
	}
	return false
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func nonEmptyRefs(refs []string) []string {
	result := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		result = append(result, ref)
	}
	return result
}

func mergeRefs(groups ...[]string) []string {
	var refs []string
	for _, group := range groups {
		refs = append(refs, group...)
	}
	return nonEmptyRefs(refs)
}

func overlaps(left, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, item := range left {
		set[item] = struct{}{}
	}
	for _, item := range right {
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}

func setContains(set map[string]struct{}, item string) bool {
	_, ok := set[item]
	return ok
}

func normalizedSetContains(set map[string]struct{}, item string) bool {
	for value := range set {
		if normalize(value) == item {
			return true
		}
	}
	return false
}

func sortedSet(set map[string]struct{}) []string {
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func cloneCandidateAnalysis(candidate CandidateAnalysis) CandidateAnalysis {
	clone := candidate
	clone.Facts = slices.Clone(candidate.Facts)
	clone.Assumptions = slices.Clone(candidate.Assumptions)
	clone.Unknowns = slices.Clone(candidate.Unknowns)
	clone.RiskAnalysis = slices.Clone(candidate.RiskAnalysis)
	for i := range clone.RiskAnalysis {
		clone.RiskAnalysis[i].EvidenceRefs = slices.Clone(candidate.RiskAnalysis[i].EvidenceRefs)
		clone.RiskAnalysis[i].PolicyRefs = slices.Clone(candidate.RiskAnalysis[i].PolicyRefs)
	}
	if candidate.ComplianceAnalysis != nil {
		compliance := *candidate.ComplianceAnalysis
		compliance.PolicyRefs = slices.Clone(candidate.ComplianceAnalysis.PolicyRefs)
		clone.ComplianceAnalysis = &compliance
	}
	if candidate.Recommendation != nil {
		recommendation := *candidate.Recommendation
		recommendation.Actions = slices.Clone(candidate.Recommendation.Actions)
		for i := range recommendation.Actions {
			recommendation.Actions[i].PolicyRefs = slices.Clone(candidate.Recommendation.Actions[i].PolicyRefs)
			recommendation.Actions[i].EvidenceRefs = slices.Clone(candidate.Recommendation.Actions[i].EvidenceRefs)
		}
		recommendation.PotentialBenefits = slices.Clone(candidate.Recommendation.PotentialBenefits)
		recommendation.PotentialRisks = slices.Clone(candidate.Recommendation.PotentialRisks)
		clone.Recommendation = &recommendation
	}
	clone.Alternatives = slices.Clone(candidate.Alternatives)
	for i := range clone.Alternatives {
		clone.Alternatives[i].Benefits = slices.Clone(candidate.Alternatives[i].Benefits)
		clone.Alternatives[i].Risks = slices.Clone(candidate.Alternatives[i].Risks)
	}
	clone.MissingInformation = slices.Clone(candidate.MissingInformation)
	return clone
}
