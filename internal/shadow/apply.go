package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// runNumber is the number of the one run the importer gives each task.
const runNumber = 1

// RunID returns the ID of the importer's run of a task.
func RunID(task model.TaskID) model.RunID {
	return model.RunID(fmt.Sprintf("%s-run-%d", task, runNumber))
}

// current is the store's state of one project before an import.
type current struct {
	tasks map[model.TaskID]model.Task
	runs  map[model.RunID]model.Run
	holds []model.Hold
}

// Apply writes tasks, runs and holds in one transaction, then the locks; unchanged input writes nothing (SH-5).
func Apply(ctx context.Context, st *store.Store, log *slog.Logger, projects []Project, lease time.Duration) error {
	cur := make(map[string]current, len(projects))
	for _, p := range projects {
		c, err := readCurrent(ctx, st, p)
		if err != nil {
			return err
		}
		cur[p.Label] = c
	}
	if err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		for _, p := range projects {
			if err := applyProject(ctx, tx, log, p, cur[p.Label]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	locks, err := st.Locks(ctx)
	if err != nil {
		return err
	}
	byProject := make(map[string]model.Lock, len(locks))
	for _, l := range locks {
		byProject[l.Project] = l
	}
	for _, p := range projects {
		if err := applyLock(ctx, st, p, byProject, lease); err != nil {
			return err
		}
	}
	return nil
}

func readCurrent(ctx context.Context, st *store.Store, p Project) (current, error) {
	c := current{tasks: map[model.TaskID]model.Task{}, runs: map[model.RunID]model.Run{}}
	tasks, err := st.ProjectTasks(ctx, p.Label)
	if err != nil {
		return current{}, err
	}
	for _, t := range tasks {
		c.tasks[t.ID] = t
	}
	for _, t := range p.Tasks {
		if _, ok := c.tasks[t.ID]; !ok {
			continue
		}
		runs, err := st.Runs(ctx, t.ID)
		if err != nil {
			return current{}, err
		}
		for _, r := range runs {
			c.runs[r.ID] = r
		}
	}
	if c.holds, err = st.Holds(ctx, p.Label); err != nil {
		return current{}, err
	}
	return c, nil
}

func applyProject(ctx context.Context, tx *store.Tx, log *slog.Logger, p Project, c current) error {
	for _, t := range p.Tasks {
		if err := applyTask(ctx, tx, log, p.Label, t, c); err != nil {
			return err
		}
	}
	return applyHolds(ctx, tx, p, c.holds)
}

func applyTask(ctx context.Context, tx *store.Tx, log *slog.Logger, label string, t Task, c current) error {
	have, ok := c.tasks[t.ID]
	if !ok {
		title := t.Prompt
		if title == "" {
			title = t.Agent
		}
		have = model.Task{ID: t.ID, Project: label, Title: title, Prompt: t.Prompt, Shape: model.ShapePR,
			State: model.TaskQueued, OwnerTask: t.OwnerTask, PickedModel: t.Model}
		if err := tx.CreateTask(ctx, have); err != nil {
			return err
		}
	}
	steps, ok := route(have.State, t.State, model.AllTaskStates)
	if !ok {
		log.Warn("shadow: store task is ahead of the state file; skipped", "project", label, "task_id", t.ID,
			"store", have.State, "file", t.State)
		return nil
	}
	for _, s := range steps {
		if err := tx.SetTaskState(ctx, t.ID, s, t.Line); err != nil {
			return err
		}
	}
	return applyRun(ctx, tx, log, label, t, c)
}

func applyRun(ctx context.Context, tx *store.Tx, log *slog.Logger, label string, t Task, c current) error {
	id := RunID(t.ID)
	run, ok := c.runs[id]
	if !ok {
		run = model.Run{ID: id, TaskID: t.ID, Number: runNumber, Branch: t.Branch, State: model.RunWorking}
		if err := tx.CreateRun(ctx, run); err != nil {
			return err
		}
	}
	want := model.RunWorking
	switch t.State {
	case model.TaskDone:
		want = model.RunSucceeded
	case model.TaskCanceled:
		want = model.RunCanceled
	}
	steps, ok := route(run.State, want, model.AllRunStates)
	if !ok {
		log.Warn("shadow: store run is ahead of the state file; skipped", "project", label, "task_id", t.ID,
			"run_id", id, "store", run.State, "file", want)
		return nil
	}
	for _, s := range steps {
		if err := tx.SetRunState(ctx, id, s, t.Line); err != nil {
			return err
		}
	}
	return nil
}

// applyHolds keeps the active holds that match a hold file, lifts the others and sets the missing ones.
func applyHolds(ctx context.Context, tx *store.Tx, p Project, active []model.Hold) error {
	want := make([]bool, len(p.Holds))
	for _, h := range active {
		kept := false
		for i, w := range p.Holds {
			if !want[i] && h.Kind == w.Kind && h.Reason == model.CapField(w.Reason) && h.FixTask == w.FixTask {
				want[i], kept = true, true
				break
			}
		}
		if !kept {
			if err := tx.LiftHold(ctx, h.ID); err != nil {
				return err
			}
		}
	}
	for i, w := range p.Holds {
		if !want[i] {
			if _, err := tx.SetHold(ctx, w); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyLock takes or renews the project lock while the merge lock is held and releases it once it is free.
func applyLock(ctx context.Context, st *store.Store, p Project, locks map[string]model.Lock, lease time.Duration) error {
	have, held := locks[p.Label]
	if p.Lock == nil {
		if !held {
			return nil
		}
		return ignoreNotHeld(st.ReleaseLock(ctx, p.Label, have.Holder))
	}
	req := store.LockRequest{Project: p.Label, Holder: p.Lock.Holder, Lease: lease}
	if held && have.Holder == p.Lock.Holder {
		err := st.RenewLock(ctx, req)
		if !errors.Is(err, store.ErrLockNotHeld) {
			return err
		}
	}
	if held && have.Holder != p.Lock.Holder {
		if err := ignoreNotHeld(st.ReleaseLock(ctx, p.Label, have.Holder)); err != nil {
			return err
		}
	}
	return st.AcquireLock(ctx, req)
}

// ignoreNotHeld drops ErrLockNotHeld: the row was already gone.
func ignoreNotHeld(err error) error {
	if errors.Is(err, store.ErrLockNotHeld) {
		return nil
	}
	return err
}

// route returns the states after from on a shortest path to to that the model allows; ok is false without one.
func route[S interface {
	comparable
	CanTransition(S) error
}](from, to S, all []S) (steps []S, ok bool) {
	if from == to {
		return nil, true
	}
	prev := map[S]S{from: from}
	queue := []S{from}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, next := range all {
			if _, seen := prev[next]; seen || s.CanTransition(next) != nil {
				continue
			}
			prev[next] = s
			if next == to {
				for at := to; at != from; at = prev[at] {
					steps = append([]S{at}, steps...)
				}
				return steps, true
			}
			queue = append(queue, next)
		}
	}
	return nil, false
}
