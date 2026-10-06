package embedding

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/chunking"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

const maxSectionRunes = 150

var (
	ErrVersionNotIndexable = errors.New("policy version is not indexable")
	ErrStaleAttempt        = errors.New("policy index attempt is stale")
)

// IndexQueries is the database boundary required by one indexing transaction.
type IndexQueries interface {
	GetPolicyVersionForUpdate(ctx context.Context, id pgtype.UUID) (db.PolicyVersion, error)
	DeletePolicyChunks(ctx context.Context, policyVersionID pgtype.UUID) error
	CreatePolicyChunk(ctx context.Context, arg db.CreatePolicyChunkParams) (db.PolicyChunk, error)
	CompletePolicyVersionIndex(ctx context.Context, arg db.CompletePolicyVersionIndexParams) (db.PolicyVersion, error)
}

var _ IndexQueries = (*db.Queries)(nil)

// IndexPolicyVersion embeds a claimed DRAFT policy version and atomically
// replaces its chunks. The external embedding call runs between transactions.
// Only the currently claimed attempt may publish READY or FAILED; a late attempt
// never mutates state.
func IndexPolicyVersion(
	ctx context.Context,
	beginTx func(context.Context, func(context.Context, IndexQueries) error) error,
	versionID, attemptID pgtype.UUID,
	embedder Embedder,
) error {
	if beginTx == nil {
		return errors.New("index transaction runner is required")
	}
	if embedder == nil {
		return errors.New("embedder is required")
	}

	var content string
	if err := beginTx(ctx, func(ctx context.Context, q IndexQueries) error {
		version, err := q.GetPolicyVersionForUpdate(ctx, versionID)
		if err != nil {
			return err
		}
		if version.Status != "DRAFT" || version.IndexStatus != "PROCESSING" {
			return ErrVersionNotIndexable
		}
		if !sameUUID(version.IndexAttemptID, attemptID) {
			return ErrStaleAttempt
		}
		content = version.Content
		return nil
	}); err != nil {
		return err
	}

	drafts := chunking.ChunkPolicy(content)
	texts := make([]string, len(drafts))
	for i, draft := range drafts {
		texts[i] = draft.Content
	}
	vectors, embedErr := embedder.EmbedDocuments(ctx, texts)
	if embedErr == nil && len(vectors) != len(drafts) {
		embedErr = fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(drafts))
	}

	if embedErr != nil {
		if err := beginTx(ctx, func(ctx context.Context, q IndexQueries) error {
			if err := validateCurrentAttempt(ctx, q, versionID, attemptID); err != nil {
				return err
			}
			_, err := q.CompletePolicyVersionIndex(ctx, db.CompletePolicyVersionIndexParams{
				ID:             versionID,
				IndexStatus:    "FAILED",
				IndexError:     pgtype.Text{String: embedErr.Error(), Valid: true},
				IndexAttemptID: attemptID,
			})
			return err
		}); err != nil {
			return err
		}
		return embedErr
	}

	return beginTx(ctx, func(ctx context.Context, q IndexQueries) error {
		if err := validateCurrentAttempt(ctx, q, versionID, attemptID); err != nil {
			return err
		}
		if err := q.DeletePolicyChunks(ctx, versionID); err != nil {
			return err
		}
		for i, draft := range drafts {
			id, err := newUUID()
			if err != nil {
				return fmt.Errorf("generate policy chunk ID: %w", err)
			}
			// policy_chunks.section is VARCHAR(150). Truncate by Unicode rune so
			// multibyte headings are not split or measured as raw bytes.
			section := truncateRunes(draft.Section, maxSectionRunes)
			if _, err := q.CreatePolicyChunk(ctx, db.CreatePolicyChunkParams{
				ID:              id,
				PolicyVersionID: versionID,
				Section:         pgtype.Text{String: section, Valid: section != ""},
				ChunkIndex:      int32(draft.Index),
				Content:         draft.Content,
				Embedding:       vectorLiteral(vectors[i]),
			}); err != nil {
				return err
			}
		}
		_, err := q.CompletePolicyVersionIndex(ctx, db.CompletePolicyVersionIndexParams{
			ID:             versionID,
			IndexStatus:    "READY",
			IndexError:     pgtype.Text{},
			IndexAttemptID: attemptID,
		})
		return err
	})
}

func validateCurrentAttempt(ctx context.Context, q IndexQueries, versionID, attemptID pgtype.UUID) error {
	version, err := q.GetPolicyVersionForUpdate(ctx, versionID)
	if err != nil {
		return err
	}
	if version.Status != "DRAFT" || version.IndexStatus != "PROCESSING" || !sameUUID(version.IndexAttemptID, attemptID) {
		return ErrStaleAttempt
	}
	return nil
}

func sameUUID(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && a.Bytes == b.Bytes
}

func newUUID() (pgtype.UUID, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return pgtype.UUID{}, err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// vectorLiteral returns the text representation accepted by pgvector.
func vectorLiteral(vector []float32) string {
	values := make([]string, len(vector))
	for i, value := range vector {
		values[i] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	return "[" + strings.Join(values, ",") + "]"
}
