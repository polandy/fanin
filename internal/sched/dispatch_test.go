package sched

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

func ownerTask(id model.TaskID) model.Task {
	return model.Task{ID: id, Title: "owner " + string(id), Prompt: "do " + string(id), OwnerTask: true}
}

func (h *harness) events(id model.TaskID, typ model.EventType) []string {
	h.t.Helper()
	evs, err := h.st.Events(h.t.Context(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev.Detail)
		}
	}
	return out
}

func TestDispatch(t *testing.T) {
	t.Run("PK-3_picker_nothing_ends_idle_and_starts_no_worker", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.res = pick.Result{Idle: true, Reason: "no open todo"}
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.until(h.taskIn("a1", model.TaskDone))
		if runs := h.runs("a1"); len(runs) != 0 {
			t.Errorf("runs = %v, want none", runs)
		}
		if got := h.events("a1", model.EventTaskState); !strings.HasPrefix(got[len(got)-1], IdleDetail) {
			t.Errorf("last task_state = %q, want the %q detail", got[len(got)-1], IdleDetail)
		}
		if !strings.Contains(h.logs.String(), "picker found nothing") {
			t.Errorf("logs lack the idle line:\n%s", h.logs)
		}
	})

	t.Run("PK-4_failed_pick_spawns_a_worker_that_chooses_its_own_task", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.err = errors.New("ERROR: junk")
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.until(h.runIn(RunID("a1", 1), model.RunWorking))
		want, err := prompt.Render(prompt.Worker, prompt.Vars{Repo: pathAlpha, Base: baseMain})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, h.rt.starts()[0].Prompt); diff != "" {
			t.Errorf("prompt (-want +got):\n%s", diff)
		}
		if !strings.Contains(h.logs.String(), "pick failed") {
			t.Errorf("logs lack the pick failed line:\n%s", h.logs)
		}
	})

	t.Run("PK-5_model_precedence_owner_project_picker", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			task         model.Task
			projectModel string
			picked       string
			want         string
		}{
			{name: "owner wins", task: model.Task{OwnerModel: modelOpus, ProjectModel: modelHaiku}, picked: modelSonnet,
				want: modelOpus},
			{name: "task project model next", task: model.Task{ProjectModel: modelHaiku}, picked: modelOpus, want: modelHaiku},
			{name: "project config model next", projectModel: modelHaiku, picked: modelOpus, want: modelHaiku},
			{name: "picked last", picked: modelOpus, want: modelOpus},
			{name: "kind default without any", want: modelSonnet},
			{name: "opencode gets no model", task: model.Task{WorkerKind: kindOC, OwnerModel: modelOpus}, picked: modelOpus},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := testConfig()
				cfg.Projects[0].Model = tc.projectModel
				h := newHarness(t, cfg)
				h.pk.res = pick.Result{Task: "fix the flaky test", Model: tc.picked}
				h.start()
				task := tc.task
				task.ID, task.Title = "a1", "loop"
				h.addTask(task)
				h.until(h.runIn(RunID("a1", 1), model.RunWorking))
				spec := h.rt.starts()[0]
				if spec.Model != tc.want {
					t.Errorf("model = %q, want %q", spec.Model, tc.want)
				}
				if !strings.Contains(spec.Prompt, "fix the flaky test") {
					t.Errorf("prompt lacks the picked task:\n%s", spec.Prompt)
				}
			})
		}
	})

	t.Run("PK-6_owner_task_runs_no_picker_and_takes_no_next_todo", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(ownerTask("a1"))
		id := RunID("a1", 1)
		h.until(h.runIn(id, model.RunWorking))
		if got := h.rt.starts()[0].Prompt; !strings.Contains(got, "do a1") {
			t.Errorf("prompt lacks the owner's task:\n%s", got)
		}
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunResult(ctx, id, model.Result{Kind: model.ResultDonePR, PR: 7})
		})
		h.until(h.runIn(id, model.RunReported))
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			for _, to := range []model.TaskState{model.TaskGating, model.TaskPostMerge, model.TaskDone} {
				if err := tx.SetTaskState(ctx, "a1", to, "merged"); err != nil {
					return err
				}
			}
			return tx.SetRunState(ctx, id, model.RunSucceeded, "merged")
		})
		h.until(func() bool { return len(h.timers("a1")) == 0 })
		h.quiet()
		if runs := h.runs("a1"); len(runs) != 1 {
			t.Errorf("runs = %d, want the one run", len(runs))
		}
		if n := h.pk.calls(); n != 0 {
			t.Errorf("picker calls = %d, want none", n)
		}
	})

	// see SCENARIOS LN-1, LN-4
	t.Run("LN-1_LN-4_any_task_kind_takes_the_lowest_free_slot", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.res = pick.Result{Task: "next todo"}
		h.start()
		h.addTask(ownerTask("a1"))
		h.addTask(model.Task{ID: "a2", Title: "analyze", Shape: model.ShapeAnalysis, OwnerTask: true, Prompt: "why"})
		h.addTask(model.Task{ID: "a3", Title: "oc", WorkerKind: kindOC})
		h.until(func() bool {
			return h.runIn(RunID("a1", 1), model.RunWorking)() && h.runIn(RunID("a2", 1), model.RunWorking)()
		})
		h.quiet()
		if runs := h.runs("a3"); len(runs) != 0 || h.task("a3").State != model.TaskQueued {
			t.Fatalf("a3 = %s with %d runs, want queued without a slot", h.task("a3").State, len(runs))
		}
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			if err := tx.SetRunState(ctx, RunID("a1", 1), model.RunCanceled, "owner"); err != nil {
				return err
			}
			return tx.SetTaskState(ctx, "a1", model.TaskCanceled, "owner")
		})
		h.until(h.runIn(RunID("a3", 1), model.RunWorking))
		got := map[model.TaskID]int{}
		for _, id := range []model.TaskID{"a2", "a3"} {
			run := h.runs(id)[0]
			got[id] = run.Slot
			if strings.Contains(run.Worker.Agent, "lane") || run.Worker.Agent != AgentName(id, 1) || run.Branch != "" {
				t.Errorf("run %s: agent %q branch %q, want %q and no lane name", run.ID, run.Worker.Agent, run.Branch,
					AgentName(id, 1))
			}
		}
		if diff := cmp.Diff(map[model.TaskID]int{"a2": 2, "a3": 1}, got); diff != "" {
			t.Errorf("slots (-want +got):\n%s", diff)
		}
		if oc := h.rt.starts()[2]; oc.Model != "" || !strings.Contains(oc.Prompt, "next todo") {
			t.Errorf("opencode spec = model %q prompt %q, want no model and the picked task", oc.Model, oc.Prompt)
		}
	})

	// see SCENARIOS LN-2, LN-5
	t.Run("LN-2_LN-5_runs_are_found_by_task_id_in_their_project", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(ownerTask("a1"))
		h.addTask(model.Task{ID: "b1", Project: projBash, Title: "bash", OwnerTask: true})
		h.until(h.runIn(RunID("a1", 1), model.RunWorking))
		h.quiet()
		if runs := h.runs("b1"); len(runs) != 0 {
			t.Errorf("bash project runs = %v, want none: fanind leaves loop: bash alone", runs)
		}
		if diff := cmp.Diff([]string{projAlpha}, h.s.Projects()); diff != "" {
			t.Errorf("Projects (-want +got):\n%s", diff)
		}
	})
}

func TestPromptName(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		shape model.Shape
		forge string
		want  prompt.Name
	}{
		{kindOC, model.ShapePR, config.ForgeGitHub, prompt.WorkerOC},
		{kindCC, model.ShapeAnalysis, config.ForgeGitea, prompt.WorkerGiteaAnalyze},
		{kindCC, model.ShapeAnalysis, config.ForgeGitHub, prompt.WorkerAnalyze},
		{kindCC, model.ShapePR, config.ForgeGitea, prompt.WorkerGitea},
		{kindCC, model.ShapePR, config.ForgeGitHub, prompt.Worker},
	} {
		if got := promptName(tc.kind, tc.shape, tc.forge); got != tc.want {
			t.Errorf("promptName(%s, %s, %s) = %s, want %s", tc.kind, tc.shape, tc.forge, got, tc.want)
		}
	}
}

func TestDispatchStops(t *testing.T) {
	t.Run("LN-3_pause_is_consumed_before_the_next_worker_until_resume", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.RequestPause(ctx, projAlpha)
			return err
		})
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.until(func() bool {
			paused, err := h.st.Paused(t.Context(), projAlpha)
			return err == nil && !paused
		})
		h.quiet()
		if runs := h.runs("a1"); len(runs) != 0 || h.pk.calls() != 0 {
			t.Fatalf("paused project: %d runs, %d picks, want none", len(runs), h.pk.calls())
		}
		h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.Resume(ctx, projAlpha) })
		h.until(h.runIn(RunID("a1", 1), model.RunWorking))
	})

	t.Run("ci_hold_blocks_fresh_workers_but_its_fix_task", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.SetHold(ctx, model.Hold{Project: projAlpha, Kind: model.HoldCI, Reason: "main red", FixTask: "a2"})
			return err
		})
		h.start()
		h.addTask(ownerTask("a1"))
		h.addTask(ownerTask("a2"))
		h.until(h.runIn(RunID("a2", 1), model.RunWorking))
		h.quiet()
		if runs := h.runs("a1"); len(runs) != 0 {
			t.Errorf("held task runs = %v, want none", runs)
		}
	})

	t.Run("worker_kind_without_herdr_is_refused", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "a1", Title: "hl", OwnerTask: true, WorkerKind: kindHL})
		h.until(h.taskIn("a1", model.TaskFailed))
		if got := h.events("a1", model.EventTaskState); !strings.Contains(got[len(got)-1], ErrWorkerKind.Error()) {
			t.Errorf("detail = %q, want %v", got[len(got)-1], ErrWorkerKind)
		}
	})

	t.Run("owner_model_the_kind_does_not_take_fails_the_task", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "a1", Title: "bad", OwnerTask: true, OwnerModel: "gpt"})
		h.until(h.taskIn("a1", model.TaskFailed))
		if runs := h.runs("a1"); len(runs) != 0 {
			t.Errorf("runs = %v, want none", runs)
		}
	})

	t.Run("SP-8_failed_MCP_config_write_is_a_failed_run", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.files = failingFiles{}
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.runIn(RunID("a1", 1), model.RunFailed))
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts = %d, want none", n)
		}
	})
}

type failingFiles struct{}

var errDisk = errors.New("disk full")

func (failingFiles) Prepare(context.Context, config.Project, model.Run) (string, error) {
	return "", errDisk
}

var _ runtime.WorkerRuntime = (*fakeRuntime)(nil)
