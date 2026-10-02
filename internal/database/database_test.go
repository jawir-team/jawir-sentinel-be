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
