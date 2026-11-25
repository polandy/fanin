// Package hook runs a project's post-merge hook under a merge hold (concept D4, SCENARIOS MG-1, MG-4, MG-5).
package hook

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

// The shell that runs the hook command and its flag.
const (
	Shell     = "sh"
	ShellFlag = "-c"
)

// The environment the hook gets on top of fanind's; there is no lane variable.
const (
	EnvProject = "FANIN_PROJECT"
	EnvPR      = "FANIN_PR"
)

// FailedPrefix leads the reason of the hold a failed hook leaves.
const FailedPrefix = "failed: "

// Sentinel errors of the package.
var (
	// ErrFailed matches a hook that ran and failed; its hold stays.
	ErrFailed = errors.New("hook: post-merge hook failed")
	// ErrInvalid matches a request that cannot run.
	ErrInvalid = errors.New("hook: invalid request")
)

// RunningReason is the reason of the hold while the hook after pr runs.
func RunningReason(pr int) string {
	return "post-merge hook after #" + strconv.Itoa(pr) + " running; the deploy state is unknown until it ends"
}

// Request is one hook run after the merge of PR in Project.
type Request struct {
	Project string
	Task    model.TaskID
	PR      int
	// Command is the project's post_merge_hook; empty runs nothing.
	Command string
	// Dir is fanind's state directory, the hook's working directory.
	Dir string
}

// FailedError is a hook that ran and failed: merges of Project stay held by HoldID until the owner lifts it.
type FailedError struct {
	Project  string
	PR       int
	HoldID   string
	LastLine string
	Err      error
}

// Error names the project, the PR, the last output line and the lift command.
func (e *FailedError) Error() string {
	return fmt.Sprintf("hook: post-merge hook of project %q after #%d failed: %s; merging held until 'fanin hold lift %s'",
		e.Project, e.PR, e.LastLine, e.HoldID)
}

// Unwrap returns ErrFailed and the cause.
func (e *FailedError) Unwrap() []error { return []error{ErrFailed, e.Err} }

// Hook runs post-merge hooks and keeps their holds in the store.
type Hook struct {
	st *store.Store
	r  runtime.Runner
}

// New returns a Hook that records in st and runs through r.
func New(st *store.Store, r runtime.Runner) *Hook { return &Hook{st: st, r: r} }

// Run writes a merge hold, runs the hook and lifts the hold on success. A failed hook leaves the hold with its last
// output line and returns a *FailedError; a ctx that ends inside the hook leaves the running hold (MG-5).
func (h *Hook) Run(ctx context.Context, req Request) error {
	if req.Command == "" {
		return nil
	}
	if err := req.validate(); err != nil {
		return err
	}
	ref := "#" + strconv.Itoa(req.PR)
	var id string
	err := h.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		if id, err = tx.SetHold(ctx, model.Hold{Project: req.Project, Kind: model.HoldMerge,
			Reason: RunningReason(req.PR)}); err != nil {
			return err
		}
		return req.event(ctx, tx, model.EventHookStarted, ref)
	})
	if err != nil {
		return fmt.Errorf("hook: hold project %q before the hook after %s: %w", req.Project, ref, err)
	}
	res, runErr := h.r.Run(ctx, runtime.Cmd{Argv: []string{Shell, ShellFlag, req.Command}, Dir: req.Dir,
		Env: []string{EnvProject + "=" + req.Project, EnvPR + "=" + strconv.Itoa(req.PR)}})
	if ctx.Err() != nil {
		return fmt.Errorf("hook: post-merge hook of project %q after %s cut off; hold %s stays: %w", req.Project, ref,
			id, ctx.Err())
	}
	if runErr == nil {
		err := h.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			if err := tx.LiftHold(ctx, id); err != nil {
				return err
			}
			return req.event(ctx, tx, model.EventHookFinished, ref)
		})
		if err != nil {
			return fmt.Errorf("hook: lift hold %s after the hook of project %q: %w", id, req.Project, err)
		}
		return nil
	}
	failed := &FailedError{Project: req.Project, PR: req.PR, HoldID: id, LastLine: tail(res, runErr), Err: runErr}
	err = h.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetHoldReason(ctx, id, FailedPrefix+failed.LastLine); err != nil {
			return err
		}
		return req.event(ctx, tx, model.EventHookFailed, failed.Error())
	})
	if err != nil {
		return fmt.Errorf("hook: record the failed hook of project %q after %s (hold %s): %w", req.Project, ref, id,
			err)
	}
	return failed
}

func (req Request) validate() error {
	switch {
	case req.Project == "":
		return fmt.Errorf("%w: no project; set Request.Project", ErrInvalid)
	case req.PR <= 0:
		return fmt.Errorf("%w: PR %d of project %q; set Request.PR to the merged PR", ErrInvalid, req.PR, req.Project)
	case req.Dir == "":
		return fmt.Errorf("%w: no state directory for the hook of project %q; set Request.Dir", ErrInvalid,
			req.Project)
	}
	return nil
}

func (req Request) event(ctx context.Context, tx *store.Tx, typ model.EventType, detail string) error {
	_, err := tx.AppendEvent(ctx, model.Event{Type: typ, Project: req.Project, TaskID: req.Task, Detail: detail})
	return err
}

// tail is the last non-empty line of stderr, else of stdout, else the error text.
func tail(res runtime.Result, err error) string {
	var exit *runtime.ExitError
	if errors.As(err, &exit) {
		if l := last(exit.StderrTail); l != "" {
			return l
		}
	}
	for _, out := range [][]byte{res.Stderr, res.Stdout} {
		if l := last(string(out)); l != "" {
			return l
		}
	}
	return err.Error()
}

func last(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n\t "), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
