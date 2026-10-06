package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/embedding"
)

func TestRetrieveEmptyTextSkipsEmbeddingAndSearch(t *testing.T) {
	embedder := &fakeQueryEmbedder{}
	searcher := &fakeSearcher{}
	retriever := NewRetriever(searcher, embedder)

	got, err := retriever.Retrieve(context.Background(), pgtype.UUID{}, " \t\n ")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Retrieve() = %#v, want empty non-nil slice", got)
	}
	if embedder.calls != 0 {
		t.Errorf("EmbedQuery() calls = %d, want 0", embedder.calls)
	}
	if searcher.calls != 0 {
		t.Errorf("SearchPolicyChunks() calls = %d, want 0", searcher.calls)
	}
}

func TestRetrievePropagatesEmbedderError(t *testing.T) {
	wantErr := errors.New("embedding unavailable")
	embedder := &fakeQueryEmbedder{err: wantErr}
	searcher := &fakeSearcher{}
	retriever := NewRetriever(searcher, embedder)

	_, err := retriever.Retrieve(context.Background(), pgtype.UUID{}, "case text")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Retrieve() error = %v, want %v", err, wantErr)
	}
	if searcher.calls != 0 {
		t.Errorf("SearchPolicyChunks() calls = %d, want 0", searcher.calls)
	}
}

func TestRetrieveRejectsWrongEmbeddingDimension(t *testing.T) {
	embedder := &fakeQueryEmbedder{vector: []float32{1, 2, 3}}
	searcher := &fakeSearcher{}
	retriever := NewRetriever(searcher, embedder)

	_, err := retriever.Retrieve(context.Background(), pgtype.UUID{}, "case text")
	if err == nil || !strings.Contains(err.Error(), "dimension 3") {
		t.Fatalf("Retrieve() error = %v, want dimension error", err)
	}
	if searcher.calls != 0 {
		t.Errorf("SearchPolicyChunks() calls = %d, want 0", searcher.calls)
	}
}

func TestRetrieveSearchesAndMapsProvenance(t *testing.T) {
	vector := make([]float32, embedding.EmbeddingDimension)
	for i := range vector {
		vector[i] = float32(i) / 1000
	}
	caseTypeID := testUUID(1)
	policyID := testUUID(2)
	policyVersionID := testUUID(3)
	chunkID := testUUID(4)
	section := pgtype.Text{String: "Approval limits", Valid: true}
	const distance = 0.42

	embedder := &fakeQueryEmbedder{vector: vector}
	searcher := &fakeSearcher{rows: []db.SearchPolicyChunksRow{{
		ChunkID:         chunkID,
		PolicyVersionID: policyVersionID,
		ChunkIndex:      7,
		Section:         section,
		Content:         "Manager approval is required.",
		Distance:        distance,
		VersionID:       policyVersionID,
		Version:         "v2",
		PolicyID:        policyID,
		PolicyCode:      "POL-002",
		PolicyTitle:     "Approval Policy",
	}}}
	retriever := NewRetriever(searcher, embedder)

	got, err := retriever.Retrieve(context.Background(), caseTypeID, "who can approve?")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if embedder.calls != 1 || embedder.text != "who can approve?" {
		t.Errorf("EmbedQuery() calls/text = %d/%q", embedder.calls, embedder.text)
	}
	if searcher.calls != 1 {
		t.Fatalf("SearchPolicyChunks() calls = %d, want 1", searcher.calls)
	}
	if searcher.params.Limit != TopK {
		t.Errorf("Limit = %d, want %d", searcher.params.Limit, TopK)
	}
	if searcher.params.CaseTypeID != caseTypeID {
		t.Errorf("CaseTypeID = %#v, want %#v", searcher.params.CaseTypeID, caseTypeID)
	}
	if !strings.HasPrefix(searcher.params.QueryEmbedding, "[") || !strings.HasSuffix(searcher.params.QueryEmbedding, "]") {
		t.Errorf("QueryEmbedding = %q, want bracketed vector", searcher.params.QueryEmbedding)
	}
	values := strings.Split(strings.Trim(searcher.params.QueryEmbedding, "[]"), ",")
	if len(values) != embedding.EmbeddingDimension {
		t.Errorf("QueryEmbedding has %d values, want %d", len(values), embedding.EmbeddingDimension)
	}

	if len(got) != 1 {
		t.Fatalf("Retrieve() returned %d chunks, want 1", len(got))
	}
	want := ScoredChunk{
		PolicyID:        policyID,
		PolicyCode:      "POL-002",
		PolicyTitle:     "Approval Policy",
		PolicyVersionID: policyVersionID,
		Version:         "v2",
		Section:         section,
		ChunkID:         chunkID,
		ChunkIndex:      7,
		Content:         "Manager approval is required.",
		Distance:        distance,
		RelevanceScore:  1 - distance/2,
	}
	if got[0] != want {
		t.Errorf("Retrieve() chunk = %#v, want %#v", got[0], want)
	}
}

func TestRetrieveEmptyRowsReturnsNonNilSlice(t *testing.T) {
	embedder := &fakeQueryEmbedder{vector: make([]float32, embedding.EmbeddingDimension)}
	searcher := &fakeSearcher{rows: []db.SearchPolicyChunksRow{}}
	retriever := NewRetriever(searcher, embedder)

	got, err := retriever.Retrieve(context.Background(), testUUID(5), "case text")
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Retrieve() = %#v, want empty non-nil slice", got)
	}
}

func TestRetrievePassesThroughNullCaseType(t *testing.T) {
	nullCaseTypeID := pgtype.UUID{}
	embedder := &fakeQueryEmbedder{vector: make([]float32, embedding.EmbeddingDimension)}
	searcher := &fakeSearcher{rows: []db.SearchPolicyChunksRow{}}
	retriever := NewRetriever(searcher, embedder)

	if _, err := retriever.Retrieve(context.Background(), nullCaseTypeID, "generic policy query"); err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if searcher.params.CaseTypeID != nullCaseTypeID {
		t.Errorf("CaseTypeID = %#v, want invalid UUID passthrough", searcher.params.CaseTypeID)
	}
}

type fakeQueryEmbedder struct {
	vector []float32
	err    error
	calls  int
	text   string
}

func (f *fakeQueryEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	f.calls++
	f.text = text
	return f.vector, f.err
}

type fakeSearcher struct {
	rows   []db.SearchPolicyChunksRow
	err    error
	calls  int
	params db.SearchPolicyChunksParams
}

func (f *fakeSearcher) SearchPolicyChunks(_ context.Context, params db.SearchPolicyChunksParams) ([]db.SearchPolicyChunksRow, error) {
	f.calls++
	f.params = params
	return f.rows, f.err
}

func testUUID(lastByte byte) pgtype.UUID {
	var bytes [16]byte
	bytes[len(bytes)-1] = lastByte
	return pgtype.UUID{Bytes: bytes, Valid: true}
}

var (
	_ QueryEmbedder = (*fakeQueryEmbedder)(nil)
	_ Searcher      = (*fakeSearcher)(nil)
)
