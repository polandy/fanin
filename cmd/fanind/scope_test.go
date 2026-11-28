package main

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/sched"
)

// see SCENARIOS SC-2
func TestDaemonScopes(t *testing.T) {
	t.Run("SC-2_schedDeps_passes_the_forges_as_scope_checks", func(t *testing.T) {
		cfg, err := config.Load(faninConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		d := deps{getenv: func(string) string { return "" }, clock: clock.NewManual(t0),
			runner: testutil.NewRecordingRunner(t),
			workers: func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				return idleWorkers{}, idleWorkers{}
			}}
		sd, err := schedDeps(cfg, nil, nil, slog.New(slog.DiscardHandler), d)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := sd.Scopes.(*forges); !ok {
			t.Errorf("Scopes = %T, want *forges", sd.Scopes)
		}
	})

	t.Run("SC-2_an_unreadable_origin_posts_no_scope", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, testutil.Reply{ExitCode: 128, Stderr: notARepo})
		if st, err := f.PostScope(t.Context(), betaOf(t, cfg), ghPRNumber, "abc123", nil); err == nil {
			t.Errorf("PostScope = %s, nil; want an error", st)
		}
	})

	t.Run("SC-2_a_project_without_loop_fanin_has_no_scope", func(t *testing.T) {
		f, cfg, _, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		alpha, _ := cfg.Project(project)
		if _, err := f.PostScope(t.Context(), alpha, ghPRNumber, "abc123", nil); !errors.Is(err,
			forge.ErrUnknownProject) {
			t.Errorf("PostScope(alpha) = %v, want ErrUnknownProject", err)
		}
	})
}
