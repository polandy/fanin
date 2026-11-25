// Package store keeps fanin's state in one SQLite file: tasks, runs, events, project locks, holds, timers and
// owner questions. It is the single source of truth (see concept D3, docs/dev/spec/store.md).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/polandy/fanin/internal/clock"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// BusyTimeout is how long a connection waits for another writer before SQLite returns SQLITE_BUSY.
const BusyTimeout = 5 * time.Second

const (
	driverName = "sqlite"
	walMode    = "wal"
)

// The store's sentinels.
var (
	// ErrNotFound is returned when the row asked for does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalid is returned for a record that misses a field the store needs.
	ErrInvalid = errors.New("store: invalid record")
	// ErrCorrupt is returned when a stored value cannot be read back.
	ErrCorrupt = errors.New("store: corrupt row")
	// ErrMigration is returned when the schema cannot be brought up to date.
	ErrMigration = errors.New("store: migration failed")
)

// Store is an open fanin database. It is safe for concurrent use.
type Store struct {
	db    *sql.DB
	clock clock.Clock

	mu       sync.Mutex
	released chan struct{}
	// waiting is called each time WaitLock blocks; tests use it as their signal.
	waiting func()
}

// Open opens or creates the database at path in WAL mode and applies every pending migration.
func Open(ctx context.Context, path string, clk clock.Clock) (*Store, error) {
	return open(ctx, path, clk, migrationFS)
}

func open(ctx context.Context, path string, clk clock.Clock, migrations fs.FS) (*Store, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("%w: database path %q; give a file path without '?' or '#'", ErrInvalid, path)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"+
		"&_txlock=immediate", path, BusyTimeout.Milliseconds())
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection: the store is the only writer, and SQLite serialises writers anyway.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, clock: clk, released: make(chan struct{}), waiting: func() {}}
	if err := s.init(ctx, path, migrations); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

func (s *Store) init(ctx context.Context, path string, migrations fs.FS) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store: open %s: %w", path, err)
	}
	if mode != walMode {
		return fmt.Errorf("store: open %s: journal mode %q, want %q; use a file on a local disk", path, mode, walMode)
	}
	return migrate(ctx, s.db, migrations)
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// querier is what *sql.DB and *sql.Tx share, so reads work inside and outside a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in one write transaction and commits it when fn returns nil.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

func toDB(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromDB(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func boolToDB(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// scanAll reads every row with scan and closes rows.
func scanAll[T any](rows *sql.Rows, scan func(scanner) (T, error)) (out []T, err error) {
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("store: close rows: %w", cerr)
		}
	}()
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: read row: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read rows: %w", err)
	}
	return out, nil
}

func notFound(err error, what, id string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %q", ErrNotFound, what, id)
	}
	return fmt.Errorf("store: read %s %q: %w", what, id, err)
}

func corrupt(what, id string, err error) error {
	return fmt.Errorf("%w: %s %q: %w", ErrCorrupt, what, id, err)
}
