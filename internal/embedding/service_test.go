package embedding

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

type fakeIndexQueries struct {
	versions []db.PolicyVersion
	getCalls int

	deleteCalls  int
	deleteIDs    []pgtype.UUID
	createArgs   []db.CreatePolicyChunkParams
	completeArgs []db.CompletePolicyVersionIndexParams
}

var _ IndexQueries = (*fakeIndexQueries)(nil)

func (f *fakeIndexQueries) GetPolicyVersionForUpdate(context.Context, pgtype.UUID) (db.PolicyVersion, error) {
	index := f.getCalls
	f.getCalls++
	if index >= len(f.versions) {
		index = len(f.versions) - 1
	}
	return f.versions[index], nil
}

func (f *fakeIndexQueries) DeletePolicyChunks(_ context.Context, id pgtype.UUID) error {
	f.deleteCalls++
	f.deleteIDs = append(f.deleteIDs, id)
	return nil
}

func (f *fakeIndexQueries) CreatePolicyChunk(_ context.Context, arg db.CreatePolicyChunkParams) (db.PolicyChunk, error) {
	f.createArgs = append(f.createArgs, arg)
	return db.PolicyChunk{
		ID:              arg.ID,
		PolicyVersionID: arg.PolicyVersionID,
		Section:         arg.Section,
		ChunkIndex:      arg.ChunkIndex,
		Content:         arg.Content,
		Embedding:       pgtype.Text{String: arg.Embedding, Valid: true},
	}, nil
}

func (f *fakeIndexQueries) CompletePolicyVersionIndex(_ context.Context, arg db.CompletePolicyVersionIndexParams) (db.PolicyVersion, error) {
	f.completeArgs = append(f.completeArgs, arg)
	return db.PolicyVersion{ID: arg.ID, IndexStatus: arg.IndexStatus, IndexError: arg.IndexError}, nil
}

func (f *fakeIndexQueries) beginTx(ctx context.Context, fn func(context.Context, IndexQueries) error) error {
	return fn(ctx, f)
}

type fakeEmbedder struct {
	vectors [][]float32
	err     error
	texts   []string
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.texts = append([]string(nil), texts...)
	if f.err != nil {
		return nil, f.err
	}
	if f.vectors != nil {
		return f.vectors, nil
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = []float32{float32(i) + 0.25, float32(i) + 0.5}
	}
	return vectors, nil
}

func TestIndexPolicyVersionSuccess(t *testing.T) {
	versionID := testUUID(1)
	attemptID := testUUID(2)
	version := processingVersion(versionID, attemptID, "# First\nalpha\n\n# Second\nbeta")
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{version, version}}
	embedder := &fakeEmbedder{}

	if err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, attemptID, embedder); err != nil {
		t.Fatalf("IndexPolicyVersion() error = %v", err)
	}
	if queries.deleteCalls != 1 || len(queries.deleteIDs) != 1 || queries.deleteIDs[0] != versionID {
		t.Errorf("deletes = %d, IDs = %v", queries.deleteCalls, queries.deleteIDs)
	}
	if len(queries.createArgs) != 2 {
		t.Fatalf("CreatePolicyChunk calls = %d, want 2", len(queries.createArgs))
	}
	wantTexts := []string{"# First\nalpha", "# Second\nbeta"}
	if !reflect.DeepEqual(embedder.texts, wantTexts) {
		t.Errorf("embedded texts = %#v, want %#v", embedder.texts, wantTexts)
	}
	for i, arg := range queries.createArgs {
		if arg.ChunkIndex != int32(i) {
			t.Errorf("chunk %d index = %d", i, arg.ChunkIndex)
		}
		if arg.Content != embedder.texts[i] {
			t.Errorf("chunk %d content = %q, want %q", i, arg.Content, embedder.texts[i])
		}
		if !strings.HasPrefix(arg.Embedding, "[") || !strings.HasSuffix(arg.Embedding, "]") {
			t.Errorf("chunk %d embedding = %q", i, arg.Embedding)
		}
	}
	if len(queries.completeArgs) != 1 {
		t.Fatalf("CompletePolicyVersionIndex calls = %d, want 1", len(queries.completeArgs))
	}
	complete := queries.completeArgs[0]
	if complete.IndexStatus != "READY" || complete.IndexError.Valid || complete.IndexAttemptID != attemptID {
		t.Errorf("complete args = %+v", complete)
	}
}

func TestIndexPolicyVersionRejectsStaleAttemptAtPhaseOne(t *testing.T) {
	versionID := testUUID(1)
	version := processingVersion(versionID, testUUID(3), "body")
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{version}}
	embedder := &fakeEmbedder{}

	err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, testUUID(2), embedder)
	if !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("error = %v, want ErrStaleAttempt", err)
	}
	assertNoMutations(t, queries)
	if embedder.texts != nil {
		t.Errorf("embedder called with %v", embedder.texts)
	}
}

func TestIndexPolicyVersionRejectsNonProcessingVersion(t *testing.T) {
	versionID := testUUID(1)
	attemptID := testUUID(2)
	version := processingVersion(versionID, attemptID, "body")
	version.IndexStatus = "READY"
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{version}}

	err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, attemptID, &fakeEmbedder{})
	if !errors.Is(err, ErrVersionNotIndexable) {
		t.Fatalf("error = %v, want ErrVersionNotIndexable", err)
	}
	assertNoMutations(t, queries)
}

func TestIndexPolicyVersionRecordsEmbeddingFailure(t *testing.T) {
	versionID := testUUID(1)
	attemptID := testUUID(2)
	version := processingVersion(versionID, attemptID, "body")
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{version, version}}
	providerErr := errors.New("Vertex unavailable")

	err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, attemptID, &fakeEmbedder{err: providerErr})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
	if queries.deleteCalls != 0 || len(queries.createArgs) != 0 {
		t.Errorf("delete calls = %d, create calls = %d", queries.deleteCalls, len(queries.createArgs))
	}
	if len(queries.completeArgs) != 1 {
		t.Fatalf("complete calls = %d, want 1", len(queries.completeArgs))
	}
	complete := queries.completeArgs[0]
	if complete.IndexStatus != "FAILED" || !complete.IndexError.Valid || complete.IndexError.String != providerErr.Error() || complete.IndexAttemptID != attemptID {
		t.Errorf("complete args = %+v", complete)
	}
}

func TestIndexPolicyVersionDoesNotRecordLateEmbeddingFailure(t *testing.T) {
	versionID := testUUID(1)
	attemptID := testUUID(2)
	current := processingVersion(versionID, attemptID, "body")
	stale := processingVersion(versionID, testUUID(3), "body")
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{current, stale}}

	err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, attemptID, &fakeEmbedder{err: errors.New("timeout")})
	if !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("error = %v, want ErrStaleAttempt", err)
	}
	assertNoMutations(t, queries)
}

func TestIndexPolicyVersionTruncatesSectionByRunes(t *testing.T) {
	versionID := testUUID(1)
	attemptID := testUUID(2)
	section := strings.Repeat("界", 200)
	version := processingVersion(versionID, attemptID, "# "+section+"\nbody")
	queries := &fakeIndexQueries{versions: []db.PolicyVersion{version, version}}

	if err := IndexPolicyVersion(context.Background(), queries.beginTx, versionID, attemptID, &fakeEmbedder{}); err != nil {
		t.Fatalf("IndexPolicyVersion() error = %v", err)
	}
	if len(queries.createArgs) != 1 {
		t.Fatalf("create calls = %d, want 1", len(queries.createArgs))
	}
	stored := queries.createArgs[0].Section
	if !stored.Valid || utf8.RuneCountInString(stored.String) != maxSectionRunes {
		t.Errorf("stored section has %d runes, valid=%v", utf8.RuneCountInString(stored.String), stored.Valid)
	}
}

func processingVersion(versionID, attemptID pgtype.UUID, content string) db.PolicyVersion {
	return db.PolicyVersion{
		ID:             versionID,
		Status:         "DRAFT",
		IndexStatus:    "PROCESSING",
		IndexAttemptID: attemptID,
		Content:        content,
	}
}

func testUUID(lastByte byte) pgtype.UUID {
	var id [16]byte
	id[15] = lastByte
	return pgtype.UUID{Bytes: id, Valid: true}
}

func assertNoMutations(t *testing.T, queries *fakeIndexQueries) {
	t.Helper()
	if queries.deleteCalls != 0 || len(queries.createArgs) != 0 || len(queries.completeArgs) != 0 {
		t.Errorf(
			"mutations: delete=%d create=%d complete=%d",
			queries.deleteCalls,
			len(queries.createArgs),
			len(queries.completeArgs),
		)
	}
}
