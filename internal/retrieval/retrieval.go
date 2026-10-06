// Package retrieval embeds case text and finds applicable policy chunks with
// their policy and version provenance.
package retrieval

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/embedding"
)

// TopK is the fixed number of policy candidates requested from pgvector.
const TopK = 8

// QueryEmbedder produces a query vector compatible with indexed policy chunks.
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

var _ QueryEmbedder = (*embedding.VertexEmbedder)(nil)

type Searcher interface {
	SearchPolicyChunks(ctx context.Context, arg db.SearchPolicyChunksParams) ([]db.SearchPolicyChunksRow, error)
}

var _ Searcher = (*db.Queries)(nil)

// ScoredChunk is a policy chunk candidate together with complete provenance.
type ScoredChunk struct {
	PolicyID        pgtype.UUID
	PolicyCode      string
	PolicyTitle     string
	PolicyVersionID pgtype.UUID
	Version         string
	Section         pgtype.Text
	ChunkID         pgtype.UUID
	ChunkIndex      int32
	Content         string
	Distance        float64
	RelevanceScore  float64
}

// Retriever embeds query text and searches active, ready, currently effective
// policy versions. The SQL contract has no domain hard filter, similarity
// threshold, or reranker. It uses gemini-embedding-001 RETRIEVAL_QUERY vectors
// at 768 dimensions and requests TopK cosine-nearest chunks through the HNSW
// index. Retriever does not determine final policy_status; it returns only
// candidates with provenance for a later analysis stage.
type Retriever struct {
	q        Searcher
	embedder QueryEmbedder
}

// NewRetriever constructs a policy chunk retriever.
func NewRetriever(q Searcher, embedder QueryEmbedder) *Retriever {
	return &Retriever{q: q, embedder: embedder}
}

// Retrieve returns the nearest applicable policy chunks for text.
func (r *Retriever) Retrieve(ctx context.Context, caseTypeID pgtype.UUID, text string) ([]ScoredChunk, error) {
	if strings.TrimSpace(text) == "" {
		return []ScoredChunk{}, nil
	}

	vector, err := r.embedder.EmbedQuery(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(vector) != embedding.EmbeddingDimension {
		return nil, fmt.Errorf("query embedding has dimension %d, want %d", len(vector), embedding.EmbeddingDimension)
	}

	rows, err := r.q.SearchPolicyChunks(ctx, db.SearchPolicyChunksParams{
		QueryEmbedding: formatVector(vector),
		CaseTypeID:     caseTypeID,
		Limit:          TopK,
	})
	if err != nil {
		return nil, err
	}

	chunks := make([]ScoredChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, ScoredChunk{
			PolicyID:        row.PolicyID,
			PolicyCode:      row.PolicyCode,
			PolicyTitle:     row.PolicyTitle,
			PolicyVersionID: row.PolicyVersionID,
			Version:         row.Version,
			Section:         row.Section,
			ChunkID:         row.ChunkID,
			ChunkIndex:      row.ChunkIndex,
			Content:         row.Content,
			Distance:        row.Distance,
			// Cosine distance is in [0,2], so this maps it linearly to
			// a relevance score in [0,1].
			RelevanceScore: 1 - row.Distance/2,
		})
	}
	return chunks, nil
}

func formatVector(vector []float32) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for i, value := range vector {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}
