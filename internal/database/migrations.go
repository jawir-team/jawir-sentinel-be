package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The migration files are embedded so the API binary can migrate a database
// without depending on its source tree at runtime.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

var migrationFilePattern = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.(up|down)\.sql$`)

type migration struct {
	version int64
	name    string
	up      string
	down    string
}

// Migrate applies every migration that has not yet been recorded.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("database pool is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration tracking table: %w", err)
	}

	applied, err := appliedMigrations(ctx, pool)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		appliedName, exists := applied[migration.version]
		if exists {
			if appliedName != migration.name {
				return fmt.Errorf("migration version %d is recorded as %q, want %q", migration.version, appliedName, migration.name)
			}
			continue
		}
		if err := applyMigration(ctx, pool, migration); err != nil {
			return err
		}
	}
	return nil
}

// Rollback reverts the most recently applied migration.
func Rollback(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("database pool is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration tracking table: %w", err)
	}

	var version int64
	var name string
	err = pool.QueryRow(ctx, `
		SELECT version, name
		FROM schema_migrations
		ORDER BY version DESC
		LIMIT 1`).Scan(&version, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read latest migration: %w", err)
	}

	var target *migration
	for i := range migrations {
		if migrations[i].version == version {
			target = &migrations[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("migration %d (%s) is recorded but missing from the binary", version, name)
	}
	if target.name != name {
		return fmt.Errorf("migration version %d is recorded as %q, want %q", version, name, target.name)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration rollback: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, target.down); err != nil {
		return fmt.Errorf("rollback migration %d (%s): %w", target.version, target.name, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, target.version); err != nil {
		return fmt.Errorf("record migration rollback %d: %w", target.version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration rollback %d: %w", target.version, err)
	}
	return nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, migration migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %d (%s): %w", migration.version, migration.name, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, migration.up); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO schema_migrations (version, name)
		VALUES ($1, $2)`, migration.version, migration.name); err != nil {
		return fmt.Errorf("record migration %d (%s): %w", migration.version, migration.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %d (%s): %w", migration.version, migration.name, err)
	}
	return nil
}

func appliedMigrations(ctx context.Context, pool *pgxpool.Pool) (map[int64]string, error) {
	rows, err := pool.Query(ctx, `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	return applied, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	byVersion := make(map[int64]*migration)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		m, exists := byVersion[version]
		if !exists {
			item := &migration{version: version, name: match[2]}
			byVersion[version] = item
			m = item
		} else if m.name != match[2] {
			return nil, fmt.Errorf("migration version %d has inconsistent names", version)
		}
		contents, err := fs.ReadFile(migrationFS, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("migration %q is empty", entry.Name())
		}
		switch match[3] {
		case "up":
			if m.up != "" {
				return nil, fmt.Errorf("duplicate up migration for version %d", version)
			}
			m.up = string(contents)
		case "down":
			if m.down != "" {
				return nil, fmt.Errorf("duplicate down migration for version %d", version)
			}
			m.down = string(contents)
		}
	}

	result := make([]migration, 0, len(byVersion))
	for _, migration := range byVersion {
		if migration.up == "" || migration.down == "" {
			return nil, fmt.Errorf("migration %d (%s) must have both up and down files", migration.version, migration.name)
		}
		result = append(result, *migration)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}
