package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationDir = "migrations"

// migrationName is NNNN_slug.sql; NNNN counts from 0001 without gaps.
var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrMigration, migrationDir, err)
	}
	var out []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("%w: file %q; name migrations NNNN_slug.sql", ErrMigration, e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("%w: file %q: %w", ErrMigration, e.Name(), err)
		}
		body, err := fs.ReadFile(fsys, migrationDir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %w", ErrMigration, e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("%w: %s has version %d, want %d; number migrations without gaps",
				ErrMigration, m.name, m.version, i+1)
		}
	}
	return out, nil
}

// migrate applies every migration above the stored version, each in its own transaction.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL) STRICT`,
	); err != nil {
		return fmt.Errorf("%w: create schema_migrations: %w", ErrMigration, err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("%w: read schema version: %w", ErrMigration, err)
	}
	if current > len(ms) {
		return fmt.Errorf("%w: database schema version %d is newer than this build knows (%d); upgrade fanin",
			ErrMigration, current, len(ms))
	}
	for _, m := range ms[current:] {
		if err := apply(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: begin: %w", ErrMigration, m.name, err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit; the apply error is the one that matters
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrMigration, m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
		m.version, m.name); err != nil {
		return fmt.Errorf("%w: %s: record version: %w", ErrMigration, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %s: commit: %w", ErrMigration, m.name, err)
	}
	return nil
}
