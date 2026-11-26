package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/store"
)

// idleWorkers is a runtime that starts nothing and a picker that finds nothing.
type idleWorkers struct{}

func (idleWorkers) Start(context.Context, runtime.RunSpec) (runtime.RunHandle, error) {
	return runtime.RunHandle{}, runtime.ErrStartFailed
}

func (idleWorkers) Prompt(context.Context, runtime.RunHandle, string) error { return nil }

func (idleWorkers) Stop(context.Context, runtime.RunHandle) error { return nil }

func (idleWorkers) Observe(context.Context, runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	return nil, runtime.ErrStatusUnknown
}

func (idleWorkers) Pick(context.Context, pick.Request) (pick.Result, error) {
	return pick.Result{Idle: true, Reason: "nothing"}, nil
}

// lockedBuffer is a log sink the scheduler goroutine and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// faninConfig writes a config whose project beta has loop: fanin.
func faninConfig(t *testing.T) string {
	t.Helper()
	dir := writeConfig(t, t.TempDir(), "127.0.0.1:0")
	file := filepath.Join(dir, "projects", projectBeta+".yaml")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(body, "loop: "+config.LoopFanin+"\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDaemonSched(t *testing.T) {
	t.Run("LN-2_loop_fanin_runs_the_scheduler_and_skips_the_shadow_import", func(t *testing.T) {
		dir := faninConfig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		logs, ready, done := &lockedBuffer{}, make(chan string, 1), make(chan error, 1)
		var built []string
		d := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] }, logOut: logs,
			clock: clock.NewManual(t0), probe: freeLocks{}, ready: func(addr string) { ready <- addr },
			workers: func(cfg config.Config, _ *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				built = append(built, cfg.Listen)
				return idleWorkers{}, idleWorkers{}
			}}
		safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
			done <- run(ctx, []string{flagConfig, dir, flagShadow, fixtures}, d)
		})
		var addr string
		select {
		case addr = <-ready:
		case err := <-done:
			t.Fatalf("fanind ended before it listened: %v", err)
		}
		snap, err := client.New("http://"+addr, token, nil).Snapshot(ctx)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		for _, h := range snap.Holds {
			if h.Project == projectBeta {
				t.Errorf("hold %s %s imported for a loop: fanin project", h.Project, h.Kind)
			}
		}
		for _, task := range snap.Tasks {
			if strings.HasPrefix(task.ID, "shadow-beta-") {
				t.Errorf("task %s imported for a loop: fanin project", task.ID)
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
		if len(built) != 1 || !strings.Contains(logs.String(), "scheduler running") {
			t.Errorf("workers built %d times, logs:\n%s", len(built), logs)
		}
	})

	t.Run("LN-5_no_loop_fanin_project_starts_no_scheduler", func(t *testing.T) {
		d := start(t, writeConfig(t, t.TempDir(), "127.0.0.1:0"))
		d.cancel()
		if err := <-d.done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
		if strings.Contains(d.logs.String(), "scheduler running") {
			t.Errorf("scheduler ran without a loop: fanin project:\n%s", d.logs)
		}
	})

	t.Run("SP-10_a_scheduler_that_cannot_resume_stops_fanind", func(t *testing.T) {
		dir := faninConfig(t)
		path := filepath.Join(dir, "fanin.db")
		seedLiveRun(t, path)
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(),
			`CREATE TRIGGER fault BEFORE INSERT ON timers BEGIN SELECT RAISE(ABORT, 'injected fault'); END`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		d := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] },
			logOut: &lockedBuffer{}, clock: clock.NewManual(t0), probe: freeLocks{}, ready: func(string) {},
			workers: func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				return idleWorkers{}, idleWorkers{}
			}}
		if err := run(t.Context(), []string{flagConfig, dir}, d); !errors.Is(err, errSchedStopped) {
			t.Errorf("run = %v, want %v", err, errSchedStopped)
		}
	})
}

// seedLiveRun stores a working run of a beta task whose timers are missing, so a resume must arm them.
func seedLiveRun(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(t.Context(), path, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	err = st.Transition(t.Context(), func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "b1", Project: projectBeta, Title: "live", Shape: model.ShapePR,
			OwnerTask: true}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "b1", model.TaskRunning, "slot 1"); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "b1-run-1", TaskID: "b1", Number: 1, Slot: 1,
			Worker: model.Worker{Agent: "b1-1", Workspace: "ws", Tab: "tab", Pane: "pane"}}); err != nil {
			return err
		}
		return tx.SetRunState(ctx, "b1-run-1", model.RunWorking, "started")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

const (
	ghPRNumber   = 7
	ghRepo       = "acme/beta"
	ghViewFields = "number,title,url,state,mergeable,headRefName,headRefOid,baseRefName,statusCheckRollup"
	ghDoneAt     = "2026-11-25T09:05:00Z"
	ghReviewAt   = "2026-11-25T09:06:00Z"
)

var (
	originArgv = []string{"git", "-C", "/path/to/" + projectBeta, "remote", "get-url", "origin"}
	viewArgv   = []string{"gh", "pr", "view", "7", "--repo", ghRepo, "--json", ghViewFields}
	baseArgv   = []string{"gh", "api", "graphql", "-f", "query=" + github.BranchChecksQuery, "-f", "owner=acme",
		"-f", "name=beta", "-f", "ref=refs/heads/main"}
	origin = testutil.Reply{Stdout: "git@github.com:" + ghRepo + ".git\n"}
)

// ghView is the gh pr view answer for PR 7 of beta whose ci check ended with conclusion and whose review passed.
func ghView(conclusion string) string {
	return `{"number":7,"title":"feat: x","url":"https://github.com/acme/beta/pull/7","state":"OPEN",` +
		`"mergeable":"MERGEABLE","headRefName":"feat/x","headRefOid":"abc123","baseRefName":"main",` +
		`"statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"` + conclusion +
		`","startedAt":"` + ghDoneAt + `","completedAt":"` + ghDoneAt + `"},{"__typename":"StatusContext",` +
		`"context":"` + gate.ReviewContext + `","state":"SUCCESS","createdAt":"` + ghReviewAt + `"}]}`
}

// forgeSeam loads the fanin config of dir and builds its forges over a recording runner.
func forgeSeam(t *testing.T, dir string, env map[string]string) (*forges, config.Config, *testutil.RecordingRunner,
	error) {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := testutil.NewRecordingRunner(t)
	t.Cleanup(r.Done)
	f, err := newForges(cfg, r, func(k string) string { return env[k] }, clock.NewManual(t0),
		slog.New(slog.DiscardHandler))
	return f, cfg, r, err
}

func betaOf(t *testing.T, cfg config.Config) config.Project {
	t.Helper()
	p, ok := cfg.Project(projectBeta)
	if !ok {
		t.Fatalf("project %s missing", projectBeta)
	}
	return p
}

func TestDaemonForges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		conclusion string
		want       gate.Verdict
	}{
		{"GH-1_a_green_PR_of_a_fanin_project_reads_green", "SUCCESS", gate.Green},
		{"GH-4_a_failed_check_reads_red", "FAILURE", gate.Red},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Expect(originArgv, origin)
			r.Expect(viewArgv, testutil.Reply{Stdout: ghView(tc.conclusion)})
			reader, target, err := f.For(t.Context(), betaOf(t, cfg), ghPRNumber)
			if err != nil {
				t.Fatal(err)
			}
			want := gate.Target{Kind: forge.KindGitHub, Repo: ghRepo, Number: ghPRNumber, Base: "main"}
			if diff := cmp.Diff(want, target); diff != "" {
				t.Errorf("target (-want +got):\n%s", diff)
			}
			res, err := gate.Read(t.Context(), reader, target)
			if err != nil || res.Verdict != tc.want {
				t.Errorf("gate = %+v, %v; want %s", res, err, tc.want)
			}
		})
	}

	t.Run("GH-5_an_unreadable_PR_is_an_error_never_a_verdict", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{ExitCode: 1, Stderr: "HTTP 502"})
		reader, target, err := f.For(t.Context(), betaOf(t, cfg), ghPRNumber)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := gate.Read(t.Context(), reader, target); !errors.Is(err, gate.ErrUnreadable) {
			t.Errorf("gate = %+v, %v; want ErrUnreadable", res, err)
		}
	})

	t.Run("GH-5_an_unreadable_origin_is_an_error_never_a_target", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, testutil.Reply{ExitCode: 128, Stderr: "fatal: not a git repository"})
		if _, target, err := f.For(t.Context(), betaOf(t, cfg), ghPRNumber); err == nil {
			t.Errorf("For = %+v, nil; want an error", target)
		}
		r.Expect(originArgv, testutil.Reply{ExitCode: 128, Stderr: "fatal: not a git repository"})
		if checks, err := f.BaseChecks(t.Context(), betaOf(t, cfg)); err == nil {
			t.Errorf("BaseChecks = %+v, nil; want an error", checks)
		}
	})

	t.Run("GH-5_a_project_without_loop_fanin_has_no_forge", func(t *testing.T) {
		f, cfg, _, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		alpha, _ := cfg.Project(project)
		if _, _, err := f.For(t.Context(), alpha, ghPRNumber); !errors.Is(err, forge.ErrUnknownProject) {
			t.Errorf("For(alpha) = %v, want ErrUnknownProject", err)
		}
	})

	t.Run("MG-8_base_checks_read_the_head_of_the_base_branch", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(baseArgv, testutil.Reply{Stdout: `{"data":{"repository":{"ref":{"target":{"statusCheckRollup":` +
			`{"contexts":{"pageInfo":{"hasNextPage":false},"nodes":[{"__typename":"CheckRun","name":"ci",` +
			`"status":"COMPLETED","conclusion":"FAILURE","startedAt":"` + ghDoneAt + `","completedAt":"` + ghDoneAt +
			`"}]}}}}}}}`})
		got, err := f.BaseChecks(t.Context(), betaOf(t, cfg))
		at := time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)
		if diff := cmp.Diff([]forge.Check{{Name: "ci", State: forge.CheckFailure, UpdatedAt: at}}, got); err != nil ||
			diff != "" {
			t.Errorf("BaseChecks err %v (-want +got):\n%s", err, diff)
		}
	})

	t.Run("MG-8_unreadable_base_checks_are_an_error", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(baseArgv, testutil.Reply{Stdout: `{"data":`})
		if _, err := f.BaseChecks(t.Context(), betaOf(t, cfg)); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("BaseChecks err = %v, want ErrUnreadable", err)
		}
	})

	t.Run("a_gitea_forge_without_its_token_is_an_error", func(t *testing.T) {
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
		if _, _, _, err := forgeSeam(t, dir, nil); !errors.Is(err, forge.ErrNoSecret) {
			t.Errorf("newForges = %v, want ErrNoSecret", err)
		}
		f, cfg, _, err := forgeSeam(t, dir, map[string]string{"TEST_GITEA_TOKEN": "x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, kind, err := f.res.For(betaOf(t, cfg).Name); err != nil || kind != forge.KindGitea {
			t.Errorf("forge of beta = %s, %v; want %s", kind, err, forge.KindGitea)
		}
	})

	t.Run("GH-1_startSched_passes_the_forges_as_gates_and_base_CI", func(t *testing.T) {
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
		if _, ok := sd.Gates.(*forges); !ok {
			t.Errorf("Gates = %T, want *forges", sd.Gates)
		}
		if _, ok := sd.BaseCI.(*forges); !ok {
			t.Errorf("BaseCI = %T, want *forges", sd.BaseCI)
		}
	})
}
