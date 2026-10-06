package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
)

func TestReadWriteProof(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		t.Skip("set TEST_DATABASE_URL or DATABASE_URL to a local PostgreSQL database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	queries := New(tx)
	wantID := pgtype.UUID{Bytes: [16]byte{0x00, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, Valid: true}
	created, err := queries.CreateUnit(ctx, CreateUnitParams{
		ID:          wantID,
		Code:        "SQLC_PROOF",
		Name:        "sqlc proof unit",
		Description: pgtype.Text{String: "typed repository proof", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := queries.GetUnit(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Code != "SQLC_PROOF" || read.Name != "sqlc proof unit" {
		t.Fatalf("read unit = %+v", read)
	}
}
