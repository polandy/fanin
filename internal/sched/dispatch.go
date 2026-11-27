package sched

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// dispatch fills the free slots of every project.
func (s *Scheduler) dispatch(ctx context.Context) error {
	for _, p := range s.projects {
		if err := s.fill(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// fill starts the project's ready tasks, owner tasks first, until no slot or no task is left.
func (s *Scheduler) fill(ctx context.Context, p config.Project) error {
	for !s.stopped[p.Name] {
		load, err := s.load(ctx, p)
		if err != nil {
			return err
		}
		slot := load.free(p.MaxParallel)
		if slot == 0 {
			return nil
		}
		task, ok, err := s.nextTask(ctx, p)
		if err != nil || !ok {
			return err
		}
		if err := s.begin(ctx, p, task, slot, load.taken); err != nil {
			return err
		}
	}
	return nil
}

// nextTask returns the first ready task that no picker holds and no hold blocks.
func (s *Scheduler) nextTask(ctx context.Context, p config.Project) (model.Task, bool, error) {
	ready, err := s.d.Store.Ready(ctx, p.Name)
	if err != nil {
		return model.Task{}, false, fmt.Errorf("sched: read the ready tasks of %s: %w", p.Name, err)
	}
	slices.SortStableFunc(ready, func(a, b model.Task) int {
		return boolOrder(b.OwnerTask) - boolOrder(a.OwnerTask)
	})
	for _, t := range ready {
		if _, ok := s.picking[t.ID]; ok {
			continue
		}
		_, held, err := s.d.Store.DispatchHold(ctx, p.Name, t.ID)
		if err != nil {
			return model.Task{}, false, fmt.Errorf("sched: read the holds of %s: %w", p.Name, err)
		}
		if !held {
			return t, true, nil
		}
	}
	return model.Task{}, false, nil
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// begin spawns an owner task at once; any other task runs the picker first (SCENARIOS PK-6).
func (s *Scheduler) begin(ctx context.Context, p config.Project, task model.Task, slot int, taken []string) error {
	if task.OwnerTask {
		return s.spawn(ctx, p, task, slot, task.Prompt, "")
	}
	paused, err := s.consumePause(ctx, p)
	if err != nil || paused {
		return err
	}
	s.picking[task.ID] = reservation{project: p.Name, slot: slot}
	req := pick.Request{Project: p.Name, Forge: pick.Forge(s.d.Config.Forges[p.Forge].Kind), Dir: p.Path,
		BaseBranch: p.Base, InProgress: taken}
	log := s.logFor(p.Name, task.ID, "")
	safego.Go(ctx, log, func(ctx context.Context) {
		res, err := s.d.Picker.Pick(ctx, req)
		s.post(ctx, func(ctx context.Context) { s.picked(ctx, p, task.ID, slot, res, err) })
	})
	return nil
}

// picked spawns the picked task, ends the task idle on NOTHING (PK-3), and spawns with no hint on a failed
// pick, so the worker chooses its own task (PK-4).
func (s *Scheduler) picked(ctx context.Context, p config.Project, id model.TaskID, slot int, res pick.Result,
	perr error) {
	delete(s.picking, id)
	if ctx.Err() != nil {
		return
	}
	log := s.logFor(p.Name, id, "")
	task, err := s.d.Store.Task(ctx, id)
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: read task %s after its pick: %w", id, err))
		return
	}
	if task.State != model.TaskQueued {
		return
	}
	if _, held, err := s.d.Store.DispatchHold(ctx, p.Name, id); err != nil {
		s.fail(ctx, fmt.Errorf("sched: read the holds of %s before a spawn: %w", p.Name, err))
		return
	} else if held {
		log.InfoContext(ctx, "sched: "+CIHeldDetail)
		return
	}
	if err := s.recordPickUsage(ctx, p, id, res.Usage); err != nil {
		s.fail(ctx, err)
		return
	}
	var hint, mdl string
	switch {
	case perr != nil:
		log.WarnContext(ctx, "sched: pick failed, the worker chooses its own task", "err", perr)
	case res.Idle:
		log.InfoContext(ctx, "sched: picker found nothing, no worker started", "reason", res.Reason)
		s.end(ctx, task, IdleDetail+res.Reason, false)
		return
	default:
		hint, mdl = res.Task, res.Model
	}
	if err := s.spawn(ctx, p, task, slot, hint, mdl); err != nil {
		s.fail(ctx, err)
	}
}

// recordPickUsage appends the picker's tokens as a token_usage event of the task; it carries counts only.
func (s *Scheduler) recordPickUsage(ctx context.Context, p config.Project, id model.TaskID, u headless.Usage) error {
	if u.Total() == 0 {
		return nil
	}
	ev := model.Event{Type: model.EventTokenUsage, Project: p.Name, TaskID: id,
		Detail: fmt.Sprintf("pick: %d tokens (input %d, output %d, cache creation %d, cache read %d)",
			u.Total(), u.Input, u.Output, u.CacheCreation, u.CacheRead)}
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil {
		return fmt.Errorf("sched: record the pick usage of task %s: %w", id, err)
	}
	return nil
}

// IdleDetail leads the detail of a task the picker found nothing for.
const IdleDetail = "idle: "

// spawn writes the run row, then starts the worker in the background; a failed spawn is a failed run (SP-8).
func (s *Scheduler) spawn(ctx context.Context, p config.Project, task model.Task, slot int, hint, picked string) error {
	log := s.logFor(p.Name, task.ID, "")
	spec, err := s.spec(p, task, hint, picked, "")
	if err != nil {
		log.ErrorContext(ctx, "sched: task refused", "err", err)
		s.end(ctx, task, err.Error(), true)
		return nil //nolint:nilerr // a refused task is ended failed, not a failed step
	}
	var run model.Run
	paused := false
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if paused, err = tx.ConsumePause(ctx, p.Name); err != nil || paused {
			return err
		}
		n, err := nextRunNumber(ctx, tx, task.ID)
		if err != nil {
			return err
		}
		run = model.Run{ID: RunID(task.ID, n), TaskID: task.ID, Number: n, Slot: slot, Hint: hint,
			Token: model.Token(rand.Text()), Worker: model.Worker{Agent: AgentName(task.ID, n)}}
		if err := tx.SetTaskState(ctx, task.ID, model.TaskRunning, fmt.Sprintf("slot %d", slot)); err != nil {
			return err
		}
		return tx.CreateRun(ctx, run)
	})
	if err != nil {
		return fmt.Errorf("sched: spawn task %s: %w", task.ID, err)
	}
	if paused {
		s.stopped[p.Name] = true
		log.InfoContext(ctx, "sched: paused before the next worker")
		return nil
	}
	s.launch(ctx, p, run, spec)
	return nil
}

// spec builds the run spec of task without the run's ID, agent and MCP config; a follow-up run's prompt ends
// with its handover.
func (s *Scheduler) spec(p config.Project, task model.Task, hint, picked, handover string) (runtime.RunSpec,
	error) {
	kind := cmp.Or(task.WorkerKind, p.WorkerKind)
	if wk, ok := s.d.Config.WorkerKinds[kind]; !ok || wk.Runtime != config.RuntimeHerdr {
		return runtime.RunSpec{}, fmt.Errorf("%w: task %s: worker kind %q has runtime %q; workers run on %s",
			ErrWorkerKind, task.ID, kind, wk.Runtime, config.RuntimeHerdr)
	}
	forgeKind := s.d.Config.Forges[p.Forge].Kind
	if kind == intake.KindOpencode && (forgeKind != config.ForgeGitHub || task.Shape != model.ShapePR) {
		return runtime.RunSpec{}, fmt.Errorf("%w: task %s: opencode runs %s tasks of GitHub projects only; project %s "+
			"has forge kind %q and the task is %s; use a Claude worker", ErrWorkerKind, task.ID, model.ShapePR, p.Name,
			forgeKind, task.Shape)
	}
	wp := p
	wp.WorkerKind, wp.Model = kind, cmp.Or(task.ProjectModel, p.Model)
	mdl, err := s.d.Config.WorkerModel(wp, task.OwnerModel, picked)
	if err != nil {
		return runtime.RunSpec{}, fmt.Errorf("sched: task %s: %w", task.ID, err)
	}
	text, err := prompt.Render(promptName(kind, task.Shape, forgeKind),
		prompt.Vars{Repo: p.Path, Base: p.Base, Hint: hint, Handover: handover})
	if err != nil {
		return runtime.RunSpec{}, fmt.Errorf("sched: task %s: %w", task.ID, err)
	}
	spec := runtime.RunSpec{TaskID: task.ID, Project: p.Name, Worktree: p.Path, Title: task.Title, Model: mdl,
		Prompt: text, Base: p.Base}
	if kind == intake.KindOpencode {
		spec.Kind = runtime.KindOpencode
	}
	return spec, nil
}

// promptName picks the worker template by worker kind, task shape and forge.
func promptName(kind string, shape model.Shape, forge string) prompt.Name {
	gitea := forge == config.ForgeGitea
	switch {
	case kind == intake.KindOpencode:
		return prompt.WorkerOC
	case shape != model.ShapePR && gitea:
		return prompt.WorkerGiteaAnalyze
	case shape != model.ShapePR:
		return prompt.WorkerAnalyze
	case gitea:
		return prompt.WorkerGitea
	}
	return prompt.Worker
}

// RunID is the ID of the task's run number n.
func RunID(task model.TaskID, n int) model.RunID {
	return model.RunID(fmt.Sprintf("%s-run-%d", task, n))
}

// AgentName is the agent of the task's run number n; it carries no lane name (SCENARIOS LN-1).
func AgentName(task model.TaskID, n int) string { return fmt.Sprintf("%s-%d", task, n) }

// nextRunNumber is one past the task's latest run, so a failed run's number is never reused (SP-8).
func nextRunNumber(ctx context.Context, tx *store.Tx, task model.TaskID) (int, error) {
	latest, err := tx.LatestRun(ctx, task)
	if errors.Is(err, store.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return latest.Number + 1, nil
}

// consumePause clears a pending pause before the project's next worker and stops the project (FP-2).
func (s *Scheduler) consumePause(ctx context.Context, p config.Project) (bool, error) {
	paused := false
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		paused, err = tx.ConsumePause(ctx, p.Name)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("sched: consume the pause of %s: %w", p.Name, err)
	}
	if paused {
		s.stopped[p.Name] = true
		s.logFor(p.Name, "", "").InfoContext(ctx, "sched: paused before the next worker")
	}
	return paused, nil
}
