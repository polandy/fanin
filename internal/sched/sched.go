// Package sched is fanind's event loop: it fills each project's slots with workers, watches them and ends the
// ones that stop answering (spec: docs/dev/spec/sched.md).
package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

// The timer kinds the scheduler arms per task.
const (
	TimerStall   = "stall"
	TimerTimeout = "worker_timeout"
	// TimerAwait looks again at a worker that stopped while its background footer was live (SCENARIOS AW-11).
	TimerAwait = "await"
)

// ownTimer reports whether the scheduler arms and fires timers of kind.
func ownTimer(kind string) bool {
	return kind == TimerStall || kind == TimerTimeout || kind == TimerAwait || gateTimer(kind)
}

// BGFooterMax is how long a worker's own background-shell footer spares it from the timeout (SCENARIOS WD-2).
const BGFooterMax = 180 * time.Minute

// EventBatch is how many store events one step reads at a time.
const EventBatch = 256

// RetryAfter is how long the loop waits before it retries a step that failed on a store read or write.
const RetryAfter = 30 * time.Second

// The scheduler errors.
var (
	// ErrStatusUnknown is logged for a live run whose worker cannot be observed; nothing else is started for it.
	ErrStatusUnknown = errors.New("sched: worker status unknown")
	// ErrWorkerKind is a task whose worker kind cannot run a worker.
	ErrWorkerKind = errors.New("sched: worker kind cannot run a worker")
	// ErrRemoteHost is a loop: fanin project on a remote host; fanind runs workers on its own host only.
	ErrRemoteHost = errors.New("sched: loop: fanin on a remote host")
)

// Picker chooses a task for a non-owner task; *pick.Picker is the real one.
type Picker interface {
	Pick(ctx context.Context, req pick.Request) (pick.Result, error)
}

// RunFiles writes what a run needs on disk before its start and returns the MCP config file.
type RunFiles interface {
	Prepare(ctx context.Context, p config.Project, run model.Run) (mcpConfig string, err error)
}

// Deps are the scheduler's collaborators.
type Deps struct {
	Config  config.Config
	Store   *store.Store
	Runtime runtime.WorkerRuntime
	Picker  Picker
	Files   RunFiles
	Clock   clock.Clock
	Log     *slog.Logger
	// Unpushed lists a run's unpushed commits before NUDGE_SECURE (SCENARIOS AW-11, AW-12).
	Unpushed Unpushed
	// Notify tells the owner about a blocked task; nil only logs the note.
	Notify notify.Notifier
	// Gates reads the gate of an analysis PR; nil leaves the PR unread for the owner's go.
	Gates PRGates
	// Waiting, when set, is called each time the loop is about to block.
	Waiting func()
	// Merger merges a gating task's PR on the owner's go; nil merges nothing.
	Merger Merger
	// Cleaner removes a merged PR's branches and worktree; nil leaves them (SCENARIOS MG-7).
	Cleaner Cleaner
	// Hook runs the post-merge hook in StateDir; both are needed with a Merger.
	Hook     PostMerge
	StateDir string
	// PreGo runs a project's pre_go_check before its PR is ready for the go; nil with a pre_go_check asks the owner.
	PreGo PreGoCheck
	// BaseCI reads the base branch's checks before a merge; nil skips the CI hold rule (SCENARIOS MG-8).
	BaseCI BaseCI
	// OCDeliver and OCReview push and review a COMMITTED opencode branch; nil asks the owner (SCENARIOS OC-1, OC-6).
	OCDeliver OCDelivery
	OCReview  OCReviewer
}

// Scheduler is the reactor of store events, runtime events and due timers. Only Run's goroutine touches it.
type Scheduler struct {
	d        Deps
	projects []config.Project
	cursor   int64
	watched  map[model.RunID]*watch
	// picking holds the slot of each task whose picker runs.
	picking map[model.TaskID]reservation
	// stopped are the projects a consumed pause holds until they are resumed.
	stopped map[string]bool
	retryAt time.Time
	// gates is the gate episode of each gating analysis task; busy marks a turn in flight.
	gates map[model.TaskID]gate.State
	busy  map[model.TaskID]turnState
	// merging marks a merge in flight, held a merge a hold paused and goAgain a standing go that the next green
	// gate merges on.
	merging map[model.TaskID]bool
	held    map[model.TaskID]mergeJob
	goAgain map[model.TaskID]bool
	// checking marks a task whose pre-go check runs: no merge starts before it ends.
	checking map[model.TaskID]bool
	done     chan func(context.Context)
	// applied counts the runtime events and stream ends the loop handled.
	applied atomic.Int64
}

type reservation struct {
	project string
	slot    int
}

// New returns a scheduler of the projects with loop: fanin; each must run on fanind's own host.
func New(d Deps) (*Scheduler, error) {
	s := &Scheduler{d: d, watched: map[model.RunID]*watch{}, picking: map[model.TaskID]reservation{},
		stopped: map[string]bool{}, done: make(chan func(context.Context)),
		gates: map[model.TaskID]gate.State{}, busy: map[model.TaskID]turnState{}, merging: map[model.TaskID]bool{},
		held: map[model.TaskID]mergeJob{}, goAgain: map[model.TaskID]bool{},
		checking: map[model.TaskID]bool{}}
	if err := checkMergeDeps(d); err != nil {
		return nil, err
	}
	if s.d.Notify == nil {
		s.d.Notify = notify.NewLog(d.Log)
	}
	for _, p := range d.Config.Projects {
		if p.Loop != config.LoopFanin {
			continue
		}
		if !d.Config.HostOf(p).Local {
			return nil, fmt.Errorf("%w: project %s runs on host %q; set loop: %s there until remote hosts are "+
				"scheduled", ErrRemoteHost, p.Name, p.Host, config.LoopBash)
		}
		s.projects = append(s.projects, p)
	}
	return s, nil
}

// Projects names the projects the scheduler drives.
func (s *Scheduler) Projects() []string {
	out := make([]string, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p.Name)
	}
	return out
}

// Run resumes every live run, then reacts until ctx ends; a stop by ctx is no failure of any task.
func (s *Scheduler) Run(ctx context.Context) error {
	seq, err := s.d.Store.LastEventSeq(ctx)
	if err != nil {
		return fmt.Errorf("sched: read the event cursor: %w", err)
	}
	s.cursor = seq
	if err := s.resume(ctx); err != nil {
		return err
	}
	for {
		changed := s.d.Store.Changed()
		s.step(ctx)
		wake, err := s.nextWake(ctx)
		if err != nil {
			s.fail(ctx, err)
		}
		if s.d.Waiting != nil {
			s.d.Waiting()
		}
		select {
		case <-ctx.Done():
			s.unwatchAll()
			return nil
		case <-changed:
		case <-wake:
		case fn := <-s.done:
			fn(ctx)
		}
	}
}

// step reads new store events, fires due timers and fills free slots; it writes nothing when nothing changed.
func (s *Scheduler) step(ctx context.Context) {
	if s.d.Clock.Now().Before(s.retryAt) {
		return
	}
	s.retryAt = time.Time{}
	err := s.readEvents(ctx)
	if err == nil {
		err = s.fireTimers(ctx)
	}
	if err == nil {
		err = s.dispatch(ctx)
	}
	if err != nil {
		s.fail(ctx, err)
	}
}

// fail logs a failed step and holds the loop for RetryAfter; a stop by ctx is not logged.
func (s *Scheduler) fail(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	s.d.Log.ErrorContext(ctx, "sched: step failed, retrying", "err", err, "retry_in", RetryAfter)
	s.retryAt = s.d.Clock.Now().Add(RetryAfter)
}

// nextWake returns a channel that fires at the next armed timer or retry, or nil when nothing is due.
func (s *Scheduler) nextWake(ctx context.Context) (<-chan time.Time, error) {
	var next time.Time
	if !s.retryAt.IsZero() {
		next = s.retryAt
	} else {
		timers, err := s.d.Store.ArmedTimers(ctx)
		if err != nil {
			return nil, fmt.Errorf("sched: read the armed timers: %w", err)
		}
		for _, t := range timers {
			if _, ok := s.project(t.Project); !ok || !ownTimer(t.Kind) {
				continue
			}
			if next.IsZero() || t.DueAt.Before(next) {
				next = t.DueAt
			}
		}
	}
	if next.IsZero() {
		return nil, nil
	}
	return s.d.Clock.After(next.Sub(s.d.Clock.Now())), nil
}

// post hands fn to the loop goroutine; it gives up when ctx ends.
func (s *Scheduler) post(ctx context.Context, fn func(context.Context)) {
	select {
	case s.done <- fn:
	case <-ctx.Done():
	}
}

// readEvents applies every store event after the cursor.
func (s *Scheduler) readEvents(ctx context.Context) error {
	for {
		evs, err := s.d.Store.EventsAfter(ctx, s.cursor, EventBatch)
		if err != nil {
			return fmt.Errorf("sched: read the events after %d: %w", s.cursor, err)
		}
		for _, ev := range evs {
			if err := s.onStoreEvent(ctx, ev); err != nil {
				return err
			}
			s.cursor = ev.Seq
		}
		if len(evs) < EventBatch {
			return nil
		}
	}
}

func (s *Scheduler) project(name string) (config.Project, bool) {
	for _, p := range s.projects {
		if p.Name == name {
			return p, true
		}
	}
	return config.Project{}, false
}

func (s *Scheduler) logFor(project string, task model.TaskID, run model.RunID) *slog.Logger {
	return s.d.Log.With("project", project, "task_id", string(task), "run_id", string(run))
}
