package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/shadow"
)

const (
	flagShadow = "--shadow"
	fixtures   = "../../internal/shadow/testdata/state"
)

// freeLocks reports every merge lock as free.
type freeLocks struct{}

func (freeLocks) Held(string) (bool, error) { return false, nil }

func TestDaemonShadow(t *testing.T) {
	t.Run("SH-6_status_shows_the_imported_state", func(t *testing.T) {
		d := start(t, writeConfig(t, t.TempDir(), "127.0.0.1:0"), flagShadow, fixtures)
		snap, err := client.New(d.url, token, nil).Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if diff := cmp.Diff([]string{project, "beta", "delta", "gamma"}, snap.Projects); diff != "" {
			t.Errorf("projects (-want +got):\n%s", diff)
		}
		states := map[string]string{}
		for _, task := range snap.Tasks {
			states[task.ID] = task.State
		}
		if states["shadow-delta-183"] != string(model.TaskGating) {
			t.Errorf("task shadow-delta-183 = %q, want gating after the owner's go", states["shadow-delta-183"])
		}
		var holds []string
		for _, h := range snap.Holds {
			holds = append(holds, h.Project+" "+h.Kind)
		}
		slices.Sort(holds)
		if diff := cmp.Diff([]string{"beta merge", "gamma ci"}, holds); diff != "" {
			t.Errorf("holds (-want +got):\n%s", diff)
		}
		d.cancel()
		if err := <-d.done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
	})
	t.Run("SH-6_a_failed_first_import_refuses_to_start", func(t *testing.T) {
		dir := t.TempDir()
		deps := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] },
			logOut: &bytes.Buffer{}, clock: clock.NewManual(t0), probe: freeLocks{},
			ready: func(string) { t.Error("ready called for a refused start") }}
		err := run(context.Background(), []string{flagConfig, writeConfig(t, dir, "127.0.0.1:0"), flagShadow,
			"testdata/none"}, deps)
		if !errors.Is(err, shadow.ErrUnreadable) || !strings.Contains(err.Error(), "testdata/none") {
			t.Errorf("run: %v, want ErrUnreadable naming testdata/none", err)
		}
	})
}
