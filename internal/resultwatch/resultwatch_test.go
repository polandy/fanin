package resultwatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const validFile = `{"token":"run-secret","kind":"blocked","text":"which database?"}`

var want = faninmcp.ResultFile{Token: "run-secret",
	Report: faninmcp.Report{Kind: faninmcp.KindBlocked, Text: "which database?"}}

// signalClock is a manual clock that signals every After call, so a test knows the watcher waits.
type signalClock struct {
	*clock.Manual
	waiting chan struct{}
}

func (c *signalClock) After(d time.Duration) <-chan time.Time {
	ch := c.Manual.After(d)
	c.waiting <- struct{}{}
	return ch
}

type outcome struct {
	f   faninmcp.ResultFile
	err error
}

func write(t *testing.T, dir, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, faninmcp.ResultFileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start runs Watch in the background on a signalling clock.
func start(ctx context.Context, dir string) (*signalClock, <-chan outcome) {
	clk := &signalClock{Manual: clock.NewManual(time.Unix(0, 0)), waiting: make(chan struct{})}
	done := make(chan outcome, 1)
	safego.Go(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context) {
		f, err := Watch(ctx, clk, dir, 0)
		done <- outcome{f, err}
	})
	return clk, done
}

func TestWatchFileAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, validFile)
	got, err := Watch(context.Background(), clock.NewManual(time.Unix(0, 0)), dir, time.Second)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Watch (-want +got):\n%s", diff)
	}
}

func TestWatchFileAppearsLater(t *testing.T) {
	dir := t.TempDir()
	clk, done := start(context.Background(), dir)
	<-clk.waiting
	clk.Advance(DefaultEvery)
	<-clk.waiting // second look found nothing and waits again
	write(t, dir, validFile)
	clk.Advance(DefaultEvery)
	got := <-done
	if got.err != nil {
		t.Fatalf("Watch: %v", got.err)
	}
	if diff := cmp.Diff(want, got.f); diff != "" {
		t.Errorf("Watch (-want +got):\n%s", diff)
	}
}

func TestWatchStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clk, done := start(ctx, t.TempDir())
	<-clk.waiting
	cancel()
	if got := <-done; !errors.Is(got.err, context.Canceled) {
		t.Errorf("Watch = %v, want context.Canceled", got.err)
	}
}

// An unreadable result.json is an error, never a result.
func TestUnreadableResultFile(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"half_written", func(t *testing.T, dir string) { write(t, dir, `{"token":"run-secret","kind":"bl`) }},
		{"unknown_field", func(t *testing.T, dir string) { write(t, dir, `{"token":"t","kind":"nothing","x":1}`) }},
		{"no_token", func(t *testing.T, dir string) { write(t, dir, `{"kind":"nothing","text":"x"}`) }},
		{"not_a_file", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, faninmcp.ResultFileName), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			f, err := Watch(context.Background(), clock.NewManual(time.Unix(0, 0)), dir, time.Second)
			if !errors.Is(err, ErrUnreadable) || f != (faninmcp.ResultFile{}) {
				t.Errorf("Watch = %+v, %v; want no result and ErrUnreadable", f, err)
			}
		})
	}
}

func TestReadMissingFile(t *testing.T) {
	_, ok, err := Read(t.TempDir())
	if ok || err != nil {
		t.Errorf("Read = ok %v, %v; want not ok and no error", ok, err)
	}
}
