package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/model"
)

// Tx is one state change in progress. Every write appends its event; all of it commits together or not at all.
type Tx struct {
	tx  *sql.Tx
	now time.Time
}

// Now is the time the transaction stamps on its rows and events.
func (t *Tx) Now() time.Time { return t.now }

// Transition runs fn in one transaction. It commits when fn returns nil and rolls back every row and event
// otherwise. fn reads and writes only through tx: a Store method called inside fn blocks on the one connection.
func (s *Store) Transition(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		return fn(ctx, &Tx{tx: tx, now: s.clock.Now()})
	})
	if err == nil {
		s.signalChanged()
	}
	return err
}

// Changed returns a channel that is closed after the next committed Transition. Take it before reading, then
// wait on it, so a commit in between is never missed.
func (s *Store) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *Store) signalChanged() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// AppendEvent appends ev, stamped with the transaction time when At is zero, and returns its sequence number.
func (t *Tx) AppendEvent(ctx context.Context, ev model.Event) (int64, error) {
	if _, err := model.ParseEventType(string(ev.Type)); err != nil {
		return 0, fmt.Errorf("%w: event: %w", ErrInvalid, err)
	}
	if ev.Project == "" {
		return 0, fmt.Errorf("%w: event %q has no project; set Event.Project", ErrInvalid, ev.Type)
	}
	if ev.At.IsZero() {
		ev.At = t.now
	}
	res, err := t.tx.ExecContext(ctx,
		`INSERT INTO events (type, project, task_id, run_id, at, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		string(ev.Type), ev.Project, string(ev.TaskID), string(ev.RunID), toDB(ev.At), model.CapField(ev.Detail))
	if err != nil {
		return 0, fmt.Errorf("store: append event %q of task %q: %w", ev.Type, ev.TaskID, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append event %q of task %q: %w", ev.Type, ev.TaskID, err)
	}
	return seq, nil
}

// CreateTask inserts task (state queued when empty) and appends task_created.
func (t *Tx) CreateTask(ctx context.Context, task model.Task) error {
	if task.ID == "" || task.Project == "" {
		return fmt.Errorf("%w: task %q of project %q; set Task.ID and Task.Project", ErrInvalid, task.ID, task.Project)
	}
	if task.State == "" {
		task.State = model.TaskQueued
	}
	if _, err := model.ParseShape(string(task.Shape)); err != nil {
		return fmt.Errorf("%w: task %q: %w", ErrInvalid, task.ID, err)
	}
	if _, err := model.ParseTaskState(string(task.State)); err != nil {
		return fmt.Errorf("%w: task %q: %w", ErrInvalid, task.ID, err)
	}
	after, err := json.Marshal(nonNil(task.After))
	if err != nil {
		return fmt.Errorf("store: encode after of task %q: %w", task.ID, err)
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO tasks (id, project, title, prompt, shape, state, owner_task,
		after_ids, owner_model, project_model, picked_model, created_at, updated_at, worker_kind)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(task.ID), task.Project, task.Title, task.Prompt, string(task.Shape), string(task.State),
		boolToDB(task.OwnerTask), string(after), task.OwnerModel, task.ProjectModel, task.PickedModel,
		toDB(t.now), toDB(t.now), task.WorkerKind); err != nil {
		return fmt.Errorf("store: create task %q: %w", task.ID, err)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventTaskCreated, Project: task.Project, TaskID: task.ID, Detail: task.Title,
	})
	return err
}

// SetTaskState moves a task to state to, if the model allows it, and appends task_state with detail.
func (t *Tx) SetTaskState(ctx context.Context, id model.TaskID, to model.TaskState, detail string) error {
	task, err := readTask(ctx, t.tx, id)
	if err != nil {
		return err
	}
	if err := task.State.CanTransition(to); err != nil {
		return fmt.Errorf("store: task %q: %w", id, err)
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`,
		string(to), toDB(t.now), string(id)); err != nil {
		return fmt.Errorf("store: set state of task %q: %w", id, err)
	}
	if detail == "" {
		detail = fmt.Sprintf("%s -> %s", task.State, to)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventTaskState, Project: task.Project, TaskID: id, Detail: detail,
	})
	return err
}

// Task reads one task inside the transaction.
func (t *Tx) Task(ctx context.Context, id model.TaskID) (model.Task, error) {
	return readTask(ctx, t.tx, id)
}

// CreateRun inserts run (state starting when empty, started now) with its worker and appends run_started.
func (t *Tx) CreateRun(ctx context.Context, run model.Run) error {
	if run.ID == "" || run.TaskID == "" || run.Number < 1 {
		return fmt.Errorf("%w: run %q of task %q number %d; set Run.ID, Run.TaskID and a Number from 1",
			ErrInvalid, run.ID, run.TaskID, run.Number)
	}
	if run.State == "" {
		run.State = model.RunStarting
	}
	if _, err := model.ParseRunState(string(run.State)); err != nil {
		return fmt.Errorf("%w: run %q: %w", ErrInvalid, run.ID, err)
	}
	task, err := readTask(ctx, t.tx, run.TaskID)
	if err != nil {
		return err
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = t.now
	}
	b, w := run.Budget, run.Worker
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO runs (id, task_id, number, slot, branch, token, fix_rounds,
		review_nudges, stalls, input_tokens, output_tokens, total_tokens, state, result, started_at, ended_at,
		host, agent, workspace, tab, pane, context_pct, footer_waits, continue_nudges, secure_nudges, hint, tail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(run.ID), string(run.TaskID), run.Number, run.Slot, run.Branch, string(run.Token), b.FixRounds,
		b.ReviewNudges, b.Stalls, b.InputTokens, b.OutputTokens, b.TotalTokens, string(run.State),
		toDB(run.StartedAt), w.Host, w.Agent, w.Workspace, w.Tab, w.Pane, b.ContextPct, b.FooterWaits,
		b.ContinueNudges, b.SecureNudges, run.Hint, run.Tail); err != nil {
		return fmt.Errorf("store: create run %q of task %q: %w", run.ID, run.TaskID, err)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventRunStarted, Project: task.Project, TaskID: run.TaskID, RunID: run.ID,
		Detail: fmt.Sprintf("run %d", run.Number),
	})
	return err
}

// SetRunState moves a run to state to, if the model allows it, and appends run_state; a terminal state ends it.
func (t *Tx) SetRunState(ctx context.Context, id model.RunID, to model.RunState, detail string) error {
	run, project, err := t.runWithProject(ctx, id)
	if err != nil {
		return err
	}
	if err := run.State.CanTransition(to); err != nil {
		return fmt.Errorf("store: run %q: %w", id, err)
	}
	var ended int64
	if to.Terminal() {
		ended = toDB(t.now)
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE runs SET state = ?, ended_at = ? WHERE id = ?`,
		string(to), ended, string(id)); err != nil {
		return fmt.Errorf("store: set state of run %q: %w", id, err)
	}
	if detail == "" {
		detail = fmt.Sprintf("%s -> %s", run.State, to)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventRunState, Project: project, TaskID: run.TaskID, RunID: id, Detail: detail,
	})
	return err
}

// SetRunWorker stores where the run's agent lives once its start succeeded; it appends no event.
func (t *Tx) SetRunWorker(ctx context.Context, id model.RunID, w model.Worker) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE runs SET host = ?, agent = ?, workspace = ?, tab = ?, pane = ?
		WHERE id = ?`, w.Host, w.Agent, w.Workspace, w.Tab, w.Pane, string(id))
	if err != nil {
		return fmt.Errorf("store: set worker of run %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: set worker of run %q: %w", id, err)
		}
		return fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return nil
}

// Run reads one run inside the transaction; a missing one is ErrNotFound.
func (t *Tx) Run(ctx context.Context, id model.RunID) (model.Run, error) {
	return readRun(ctx, t.tx, id)
}

// LatestRun reads the task's run with the highest number; a task without runs is ErrNotFound.
func (t *Tx) LatestRun(ctx context.Context, task model.TaskID) (model.Run, error) {
	r, err := scanRun(t.tx.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE task_id = ? ORDER BY number DESC LIMIT 1`, string(task)))
	if err != nil {
		return model.Run{}, notFound(err, "latest run of task", string(task))
	}
	return r, nil
}

// SetRunResult stores the worker's validated, normalised report and appends result_reported.
func (t *Tx) SetRunResult(ctx context.Context, id model.RunID, res model.Result) error {
	res = res.Normalize()
	if err := res.Validate(); err != nil {
		return fmt.Errorf("store: result of run %q: %w", id, err)
	}
	run, project, err := t.runWithProject(ctx, id)
	if err != nil {
		return err
	}
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("store: encode result of run %q: %w", id, err)
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE runs SET result = ? WHERE id = ?`, string(body), string(id)); err != nil {
		return fmt.Errorf("store: set result of run %q: %w", id, err)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventResultReported, Project: project, TaskID: run.TaskID, RunID: id, Detail: string(res.Kind),
	})
	return err
}

// SetRunBudget stores what the run has spent so far and appends token_usage.
func (t *Tx) SetRunBudget(ctx context.Context, id model.RunID, b model.Budget) error {
	run, project, err := t.runWithProject(ctx, id)
	if err != nil {
		return err
	}
	if err := t.writeBudget(ctx, id, b); err != nil {
		return err
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventTokenUsage, Project: project, TaskID: run.TaskID, RunID: id,
		Detail: fmt.Sprintf("%d tokens", b.TotalTokens),
	})
	return err
}

// SetRunCounts stores the run's budget like SetRunBudget but appends no event; the scheduler counts its stalls and
// nudges with it.
func (t *Tx) SetRunCounts(ctx context.Context, id model.RunID, b model.Budget) error {
	return t.writeBudget(ctx, id, b)
}

func (t *Tx) writeBudget(ctx context.Context, id model.RunID, b model.Budget) error {
	return t.setRun(ctx, id, "budget", `UPDATE runs SET fix_rounds = ?, review_nudges = ?, stalls = ?,
		input_tokens = ?, output_tokens = ?, total_tokens = ?, context_pct = ?, footer_waits = ?, continue_nudges = ?,
		secure_nudges = ? WHERE id = ?`, b.FixRounds, b.ReviewNudges, b.Stalls, b.InputTokens, b.OutputTokens,
		b.TotalTokens, b.ContextPct, b.FooterWaits, b.ContinueNudges, b.SecureNudges, string(id))
}

// SetRunTail stores the worker's pane tail at its latest stop; it appends no event (SCENARIOS AW-12).
func (t *Tx) SetRunTail(ctx context.Context, id model.RunID, tail string) error {
	return t.setRun(ctx, id, "tail", `UPDATE runs SET tail = ? WHERE id = ?`, tail, string(id))
}

// SetRunBranch stores the branch the run's worker named; it appends no event.
func (t *Tx) SetRunBranch(ctx context.Context, id model.RunID, branch string) error {
	return t.setRun(ctx, id, "branch", `UPDATE runs SET branch = ? WHERE id = ?`, branch, string(id))
}

// setRun runs one UPDATE of a run row; a missing run is ErrNotFound.
func (t *Tx) setRun(ctx context.Context, id model.RunID, what, query string, args ...any) error {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: set %s of run %q: %w", what, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set %s of run %q: %w", what, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return nil
}

func (t *Tx) runWithProject(ctx context.Context, id model.RunID) (model.Run, string, error) {
	run, err := readRun(ctx, t.tx, id)
	if err != nil {
		return model.Run{}, "", err
	}
	task, err := readTask(ctx, t.tx, run.TaskID)
	if err != nil {
		return model.Run{}, "", err
	}
	return run, task.Project, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
