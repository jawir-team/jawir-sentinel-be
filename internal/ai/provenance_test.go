package ai

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/retrieval"
)

func TestNewPolicyRefFromChunk(t *testing.T) {
	versionID := provenanceTestUUID(1)
	section := pgtype.Text{String: "Approval limits", Valid: true}
	ref, err := NewPolicyRefFromChunk(retrieval.ScoredChunk{
		PolicyVersionID: versionID,
		Section:         section,
		Content:         "Manager approval is required.",
		RelevanceScore:  0.123456,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.PolicyVersionID != versionID || ref.Section != section {
		t.Fatalf("mapped identity/section = %+v", ref)
	}
	if !ref.Excerpt.Valid || ref.Excerpt.String != "Manager approval is required." {
		t.Errorf("excerpt = %+v", ref.Excerpt)
	}
	if !ref.RelevanceScore.Valid || ref.RelevanceScore.Int == nil || ref.RelevanceScore.Int.Int64() != 12346 || ref.RelevanceScore.Exp != -5 {
		t.Errorf("relevance score = %+v, want 0.12346", ref.RelevanceScore)
	}

	empty, err := NewPolicyRefFromChunk(retrieval.ScoredChunk{PolicyVersionID: versionID, RelevanceScore: 1})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Excerpt.Valid {
		t.Errorf("empty excerpt = %+v, want NULL", empty.Excerpt)
	}
}

func TestNewPolicyRefFromChunkRejectsInvalidInput(t *testing.T) {
	validID := provenanceTestUUID(1)
	tests := []struct {
		name  string
		chunk retrieval.ScoredChunk
	}{
		{name: "invalid version", chunk: retrieval.ScoredChunk{RelevanceScore: 0.5}},
		{name: "zero version", chunk: retrieval.ScoredChunk{PolicyVersionID: pgtype.UUID{Valid: true}, RelevanceScore: 0.5}},
		{name: "NaN", chunk: retrieval.ScoredChunk{PolicyVersionID: validID, RelevanceScore: math.NaN()}},
		{name: "negative", chunk: retrieval.ScoredChunk{PolicyVersionID: validID, RelevanceScore: -0.01}},
		{name: "above one", chunk: retrieval.ScoredChunk{PolicyVersionID: validID, RelevanceScore: 1.01}},
		{name: "long section", chunk: retrieval.ScoredChunk{PolicyVersionID: validID, Section: pgtype.Text{String: strings.Repeat("x", 151), Valid: true}, RelevanceScore: 0.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPolicyRefFromChunk(tt.chunk)
			if !errors.Is(err, ErrInvalidProvenance) {
				t.Fatalf("error = %v, want ErrInvalidProvenance", err)
			}
		})
	}
}

func TestNewEvidenceRefRejectsInvalidUsage(t *testing.T) {
	_, err := NewEvidenceRef(provenanceTestUUID(1), EvidenceUsageType("UNKNOWN"))
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("error = %v, want ErrInvalidProvenance", err)
	}
}

func TestBuildEvidenceRefsDeduplicatesExactUsageOnly(t *testing.T) {
	a := provenanceTestUUID(1)
	b := provenanceTestUUID(2)
	refs := BuildEvidenceRefs(EvidenceUsageInput{
		SupportingFactIDs: []pgtype.UUID{a, a, {}, {Valid: true}},
		ContextIDs:        []pgtype.UUID{a, b},
	})
	if len(refs) != 3 {
		t.Fatalf("refs = %+v, want 3", refs)
	}
	want := []EvidenceRef{
		{EvidenceID: a, UsageType: EvidenceUsageSupportingFact},
		{EvidenceID: a, UsageType: EvidenceUsageContext},
		{EvidenceID: b, UsageType: EvidenceUsageContext},
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, refs[i], want[i])
		}
	}
}

func TestAnalysisProvenanceValidateRejectsDuplicateEvidenceRef(t *testing.T) {
	ref := EvidenceRef{EvidenceID: provenanceTestUUID(1), UsageType: EvidenceUsageContext}
	err := (&AnalysisProvenance{EvidenceRefs: []EvidenceRef{ref, ref}}).Validate()
	if !errors.Is(err, ErrInvalidProvenance) || !errors.Is(err, ErrDuplicateEvidenceRef) {
		t.Fatalf("error = %v, want both provenance sentinels", err)
	}
	if err := (*AnalysisProvenance)(nil).Validate(); err != nil {
		t.Fatalf("nil provenance error = %v", err)
	}
}

func provenanceTestUUID(last byte) pgtype.UUID {
	var id [16]byte
	id[15] = last
	return pgtype.UUID{Bytes: id, Valid: true}
}
