package database

import (
	"strings"
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected at least one migration")
	}
	for _, migration := range migrations {
		if migration.version < 1 || migration.name == "" {
			t.Fatalf("invalid migration metadata: %+v", migration)
		}
		if strings.TrimSpace(migration.up) == "" || strings.TrimSpace(migration.down) == "" {
			t.Fatalf("migration %d must have up and down SQL", migration.version)
		}
	}
}

func TestInitialMigrationContainsRequiredSchema(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 1 || migrations[0].version != 1 {
		t.Fatalf("migrations = %+v, want one initial migration", migrations)
	}
	sql := strings.ToLower(migrations[0].up)
	for _, required := range []string{
		`create extension if not exists "uuid-ossp"`,
		`create extension if not exists vector`,
		`embedding vector(768) not null`,
		`using hnsw (embedding vector_cosine_ops)`,
		`uq_case_active_maker`,
		`uq_case_active_signer`,
		`uq_case_active_executer`,
		`uq_case_one_active_role_per_user`,
		`unique (case_id, analysis_id)`,
		`unique (event_type, analysis_id)`,
		`scope_type = 'case'`,
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("initial migration does not contain %q", required)
		}
	}
}
