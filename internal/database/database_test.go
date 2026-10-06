package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestInvalidURL(t *testing.T) {
	for _, url := range []string{"", "postgres://user:secret@host:invalid/db"} {
		pool, err := Open(context.Background(), url)
		if pool != nil {
			pool.Close()
		}
		if err == nil || pool != nil {
			t.Fatal("invalid configuration must return an error and no pool")
		}
	}
}

func TestPostgresVector(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a local test PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		t.Fatal(err)
	}
	var distance float64
	if err := tx.QueryRow(ctx, "SELECT '[1,0,0]'::vector <=> '[1,0,0]'::vector").Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if distance != 0 {
		t.Fatalf("cosine distance = %v, want 0", distance)
	}
	// Rollback leaves extension activation to the BE-003 migrations.
}

func TestMigrationRoundTrip(t *testing.T) {
	url := migrationTestURL()
	if url == "" {
		t.Skip("set TEST_DATABASE_URL or DATABASE_URL to a dedicated local test PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var tableCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		AND table_name IN ('cases', 'policy_chunks', 'outbox_events', 'audit_events')`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 4 {
		t.Fatalf("migrated table count = %d, want 4", tableCount)
	}
	if err := Rollback(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("applied migrations after rollback = %d, want 0", applied)
	}
}

func TestMigrationConstraints(t *testing.T) {
	url := migrationTestURL()
	if url == "" {
		t.Skip("set TEST_DATABASE_URL or DATABASE_URL to a dedicated local test PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := Rollback(context.Background(), pool); err != nil {
			t.Errorf("rollback constraint test schema: %v", err)
		}
	}()

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustReject := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err == nil {
			t.Fatalf("query unexpectedly succeeded: %s", query)
		}
	}

	unitID := "00000000-0000-0000-0000-000000000001"
	userIDs := []string{
		"00000000-0000-0000-0000-000000000011",
		"00000000-0000-0000-0000-000000000012",
		"00000000-0000-0000-0000-000000000013",
		"00000000-0000-0000-0000-000000000014",
	}
	caseID := "00000000-0000-0000-0000-000000000021"
	analysisID := "00000000-0000-0000-0000-000000000031"
	policyID := "00000000-0000-0000-0000-000000000041"
	policyVersionID := "00000000-0000-0000-0000-000000000051"
	caseTypeID := "00000000-0000-0000-0000-000000000061"
	executionID := "00000000-0000-0000-0000-000000000071"
	outboxID := "00000000-0000-0000-0000-000000000081"

	mustExec(`INSERT INTO units (id, code, name) VALUES ($1, 'TEST', 'Test Unit')`, unitID)
	mustExec(`INSERT INTO case_types (id, code, name) VALUES ($1, 'TEST_CASE', 'Test Case')`, caseTypeID)
	for _, userID := range userIDs {
		mustExec(`INSERT INTO users (id, unit_id, firebase_uid, name, email) VALUES ($1, $2, $3, $4, $5)`,
			userID, unitID, "firebase-test-"+userID, "Test User "+userID, userID+"@example.test")
	}
	mustExec(`INSERT INTO cases (id, case_number, case_type_id, title, description, urgency, created_by, owner_id)
		VALUES ($1, 'TEST-001', $2, 'Test case', 'Test description', 'LOW', $3, $3)`,
		caseID, caseTypeID, userIDs[0])

	// Two active Checkers are valid, while a second active Maker and a second
	// active role for one user are rejected by the partial unique indexes.
	mustExec(`INSERT INTO case_participants (id, case_id, user_id, role, assigned_by) VALUES
		('00000000-0000-0000-0000-000000000091', $1, $2, 'MAKER', $2),
		('00000000-0000-0000-0000-000000000092', $1, $3, 'CHECKER', $2),
		('00000000-0000-0000-0000-000000000093', $1, $4, 'CHECKER', $2)`,
		caseID, userIDs[0], userIDs[1], userIDs[2])
	mustReject(`INSERT INTO case_participants (id, case_id, user_id, role, assigned_by)
		VALUES ('00000000-0000-0000-0000-000000000094', $1, $2, 'MAKER', $2)`, caseID, userIDs[3])
	mustReject(`INSERT INTO case_participants (id, case_id, user_id, role, assigned_by)
		VALUES ('00000000-0000-0000-0000-000000000095', $1, $2, 'SIGNER', $2)`, caseID, userIDs[0])

	mustExec(`INSERT INTO policies (id, code, title, domain) VALUES ($1, 'TEST-POLICY', 'Test Policy', 'TEST')`, policyID)
	mustExec(`INSERT INTO policy_versions (id, policy_id, version, content, created_by)
		VALUES ($1, $2, '1.0', 'test policy', $3)`, policyVersionID, policyID, userIDs[0])
	mustExec(`INSERT INTO policy_versions
		(id, policy_id, version, status, index_status, index_attempt_id, index_started_at, content, created_by)
		VALUES ('00000000-0000-0000-0000-000000000052', $1, '2.0', 'DRAFT', 'PROCESSING',
		'00000000-0000-0000-0000-000000000053', now(), 'processing policy', $2)`, policyID, userIDs[0])
	var indexAttempt string
	if err := pool.QueryRow(ctx, `SELECT index_attempt_id::text FROM policy_versions WHERE id = '00000000-0000-0000-0000-000000000052'`).Scan(&indexAttempt); err != nil {
		t.Fatal(err)
	}
	if indexAttempt == "" {
		t.Fatal("policy index attempt was not persisted")
	}
	mustReject(`INSERT INTO policy_versions
		(id, policy_id, version, status, content, created_by)
		VALUES ('00000000-0000-0000-0000-000000000054', $1, '3.0', 'ACTIVE', 'not indexed', $2)`, policyID, userIDs[0])

	mustExec(`INSERT INTO policy_chunks (id, policy_version_id, chunk_index, content, embedding)
		VALUES ('00000000-0000-0000-0000-000000000055', $1, 0, 'chunk',
			('[' || repeat('0,', 767) || '0]')::vector)`, policyVersionID)
	var vectorType string
	if err := pool.QueryRow(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = 'policy_chunks'::regclass AND a.attname = 'embedding' AND NOT a.attisdropped`).Scan(&vectorType); err != nil {
		t.Fatal(err)
	}
	if vectorType != "vector(768)" {
		t.Fatalf("embedding type = %q, want vector(768)", vectorType)
	}

	mustExec(`INSERT INTO ai_analyses (id, case_id, version, status, model_name, prompt_version)
		VALUES ($1, $2, 1, 'GENERATING', 'test-model', 'test-prompt')`, analysisID, caseID)
	mustExec(`INSERT INTO executions (id, case_id, analysis_id, executer_id, status)
		VALUES ($1, $2, $3, $4, 'IN_PROGRESS')`, executionID, caseID, analysisID, userIDs[3])
	mustReject(`INSERT INTO executions (id, case_id, analysis_id, executer_id, status)
		VALUES ('00000000-0000-0000-0000-000000000072', $1, $2, $3, 'IN_PROGRESS')`, caseID, analysisID, userIDs[3])

	mustExec(`INSERT INTO outbox_events (id, case_id, analysis_id, event_type) VALUES ($1, $2, $3, 'AI_ANALYSIS_REQUESTED')`, outboxID, caseID, analysisID)
	mustReject(`INSERT INTO outbox_events (id, case_id, analysis_id, event_type) VALUES
		('00000000-0000-0000-0000-000000000082', $1, $2, 'AI_ANALYSIS_REQUESTED')`, caseID, analysisID)

	mustReject(`INSERT INTO audit_events (id, scope_type, event_type) VALUES
		('00000000-0000-0000-0000-0000000000a1', 'CASE', 'CASE_CREATED')`)
	mustExec(`INSERT INTO audit_events (id, scope_type, case_id, event_type) VALUES
		('00000000-0000-0000-0000-0000000000a2', 'CASE', $1, 'CASE_CREATED')`, caseID)
	mustExec(`INSERT INTO audit_events (id, scope_type, policy_id, policy_version_id, event_type) VALUES
		('00000000-0000-0000-0000-0000000000a3', 'POLICY', $1, $2, 'POLICY_VERSION_CREATED')`, policyID, policyVersionID)
	mustReject(`INSERT INTO audit_events (id, scope_type, case_id, policy_id, event_type) VALUES
		('00000000-0000-0000-0000-0000000000a4', 'CASE', $1, $2, 'CASE_CREATED')`, caseID, policyID)
}

func migrationTestURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return os.Getenv("DATABASE_URL")
}
