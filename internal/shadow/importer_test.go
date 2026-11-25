package shadow

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/safego"
)

// switchFS serves fsys until broken, then fails every open.
type switchFS struct {
	mu     sync.Mutex
	fsys   fs.FS
	broken bool
}

func (s *switchFS) Open(name string) (fs.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return s.fsys.Open(name)
}

func (s *switchFS) breakIt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken = true
}

func TestImporterRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := clock.NewManual(t0)
	st := openStore(t, clk)
	log, logs := logBuffer()
	files := fstest.MapFS{alphaLog: {Data: []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): p\n")}}
	src := &switchFS{fsys: files}
	waiting := make(chan struct{})
	im := &Importer{Dir: "state", FS: src, Probe: fakeProbe{}, Store: st, Clock: clk, Log: log,
		Waiting: func() { waiting <- struct{}{} }}
	if err := im.Import(ctx); err != nil {
		t.Fatalf("first import: %v", err)
	}
	done := make(chan struct{})
	safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
		im.Run(ctx)
		close(done)
	})
	<-waiting

	t.Run("SH-1_each_interval_imports_the_directory", func(t *testing.T) {
		files[alphaLog] = &fstest.MapFile{Data: []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): p\n" +
			"2026-01-02 03:05 pr #7 alpha-1 feat/x\n")}
		clk.Advance(Interval)
		<-waiting
		if got := dump(t, st, alpha); got != "alpha shadow-alpha-1 gating model=sonnet owner=false run1=working\n" {
			t.Errorf("store after a tick:\n%s", got)
		}
	})
	t.Run("SH-4_a_failed_import_leaves_the_store_and_logs", func(t *testing.T) {
		seq := lastSeq(t, st)
		src.breakIt()
		clk.Advance(Interval)
		<-waiting
		if got := lastSeq(t, st); got != seq {
			t.Errorf("last event %d after a failed import, want %d", got, seq)
		}
		if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "state") {
			t.Errorf("log %q, want an error naming the directory", logs.String())
		}
	})
	cancel()
	<-done
}

func TestImporterImportNamesTheDirectory(t *testing.T) {
	// see SCENARIOS SH-4
	st := openStore(t, clock.NewManual(t0))
	im := &Importer{Dir: "testdata/none", FS: os.DirFS("testdata/none"), Probe: fakeProbe{}, Store: st,
		Clock: clock.NewManual(t0), Log: slog.New(slog.DiscardHandler)}
	err := im.Import(context.Background())
	if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "testdata/none") {
		t.Fatalf("Import: %v, want ErrUnreadable naming testdata/none", err)
	}
}

func TestImporterSkipsProjectsFaninSchedules(t *testing.T) {
	st := openStore(t, clock.NewManual(t0))
	files := fstest.MapFS{alphaLog: {Data: []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): p\n")}}
	im := &Importer{Dir: "state", FS: files, Probe: fakeProbe{}, Store: st, Clock: clock.NewManual(t0),
		Log: slog.New(slog.DiscardHandler), Skip: map[string]bool{alpha: true}}
	if err := im.Import(context.Background()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := dump(t, st, alpha); got != "" {
		t.Errorf("store after importing a skipped project:\n%s", got)
	}
}
