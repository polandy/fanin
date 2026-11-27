package sched

import (
	"context"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/store"
)

// ciHoldFixedBy returns the ci hold this task's merge lifts, or "".
func (s *Scheduler) ciHoldFixedBy(ctx context.Context, j mergeJob) (string, error) {
	holds, err := s.d.Store.Holds(ctx, j.p.Name)
	if err != nil {
		return "", fmt.Errorf("read the holds of %s: %w", j.p.Name, err)
	}
	for _, h := range holds {
		if h.Kind == model.HoldCI && h.FixTask == j.run.TaskID {
			return h.ID, nil
		}
	}
	return "", nil
}

// baseRed sets a ci hold with a fix task when a check on the base branch failed; an unreadable base CI goes to the
// owner (SCENARIOS MG-8).
func (s *Scheduler) baseRed(ctx context.Context, j mergeJob) (mergeEnd, bool) {
	if s.d.BaseCI == nil {
		return mergeEnd{}, false
	}
	checks, err := s.d.BaseCI.BaseChecks(ctx, j.p)
	if err != nil {
		return mergeEnd{owner: fmt.Sprintf("%s%s: CI of %s unreadable: %v", MergeErrorDetail, j.ref(), j.p.Base,
			err)}, true
	}
	var red []string
	for _, c := range checks {
		if c.State == forge.CheckFailure || c.State == forge.CheckCancelled {
			red = append(red, c.Name)
		}
	}
	if len(red) == 0 {
		return mergeEnd{}, false
	}
	title := CIFixTitle + j.p.Base + ": " + strings.Join(red, ", ")
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		id, err := tx.NextTaskID(ctx, j.p.Name)
		if err != nil {
			return err
		}
		if err := tx.CreateTask(ctx, model.Task{ID: id, Project: j.p.Name, Shape: model.ShapePR, Title: title,
			Prompt: title, OwnerTask: true}); err != nil {
			return err
		}
		_, err = tx.SetHold(ctx, model.Hold{Project: j.p.Name, Kind: model.HoldCI, Reason: title, FixTask: id})
		return err
	})
	if err != nil {
		return mergeEnd{err: fmt.Errorf("set the ci hold of %s: %w", j.p.Name, err)}, true
	}
	return s.mergeHeld(ctx, j)
}

// ciFixed nudges the project's other live workers after the fix task's merge lifted hold; the returned lift line
// names every failed nudge (SCENARIOS MG-8).
func (s *Scheduler) ciFixed(ctx context.Context, j mergeJob, hold string) (string, error) {
	runs, err := s.liveRuns(ctx, j)
	if err != nil {
		return "", err
	}
	text, err := prompt.Render(prompt.NudgeCIFixed, prompt.Vars{Repo: j.p.Path, Base: j.p.Base})
	if err != nil {
		return "", fmt.Errorf("render %s: %w", prompt.NudgeCIFixed, err)
	}
	line := liftNote + hold + " lifted"
	var sent, lost []string
	for _, r := range runs {
		if err := s.d.Runtime.Prompt(ctx, handle(j.p.Name, r), text); err != nil {
			lost = append(lost, fmt.Sprintf("%s%s failed: %v", NudgeFailedDetail, r.Worker.Agent, err))
			continue
		}
		sent = append(sent, r.Worker.Agent)
		err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventWorkerNudged, Project: j.p.Name,
				TaskID: r.TaskID, RunID: r.ID, Detail: string(prompt.NudgeCIFixed)})
			return err
		})
		if err != nil {
			return "", fmt.Errorf("record the nudge of %s: %w", r.ID, err)
		}
	}
	if len(sent) > 0 {
		line += ", nudged " + strings.Join(sent, ", ")
	}
	if len(lost) > 0 {
		line += "; " + strings.Join(lost, "; ")
	}
	return line, nil
}

// liveRuns are the working or stalled runs of the project's other tasks.
func (s *Scheduler) liveRuns(ctx context.Context, j mergeJob) ([]model.Run, error) {
	tasks, err := s.d.Store.OpenTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the open tasks: %w", err)
	}
	var out []model.Run
	for _, t := range tasks {
		if t.Project != j.p.Name || t.ID == j.run.TaskID || t.State != model.TaskRunning {
			continue
		}
		runs, err := s.d.Store.Runs(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("read the runs of %s: %w", t.ID, err)
		}
		if n := len(runs); n > 0 && (runs[n-1].State == model.RunWorking || runs[n-1].State == model.RunStalled) {
			out = append(out, runs[n-1])
		}
	}
	return out, nil
}

// mergeHeld records a merge a hold pauses; held is false when nothing holds it or the holds are unreadable.
func (s *Scheduler) mergeHeld(ctx context.Context, j mergeJob) (mergeEnd, bool) {
	h, ok, err := s.d.Store.MergeHold(ctx, j.p.Name, j.run.TaskID)
	if err != nil {
		return mergeEnd{err: fmt.Errorf("read the holds of %s: %w", j.p.Name, err)}, true
	}
	if !ok {
		return mergeEnd{}, false
	}
	detail := fmt.Sprintf("%s%s%s(%s hold %s: %s) (worker %s still open)", PausedDetail, j.ref(), MergeHeldNote,
		h.Kind, h.ID, h.Reason, j.run.Worker.Agent)
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventMergeHeld, Project: j.p.Name,
			TaskID: j.run.TaskID, RunID: j.run.ID, Detail: detail})
		return err
	})
	if err != nil {
		return mergeEnd{err: fmt.Errorf("record the held merge of %s: %w", j.run.TaskID, err)}, true
	}
	s.logFor(j.p.Name, j.run.TaskID, j.run.ID).InfoContext(ctx, "sched: "+detail)
	return mergeEnd{held: true}, true
}
