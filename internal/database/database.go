package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open validates the configuration and checks connectivity before returning a pool.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL configuration")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		// Driver errors may include connection credentials; do not expose them.
		return nil, errors.New("database connection failed; check DATABASE_URL and PostgreSQL availability")
	}
	return pool, nil
}
