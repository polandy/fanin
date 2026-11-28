package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

const (
	modelOpus = "opus"
	flagTask  = "--task"
)

// startIntakeDaemon serves the API with intake over a real store; alpha is a GitHub project.
func startIntakeDaemon(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clock.NewManual(statusT0))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	cfg := config.Config{
		Forges:      map[string]config.Forge{"gh": {Kind: config.ForgeGitHub}},
		WorkerKinds: map[string]config.WorkerKind{"claude": {Models: []string{modelOpus}}, intake.KindOpencode: {}},
		Projects:    []config.Project{{Name: projAlpha, Forge: "gh", WorkerKind: "claude"}},
	}
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	h, err := api.New(api.Config{Source: st, OwnerToken: statusToken, Clock: clock.NewManual(statusT0), Registry: reg,
		Projects: cfg.ProjectNames(), Intake: intake.New(cfg, st)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return st, srv.URL
}

func taskRun(t *testing.T, url string, args ...string) (int, string, string) {
	t.Helper()
	return runArgs(t, defaultRegistry, map[string]string{envURL: url, envToken: statusToken}, args...)
}

func queued(t *testing.T, st *store.Store) []model.Task {
	t.Helper()
	ts, err := st.OpenTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestTaskAddCommand(t *testing.T) {
	t.Run("PK-6_owner_task_is_queued_and_printed", func(t *testing.T) {
		st, url := startIntakeDaemon(t)
		code, out, errOut := taskRun(t, url, "task", "add", "--project", projAlpha, flagTask, "fix it", "--model", modelOpus)
		ts := queued(t, st)
		if code != cli.ExitOK || len(ts) != 1 || !ts[0].OwnerTask || out != string(ts[0].ID)+"\n" {
			t.Errorf("exit %d, stdout %q, stderr %q, tasks %+v", code, out, errOut, ts)
		}
	})
	t.Run("PK-6_analyze_makes_an_analysis", func(t *testing.T) {
		st, url := startIntakeDaemon(t)
		code, _, errOut := taskRun(t, url, "task", "add", "--project", projAlpha, "--analyze", "why?")
		ts := queued(t, st)
		if code != cli.ExitOK || len(ts) != 1 || ts[0].Shape != model.ShapeAnalysis {
			t.Errorf("exit %d, stderr %q, tasks %+v", code, errOut, ts)
		}
	})
	t.Run("SC-1_allow_sets_the_scope", func(t *testing.T) {
		st, url := startIntakeDaemon(t)
		code, _, errOut := taskRun(t, url, "task", "add", "--project", projAlpha, flagTask, "a", "--allow",
			"internal/scope/", "--allow", "docs/**")
		ts := queued(t, st)
		if code != cli.ExitOK || len(ts) != 1 || strings.Join(ts[0].AllowedPaths, ",") != "internal/scope/,docs/**" {
			t.Errorf("exit %d, stderr %q, tasks %+v", code, errOut, ts)
		}
	})
	t.Run("SC-1_an_absolute_allow_is_a_usage_error", func(t *testing.T) {
		st, url := startIntakeDaemon(t)
		code, _, errOut := taskRun(t, url, "task", "add", "--project", projAlpha, flagTask, "a", "--allow", "/etc")
		if code != cli.ExitUsage || len(queued(t, st)) != 0 || !strings.Contains(errOut, "absolute") {
			t.Errorf("exit %d, stderr %q; want usage error 2 naming the absolute path, nothing queued", code, errOut)
		}
	})
	for name, args := range map[string][]string{
		"task_with_analyze":      {flagTask, "a", "--analyze", "b"},
		"opencode_without_task":  {"--analyze", "b", "--worker", intake.KindOpencode},
		"unknown_model":          {flagTask, "a", "--model", "gpt-0"},
		"neither_task_nor_query": {},
		"no_project":             {flagTask, "a"},
	} {
		t.Run("PK-7_usage_error_"+name, func(t *testing.T) {
			st, url := startIntakeDaemon(t)
			full := append([]string{"task", "add"}, args...)
			if name != "no_project" {
				full = append(full, "--project", projAlpha)
			}
			code, _, errOut := taskRun(t, url, full...)
			if code != cli.ExitUsage || len(queued(t, st)) != 0 {
				t.Errorf("exit %d, stderr %q, tasks %d; want usage error 2 and nothing queued", code, errOut,
					len(queued(t, st)))
			}
		})
	}
}

func TestTaskImportAndListCommands(t *testing.T) {
	st, url := startIntakeDaemon(t)
	file := filepath.Join(t.TempDir(), "batch.chain")
	write := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# c\nopus|one\n\nopus|two\n")
	code, out, errOut := taskRun(t, url, "task", "import", file, "--project", projAlpha)
	if code != cli.ExitOK || !strings.Contains(out, "2 added, 0 skipped") {
		t.Fatalf("CH-1 import: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	write("# c\nopus|one\n\nopus|two\nopus|three\n")
	code, out, errOut = taskRun(t, url, "task", "import", file, "--project", projAlpha)
	if code != cli.ExitOK || !strings.Contains(out, "1 added, 2 skipped") || len(queued(t, st)) != 3 {
		t.Fatalf("CH-1 re-import: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, _ = taskRun(t, url, "task", "list", "--project", projAlpha)
	if code != cli.ExitOK || !strings.Contains(out, "alpha-batch-5") || !strings.Contains(out, "after alpha-batch-4") {
		t.Errorf("list: exit %d, stdout %q", code, out)
	}
}
