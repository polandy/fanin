package sched

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// The details a merge leaves on its task (SCENARIOS MG-1 to MG-8).
const (
	// MergedDetail leads the detail of a merged task.
	MergedDetail = "merged "
	// MergeErrorDetail leads the detail of a merge that went to the owner.
	MergeErrorDetail = "ERROR "
	// MergeHeldNote is the middle of a merge a hold paused.
	MergeHeldNote = ": merge held "
	// RegateDetail leads the detail of a task whose re-gate under the lock was red.
	RegateDetail = "re-gate "
	// CIHeldDetail is logged for a task a CI hold keeps from a fresh worker.
	CIHeldDetail = "paused: CI held, no new worker"
	// CIFixTitle leads the title of the owner task a red base branch creates.
	CIFixTitle = "fix red CI on "
	// CleanupFailed is logged when the cleanup after a merge left something behind.
	CleanupFailed = "sched: cleanup after the merge left something behind; remove it by hand"
	// liftNote leads the part of a merged detail that names a lifted CI hold and its nudges.
	liftNote = "; ci hold "
)

// ErrMergeDeps is a Merger without the hook runner or the state directory it needs.
var ErrMergeDeps = errors.New("sched: merge deps incomplete")

// Merger squash-merges one green PR of a project.
type Merger interface {
	Merge(ctx context.Context, p config.Project, pr int) error
}

// Cleaner removes the branches and worktree of a merged PR; *cleanup.Cleaner is the real one.
type Cleaner interface {
	Cleanup(ctx context.Context, p config.Project, pr int) error
}

// PostMerge runs a project's post-merge hook under its hold; *hook.Hook is the real one.
type PostMerge interface {
	Run(ctx context.Context, req hook.Request) error
}

// PreGoCheck runs a project's pre-go check on a PR's branch; *hook.PreGo is the real one.
type PreGoCheck interface {
	Run(ctx context.Context, req hook.PreGoRequest) (string, error)
}

// BaseCI reads the latest checks of a project's base branch.
type BaseCI interface {
	BaseChecks(ctx context.Context, p config.Project) ([]forge.Check, error)
}

// FailLogs reads the tail of the failed CI step's log of PR pr of a project.
type FailLogs interface {
	FailedLog(ctx context.Context, p config.Project, pr int) (string, error)
}

// ReviewTexts reads the text of the latest review comment of PR pr of a project, cut to review.TextLimit.
type ReviewTexts interface {
	ReviewText(ctx context.Context, p config.Project, pr int) (string, error)
}

// mergeJob is one merge attempt of a gating task after the owner's go.
type mergeJob struct {
	p   config.Project
	run model.Run
	pr  int
	// shape is the task's: a red re-gate of a PR task takes the PR path's nudges.
	shape model.Shape
	// regate reads the gate again under the lock: another merge may have landed since the last read.
	regate bool
}

func (j mergeJob) ref() string { return "#" + strconv.Itoa(j.pr) }

// mergeEnd is what a merge attempt left for the loop to do.
type mergeEnd struct {
	// held pauses the merge until the hold is lifted.
	held bool
	// red is a red re-gate: the worker gets a nudge.
	red *gate.Result
	// pending re-reads the gate; the merge follows its green.
	pending bool
	// owner moves the task to needs_owner with this detail.
	owner string
	// merged ends the merged task done with this detail.
	merged string
	err    error
}

// checkMergeDeps refuses a Merger whose hook could not run.
func checkMergeDeps(d Deps) error {
	if d.Merger != nil && (d.Hook == nil || d.StateDir == "") {
		return fmt.Errorf("%w: Deps.Merger is set without Deps.Hook or Deps.StateDir; set both", ErrMergeDeps)
	}
	return nil
}

// startMerge begins the merge of a gating task whose latest run reported a PR; a task already merging, or no
// Merger, merges nothing. regate reads the gate again under the lock.
func (s *Scheduler) startMerge(ctx context.Context, p config.Project, id model.TaskID, regate bool) error {
	if s.d.Merger == nil || s.merging[id] {
		return nil
	}
	if s.checking[id] {
		s.goAgain[id] = true
		return nil
	}
	task, err := s.d.Store.Task(ctx, id)
	if err != nil {
		return fmt.Errorf("sched: read task %s for its merge: %w", id, err)
	}
	runs, err := s.d.Store.Runs(ctx, id)
	if err != nil {
		return fmt.Errorf("sched: read the runs of %s for its merge: %w", id, err)
	}
	if task.State != model.TaskGating || len(runs) == 0 {
		return nil
	}
	run := runs[len(runs)-1]
	if run.Result == nil || run.Result.Kind != model.ResultDonePR {
		return nil
	}
	delete(s.held, id)
	delete(s.goAgain, id)
	s.merging[id] = true
	j := mergeJob{p: p, run: run, pr: run.Result.PR, shape: task.Shape, regate: regate}
	log := s.logFor(p.Name, id, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		end := s.mergeOnce(ctx, j)
		s.post(ctx, func(ctx context.Context) { s.mergeEnded(ctx, j, end) })
	})
	return nil
}

// retryHeld starts again every merge of project a hold paused, with a re-gate (SCENARIOS MG-4, MG-8).
func (s *Scheduler) retryHeld(ctx context.Context, project string) error {
	for id, j := range s.held {
		if j.p.Name != project {
			continue
		}
		if err := s.startMerge(ctx, j.p, id, true); err != nil {
			return err
		}
	}
	return nil
}

// mergeOnce runs one merge under the project lock: holds, the base CI, the re-gate after a wait, the merge and the
// hook. It runs off the loop and touches only the store and the collaborators.
func (s *Scheduler) mergeOnce(ctx context.Context, j mergeJob) mergeEnd {
	req := store.LockRequest{Project: j.p.Name, Holder: string(j.run.TaskID), Lease: j.p.Merge.LockLease.Std()}
	waited := false
	err := s.d.Store.AcquireLock(ctx, req)
	if errors.Is(err, store.ErrLockHeld) {
		waited = true
		s.logFor(j.p.Name, j.run.TaskID, j.run.ID).InfoContext(ctx, "sched: merge waits for the project lock",
			"pr", j.ref(), "err", err)
		err = s.d.Store.WaitLock(ctx, req, j.p.Merge.LockWait.Std())
	}
	var held *store.LockHeldError
	if errors.As(err, &held) {
		return mergeEnd{owner: fmt.Sprintf("%s%s: merge lock of project %s held for over %s by %s; end or release "+
			"that holder", MergeErrorDetail, j.ref(), j.p.Name, held.Waited, held.Holder)}
	}
	if err != nil {
		return mergeEnd{err: fmt.Errorf("take the merge lock of %s: %w", j.p.Name, err)}
	}
	stop := s.renewLock(ctx, j, req)
	defer stop()
	end := s.mergeLocked(ctx, j, waited || j.regate)
	if err := s.d.Store.ReleaseLock(ctx, j.p.Name, req.Holder); err != nil && ctx.Err() == nil {
		s.logFor(j.p.Name, j.run.TaskID, j.run.ID).WarnContext(ctx,
			"sched: merge lock not released; it frees when the lease ends", "pr", j.ref(), "err", err)
	}
	return end
}

// mergeLocked is the part of a merge that runs under the lock.
func (s *Scheduler) mergeLocked(ctx context.Context, j mergeJob, regate bool) mergeEnd {
	if end, stop := s.mergeDropped(ctx, j); stop {
		return end
	}
	if end, held := s.mergeHeld(ctx, j); held {
		return end
	}
	fixFor, err := s.ciHoldFixedBy(ctx, j)
	if err != nil {
		return mergeEnd{err: err}
	}
	if fixFor == "" {
		if end, stop := s.baseRed(ctx, j); stop {
			return end
		}
	}
	if regate {
		if end, stop := s.regate(ctx, j); stop {
			return end
		}
	}
	if err := s.d.Merger.Merge(ctx, j.p, j.pr); err != nil {
		return mergeEnd{owner: fmt.Sprintf("%s%s: merge failed: %v", MergeErrorDetail, j.ref(), err)}
	}
	return s.afterMerge(ctx, j, fixFor)
}

// regate reads the gate once more after another task's merge; only a green gate merges (SCENARIOS MG-2).
func (s *Scheduler) regate(ctx context.Context, j mergeJob) (mergeEnd, bool) {
	if s.d.Gates == nil {
		return mergeEnd{owner: MergeErrorDetail + j.ref() + ": " + GateUnreadNote}, true
	}
	r, target, err := s.d.Gates.For(ctx, j.p, j.pr)
	if err == nil {
		var res gate.Result
		if res, err = gate.Read(ctx, r, target); err == nil {
			switch {
			case res.Verdict.IsGreen():
				return mergeEnd{}, false
			case res.Verdict == gate.Pending:
				return mergeEnd{pending: true}, true
			}
			return mergeEnd{red: &res}, true
		}
	}
	return mergeEnd{owner: fmt.Sprintf("%s%s: re-gate unreadable: %v", MergeErrorDetail, j.ref(), err)}, true
}

// afterMerge records the merge, lifts the ci hold the task fixed and runs the hook; the PR counts as merged
// whatever the hook does (SCENARIOS MG-4).
func (s *Scheduler) afterMerge(ctx context.Context, j mergeJob, fixFor string) mergeEnd {
	detail := MergedDetail + j.ref()
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.AppendEvent(ctx, model.Event{Type: model.EventMerged, Project: j.p.Name,
			TaskID: j.run.TaskID, RunID: j.run.ID, Detail: detail}); err != nil {
			return err
		}
		if fixFor != "" {
			if err := tx.LiftHold(ctx, fixFor); err != nil {
				return err
			}
		}
		return tx.SetTaskState(ctx, j.run.TaskID, model.TaskPostMerge, detail)
	})
	if err != nil {
		return mergeEnd{err: fmt.Errorf("record the merge of %s: %w", j.run.TaskID, err)}
	}
	var lift string
	if fixFor != "" {
		if lift, err = s.ciFixed(ctx, j, fixFor); err != nil {
			return mergeEnd{err: err}
		}
	}
	err = s.d.Hook.Run(ctx, hook.Request{Project: j.p.Name, Task: j.run.TaskID, PR: j.pr, Command: j.p.PostMergeHook,
		Dir: s.d.StateDir})
	var failed *hook.FailedError
	switch {
	case errors.As(err, &failed):
		detail = fmt.Sprintf("%s%s: post-merge hook failed: %s; merging held for all slots until 'fanin hold lift %s'",
			MergeErrorDetail, j.ref(), failed.LastLine, failed.HoldID)
		note := notify.Note{Project: j.p.Name, TaskID: j.run.TaskID, Text: detail}
		s.note(ctx, s.logFor(j.p.Name, j.run.TaskID, j.run.ID), j.run, note)
	case err != nil:
		return mergeEnd{err: fmt.Errorf("post-merge hook of %s: %w", j.run.TaskID, err)}
	}
	return mergeEnd{merged: detail + lift}
}

// renewLock keeps the lease alive at half its length until the returned stop is called.
func (s *Scheduler) renewLock(ctx context.Context, j mergeJob, req store.LockRequest) (stop func()) {
	done := make(chan struct{})
	log := s.logFor(j.p.Name, j.run.TaskID, j.run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-s.d.Clock.After(req.Lease / 2):
			}
			if err := s.d.Store.RenewLock(ctx, req); err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "sched: merge lock not renewed; it frees when the lease ends", "err", err)
			}
		}
	})
	return func() { close(done) }
}

// mergeEnded applies a merge attempt on the loop.
func (s *Scheduler) mergeEnded(ctx context.Context, j mergeJob, end mergeEnd) {
	delete(s.merging, j.run.TaskID)
	if ctx.Err() != nil || end.err == nil && s.gone(ctx, j.run.TaskID) {
		return
	}
	var err error
	switch {
	case end.err != nil:
		err = end.err
	case end.held:
		s.held[j.run.TaskID] = j
		err = s.heldStill(ctx, j)
	case end.red != nil && j.shape == model.ShapePR:
		s.goAgain[j.run.TaskID] = true
		err = s.redPR(ctx, j.p, j.run, j.pr, *end.red)
	case end.red != nil:
		err = s.regateRed(ctx, j, *end.red)
	case end.pending:
		s.goAgain[j.run.TaskID] = true
		err = s.gateTurn(ctx, j.p, j.run, j.pr)
	case end.merged != "":
		err = s.mergeDone(ctx, j, end.merged)
	case end.owner != "":
		err = s.askOwner(ctx, j.p, j.run, end.owner)
	}
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: merge %s of %s: %w", j.ref(), j.run.TaskID, err))
	}
}

// heldStill retries a held merge whose hold was lifted before the loop recorded it as held.
func (s *Scheduler) heldStill(ctx context.Context, j mergeJob) error {
	_, held, err := s.d.Store.MergeHold(ctx, j.p.Name, j.run.TaskID)
	if err != nil || held {
		return err
	}
	return s.startMerge(ctx, j.p, j.run.TaskID, true)
}

// regateRed sends the worker back to work with the gate's reasons; its next green merges on the standing go.
func (s *Scheduler) regateRed(ctx context.Context, j mergeJob, res gate.Result) error {
	if err := s.backToWork(ctx, j.p, j.run, RegateDetail+j.ref()+" "+res.String()); err != nil {
		return err
	}
	s.goAgain[j.run.TaskID] = true
	j.run.State = model.RunWorking
	return s.nudgeWith(ctx, j.p, j.run, prompt.NudgeRegate, j.ref()+" "+res.String(), nil, nil)
}

// mergeDone ends the merged run and task with detail, closes the pane and cleans up after the merge (SCENARIOS
// MG-7); neither step can undo the record.
func (s *Scheduler) mergeDone(ctx context.Context, j mergeJob, detail string) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetRunState(ctx, j.run.ID, model.RunSucceeded, detail); err != nil {
			return err
		}
		return tx.SetTaskState(ctx, j.run.TaskID, model.TaskDone, detail)
	})
	if err != nil {
		return fmt.Errorf("end merged task %s: %w", j.run.TaskID, err)
	}
	log := s.logFor(j.p.Name, j.run.TaskID, j.run.ID)
	log.InfoContext(ctx, "sched: "+detail)
	s.unwatch(j.run.ID)
	h := handle(j.p.Name, j.run)
	safego.Go(ctx, log, func(ctx context.Context) {
		err := s.d.Runtime.Stop(ctx, h)
		var cleanErr error
		if s.d.Cleaner != nil {
			cleanErr = s.d.Cleaner.Cleanup(ctx, j.p, j.pr)
		}
		s.post(ctx, func(ctx context.Context) {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.ErrorContext(ctx, closeByHand, "agent", h.Agent, "pane", h.Pane, "err", err)
			}
			if cleanErr != nil {
				log.WarnContext(ctx, CleanupFailed, "pr", j.ref(), "err", cleanErr)
			}
		})
	})
	return nil
}
