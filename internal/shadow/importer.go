package shadow

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/store"
)

// Interval is the time between two imports.
const Interval = 30 * time.Second

// LockLease is the lease of an imported merge lock: a stopped fanind lets it expire after three missed imports.
const LockLease = 3 * Interval

// Importer imports one bash state directory into the store every Interval.
type Importer struct {
	// Dir names the state directory in errors and logs; FS and Probe read it.
	Dir   string
	FS    fs.FS
	Probe Probe
	Store *store.Store
	Clock clock.Clock
	Log   *slog.Logger
	// Waiting, when set, is called each time Run has armed the next tick.
	Waiting func()
	// Skip names the projects fanind schedules itself (loop: fanin); their bash state is not imported.
	Skip map[string]bool
}

// New returns the importer of the state directory dir.
func New(dir string, st *store.Store, clk clock.Clock, log *slog.Logger) *Importer {
	return &Importer{Dir: dir, FS: os.DirFS(dir), Probe: ProcLocks{Dir: dir, Locks: ProcLocksPath}, Store: st,
		Clock: clk, Log: log}
}

// Import reads the directory and writes it to the store; an unreadable directory writes nothing.
func (im *Importer) Import(ctx context.Context) error {
	projects, err := Read(im.FS, im.Probe)
	if err != nil {
		return fmt.Errorf("shadow: import %s: %w", im.Dir, err)
	}
	projects = slices.DeleteFunc(projects, func(p Project) bool { return im.Skip[p.Label] })
	if err := Apply(ctx, im.Store, im.Log, projects, LockLease); err != nil {
		return fmt.Errorf("shadow: import %s: %w", im.Dir, err)
	}
	return nil
}

// Run imports every Interval until ctx ends; a failed import is logged unless ctx ended, and the next tick retries.
func (im *Importer) Run(ctx context.Context) {
	for {
		tick := im.Clock.After(Interval)
		if im.Waiting != nil {
			im.Waiting()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		if err := im.Import(ctx); err != nil && ctx.Err() == nil {
			im.Log.ErrorContext(ctx, "shadow: import failed", "dir", im.Dir, "err", err)
		}
	}
}
