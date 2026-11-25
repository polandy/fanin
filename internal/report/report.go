// Package report records worker reports that carry the current run's token (docs/dev/spec/worker-contract.md).
package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// heartbeatDetail is the progress event's detail when the worker sent no text.
const heartbeatDetail = "heartbeat"

// Errors of a refused report; nothing was recorded.
var (
	// ErrToken is a missing token or one that is not the run's.
	ErrToken = errors.New("report: wrong run token")
	// ErrStaleRun is a run that is no longer its task's current run: a newer run exists or it has ended.
	ErrStaleRun = errors.New("report: not the current run")
)

// Service checks a report's run token and records it in the store.
type Service struct {
	st  *store.Store
	log *slog.Logger
}

// New returns the service; a nil log uses slog.Default.
func New(st *store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, log: log}
}

// FromWire turns a worker's report into a model.Result; an unknown kind wraps model.ErrUnknownValue.
func FromWire(r faninmcp.Report) (model.Result, error) {
	kind, err := model.ParseResultKind(string(r.Kind))
	if err != nil {
		return model.Result{}, fmt.Errorf("report: %w", err)
	}
	return model.Result{Kind: kind, PR: r.PR, Branch: r.Branch, Issue: r.Issue, IssueURL: r.IssueURL,
		Text: r.Text}, nil
}

// Report stores res as the run's result and appends result_reported.
func (s *Service) Report(ctx context.Context, id model.RunID, token string, res model.Result) error {
	return s.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if _, _, err := s.current(ctx, tx, id, token); err != nil {
			return err
		}
		return tx.SetRunResult(ctx, id, res)
	})
}

// Progress appends a progress event with text cut to one line of model.FieldCap characters.
func (s *Service) Progress(ctx context.Context, id model.RunID, token, text string) error {
	return s.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		run, project, err := s.current(ctx, tx, id, token)
		if err != nil {
			return err
		}
		detail := model.CapField(text)
		if detail == "" {
			detail = heartbeatDetail
		}
		_, err = tx.AppendEvent(ctx, model.Event{
			Type: model.EventProgress, Project: project, TaskID: run.TaskID, RunID: id, Detail: detail,
		})
		return err
	})
}

// current returns the run and its project when token is the run's and the run is its task's current one.
func (s *Service) current(ctx context.Context, tx *store.Tx, id model.RunID, token string) (model.Run, string, error) {
	run, err := tx.Run(ctx, id)
	if err != nil {
		return model.Run{}, "", err
	}
	task, err := tx.Task(ctx, run.TaskID)
	if err != nil {
		return model.Run{}, "", err
	}
	log := s.log.With("task_id", string(run.TaskID), "run_id", string(id), "project", task.Project)
	if token == "" || run.Token == "" || !run.Token.Equal(token) {
		log.WarnContext(ctx, "report: refused, wrong run token")
		return model.Run{}, "", fmt.Errorf("%w: run %q; send the token fanind gave this run in $%s",
			ErrToken, id, faninmcp.EnvRunToken)
	}
	latest, err := tx.LatestRun(ctx, run.TaskID)
	if err != nil {
		return model.Run{}, "", err
	}
	if latest.ID != id || run.State.Terminal() {
		log.WarnContext(ctx, "report: refused, not the current run", "current_run", string(latest.ID),
			"state", string(run.State))
		return model.Run{}, "", fmt.Errorf("%w: run %q (%s) of task %q; the current run is %q, stop this worker",
			ErrStaleRun, id, run.State, run.TaskID, latest.ID)
	}
	return run, task.Project, nil
}
