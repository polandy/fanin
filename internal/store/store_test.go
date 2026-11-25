package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/polandy/fanin/internal/clock"
)

func TestOpenUsesWALAndBusyTimeout(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	var mode string
	var busy int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if mode != walMode || busy != BusyTimeout.Milliseconds() {
		t.Errorf("journal_mode %q busy_timeout %d, want %q %d", mode, busy, walMode, BusyTimeout.Milliseconds())
	}
}

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanin.db")
	clk := clock.NewManual(t0)
	s := openStore(t, path, clk)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path, clk)
	var n int
	if err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_migrations has %d rows, want 1", n)
	}
}

func TestOpenFailures(t *testing.T) {
	good := fstest.MapFS{"migrations/0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	tests := []struct {
		name string
		path string
		fsys fstest.MapFS
		want error
	}{
		{"empty path", "", good, ErrInvalid},
		{"path with query", "x.db?mode=ro", good, ErrInvalid},
		{"failed migration", "", fstest.MapFS{
			"migrations/0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
			"migrations/0002_b.sql": {Data: []byte("CREATE TABLE broken (;")},
		}, ErrMigration},
		{"migration gap", "", fstest.MapFS{"migrations/0002_a.sql": {Data: []byte("SELECT 1;")}}, ErrMigration},
		{"bad migration name", "", fstest.MapFS{"migrations/init.sql": {Data: []byte("SELECT 1;")}}, ErrMigration},
		{"no migration dir", "", fstest.MapFS{}, ErrMigration},
		{"not a directory", filepath.Join(t.TempDir(), "missing", "fanin.db"), good, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if path == "" && tt.want != ErrInvalid {
				path = filepath.Join(t.TempDir(), "fanin.db")
			}
			s, err := open(context.Background(), path, clock.NewManual(t0), tt.fsys)
			if err == nil {
				_ = s.Close()
				t.Fatal("open succeeded, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("open: %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFailedMigrationKeepsEarlierOnesAndRollsBackItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanin.db")
	ctx := context.Background()
	bad := fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"migrations/0002_b.sql": {Data: []byte("CREATE TABLE b (x INTEGER); INSERT INTO nope VALUES (1);")},
	}
	if _, err := open(ctx, path, clock.NewManual(t0), bad); !errors.Is(err, ErrMigration) {
		t.Fatalf("open: %v, want ErrMigration", err)
	}
	fixed := fstest.MapFS{
		"migrations/0001_a.sql": bad["migrations/0001_a.sql"],
		"migrations/0002_b.sql": {Data: []byte("CREATE TABLE b (x INTEGER);")},
	}
	s, err := open(ctx, path, clock.NewManual(t0), fixed)
	if err != nil {
		t.Fatalf("open after fix: %v (table b of the failed run must have rolled back)", err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Errorf("schema version %d, want 2", v)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanin.db")
	ctx := context.Background()
	two := fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"migrations/0002_b.sql": {Data: []byte("CREATE TABLE b (x INTEGER);")},
	}
	s, err := open(ctx, path, clock.NewManual(t0), two)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	one := fstest.MapFS{"migrations/0001_a.sql": two["migrations/0001_a.sql"]}
	if _, err := open(ctx, path, clock.NewManual(t0), one); !errors.Is(err, ErrMigration) {
		t.Errorf("open with an older build: %v, want ErrMigration", err)
	}
}

func TestClosedDatabaseIsAnError(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := LockRequest{Project: project, Holder: "h", Lease: LockPoll}
	checks := map[string]error{
		"Transition": s.Transition(ctx, func(context.Context, *Tx) error { return nil }),
		"OpenTasks":  second(s.OpenTasks(ctx)),
		"Task":       second(s.Task(ctx, "t")),
		"Run":        second(s.Run(ctx, "r")),
		"Runs":       second(s.Runs(ctx, "t")),
		"Events":     second(s.Events(ctx, "t")),
		"Holds":      second(s.Holds(ctx, project)),
		"MergeHold":  third(s.MergeHold(ctx, project, "t")),
		"Timers":     second(s.ArmedTimers(ctx)),
		"Questions":  second(s.OpenQuestions(ctx)),
		"Question":   second(s.Question(ctx, "q")),
		"Acquire":    s.AcquireLock(ctx, req),
		"Wait":       s.WaitLock(ctx, req, LockPoll),
		"Renew":      s.RenewLock(ctx, req),
		"Release":    s.ReleaseLock(ctx, project, "h"),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s on a closed store returned nil, want an error", name)
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrLockNotHeld) {
			t.Errorf("%s on a closed store: %v; an unreadable store is not an answer", name, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

func third[A, B any](_ A, _ B, err error) error { return err }
