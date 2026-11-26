package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/sched"
)

var reviewRun = model.Run{ID: "t1-1", TaskID: "t1"}

// reviewSeam builds fanind's reviewer over the forges of dir on a recording runner.
func reviewSeam(t *testing.T, dir string, env map[string]string) (*reviews, config.Config, *testutil.RecordingRunner) {
	t.Helper()
	f, cfg, r, err := forgeSeam(t, dir, env)
	if err != nil {
		t.Fatal(err)
	}
	return newReviews(f, r, clock.NewManual(t0), slog.New(slog.DiscardHandler), nil), cfg, r
}

// see SCENARIOS OC-3
func TestDaemonReviewer(t *testing.T) {
	t.Run("OC-3_schedDeps_passes_the_reviewer", func(t *testing.T) {
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
		if _, ok := sd.Reviewer.(*reviews); !ok {
			t.Errorf("Reviewer = %T, want *reviews", sd.Reviewer)
		}
	})

	t.Run("OC-3_a_review_reads_the_PR_of_the_origin_repo_and_an_unreadable_PR_posts_nothing", func(t *testing.T) {
		rv, cfg, r := reviewSeam(t, faninConfig(t), nil)
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{ExitCode: 1, Stderr: "HTTP 502"})
		if _, err := rv.Review(t.Context(), betaOf(t, cfg), reviewRun, ghPRNumber); err == nil {
			t.Error("Review = nil, want the unreadable PR")
		}
	})

	t.Run("OC-3_a_project_without_review_model_is_an_error_before_any_forge_call", func(t *testing.T) {
		rv, cfg, r := reviewSeam(t, faninConfig(t), nil)
		r.Expect(originArgv, origin)
		p := betaOf(t, cfg)
		p.ReviewModel = ""
		if _, err := rv.Review(t.Context(), p, reviewRun, ghPRNumber); !errors.Is(err, runtime.ErrInvalidSpec) {
			t.Errorf("Review = %v, want ErrInvalidSpec", err)
		}
	})

	t.Run("OC-3_an_unreadable_origin_is_an_error", func(t *testing.T) {
		rv, cfg, r := reviewSeam(t, faninConfig(t), nil)
		r.Expect(originArgv, testutil.Reply{ExitCode: 128, Stderr: notARepo})
		if _, err := rv.Review(t.Context(), betaOf(t, cfg), reviewRun, ghPRNumber); err == nil {
			t.Error("Review = nil, want the unreadable origin")
		}
	})

	t.Run("OC-3_a_gitea_project_is_not_reviewed_and_runs_nothing", func(t *testing.T) {
		dir := faninConfig(t)
		forges := "forges:\n  github:\n    kind: github\n    token_env: GH_TOKEN\n" +
			"  forgejo:\n    kind: gitea\n    url: https://git.example.com\n    token_env: TEST_GITEA_TOKEN\n"
		if err := os.WriteFile(filepath.Join(dir, "forges.yaml"), []byte(forges), 0o600); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "projects", projectBeta+".yaml")
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), "forge: github", "forge: forgejo", 1))
		if err := os.WriteFile(file, body, 0o600); err != nil {
			t.Fatal(err)
		}
		rv, cfg, _ := reviewSeam(t, dir, map[string]string{"TEST_GITEA_TOKEN": "x"})
		if _, err := rv.Review(t.Context(), betaOf(t, cfg), reviewRun, ghPRNumber); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("Review = %v, want ErrInvalidRequest", err)
		}
	})
}
