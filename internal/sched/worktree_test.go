package sched

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/worktree"
)

var errWorktree = errors.New("fatal: no space left on device")

// fakeWorktrees records each prepared tree; err fails every prepare.
type fakeWorktrees struct {
	mu    sync.Mutex
	trees []worktree.Tree
	err   error
}

func (f *fakeWorktrees) Prepare(_ context.Context, _ config.Project, t worktree.Tree) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trees = append(f.trees, t)
	return f.err
}

func (f *fakeWorktrees) prepared() []worktree.Tree {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]worktree.Tree(nil), f.trees...)
}

func alphaProject(t *testing.T, cfg config.Config) config.Project {
	t.Helper()
	for _, p := range cfg.Projects {
		if p.Name == projAlpha {
			return p
		}
	}
	t.Fatalf("no project %s", projAlpha)
	return config.Project{}
}

func TestSpawnWorktree(t *testing.T) {
	t.Run("SP-12_the_worker_starts_in_its_own_prepared_worktree", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "alpha-7", Title: "gate", Prompt: "fix(gate): retry on a 502", OwnerTask: true})
		h.until(h.runIn(RunID("alpha-7", 1), model.RunWorking))
		want := worktree.For(alphaProject(t, h.cfg), "alpha-7", false, "fix(gate): retry on a 502")
		if diff := cmp.Diff([]worktree.Tree{want}, h.wt.prepared()); diff != "" {
			t.Errorf("prepared (-want +got):\n%s", diff)
		}
		spec := h.startSpec(AgentName("alpha-7", 1))
		if spec.Worktree != want.Path || spec.Worktree == pathAlpha {
			t.Errorf("spec worktree = %q, want %q", spec.Worktree, want.Path)
		}
		for _, s := range []string{want.Path, want.Branch} {
			if !strings.Contains(spec.Prompt, s) {
				t.Errorf("prompt lacks %q:\n%s", s, spec.Prompt)
			}
		}
	})

	t.Run("SP-12_two_slots_of_one_project_never_share_a_worktree", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "alpha-1", Title: "one", Prompt: "fix: same", OwnerTask: true})
		h.addTask(model.Task{ID: "alpha-2", Title: "two", Prompt: "fix: same", OwnerTask: true})
		h.until(h.runIn(RunID("alpha-1", 1), model.RunWorking))
		h.until(h.runIn(RunID("alpha-2", 1), model.RunWorking))
		a, b := h.startSpec(AgentName("alpha-1", 1)), h.startSpec(AgentName("alpha-2", 1))
		if a.Worktree == b.Worktree {
			t.Errorf("both slots start in %q", a.Worktree)
		}
		if trees := h.wt.prepared(); len(trees) != 2 || trees[0].Branch == trees[1].Branch {
			t.Errorf("prepared = %+v, want two trees on distinct branches", trees)
		}
	})

	t.Run("SP-12_a_failed_worktree_add_fails_the_spawn_before_any_start", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.wt.err = errWorktree
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.taskIn("a1", model.TaskFailed))
		if got := h.run(RunID("a1", 1)); got.State != model.RunFailed {
			t.Errorf("run = %s, want failed", got.State)
		}
		if got := h.events("a1", model.EventTaskState); !strings.Contains(got[len(got)-1], errWorktree.Error()) {
			t.Errorf("task detail = %q, want the git error", got[len(got)-1])
		}
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts = %d, want none", n)
		}
	})

	t.Run("SP-12_an_opencode_worker_keeps_its_sandbox", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "a1", Title: "oc", Prompt: promptA1, OwnerTask: true, WorkerKind: kindOC})
		h.until(h.runIn(RunID("a1", 1), model.RunWorking))
		if got := h.wt.prepared(); len(got) != 0 {
			t.Errorf("prepared = %+v, want none", got)
		}
		if spec := h.startSpec(AgentName("a1", 1)); spec.Worktree != pathAlpha {
			t.Errorf("spec worktree = %q, want the checkout %q", spec.Worktree, pathAlpha)
		}
	})

	t.Run("SP-12_a_follow_up_run_gets_the_same_worktree_and_its_handover_names_it", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, quietStall("hangs"))
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.send(run.Worker.Agent, quietStall("still hangs"))
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		trees := h.wt.prepared()
		if len(trees) != 2 || trees[0] != trees[1] {
			t.Fatalf("prepared = %+v, want the same tree twice", trees)
		}
		got := h.startSpec(AgentName("a1", 2))
		if got.Worktree != trees[0].Path {
			t.Errorf("follow-up worktree = %q, want %q", got.Worktree, trees[0].Path)
		}
		if want := "### Worktree\n\n`" + trees[0].Path + "`"; !strings.Contains(got.Prompt, want) {
			t.Errorf("handover lacks %q:\n%s", want, got.Prompt)
		}
	})

	t.Run("SP-12_no_worktree_maker_fails_the_spawn_closed", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.wt = nil
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.taskIn("a1", model.TaskFailed))
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts = %d, want none", n)
		}
	})
}
