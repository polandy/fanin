package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/model"
)

const taskColumns = `id, project, title, prompt, shape, state, owner_task, after_ids, owner_model, project_model,
	picked_model, created_at, updated_at`

const runColumns = `id, task_id, number, slot, branch, token, fix_rounds, review_nudges, stalls, input_tokens,
	output_tokens, total_tokens, state, result, started_at, ended_at`

type scanner interface{ Scan(dest ...any) error }

func scanTask(sc scanner) (model.Task, error) {
	var (
		t                       model.Task
		id, shape, state, after string
		ownerTask, created, upd int64
	)
	if err := sc.Scan(&id, &t.Project, &t.Title, &t.Prompt, &shape, &state, &ownerTask, &after, &t.OwnerModel,
		&t.ProjectModel, &t.PickedModel, &created, &upd); err != nil {
		return model.Task{}, err
	}
	t.ID = model.TaskID(id)
	var err error
	if t.Shape, err = model.ParseShape(shape); err != nil {
		return model.Task{}, corrupt("task", id, err)
	}
	if t.State, err = model.ParseTaskState(state); err != nil {
		return model.Task{}, corrupt("task", id, err)
	}
	if err := json.Unmarshal([]byte(after), &t.After); err != nil {
		return model.Task{}, corrupt("task", id, err)
	}
	if len(t.After) == 0 {
		t.After = nil
	}
	t.OwnerTask = ownerTask != 0
	t.CreatedAt, t.UpdatedAt = fromDB(created), fromDB(upd)
	return t, nil
}

func scanRun(sc scanner) (model.Run, error) {
	var (
		r                           model.Run
		id, task, token, state, res string
		started, ended              int64
	)
	b := &r.Budget
	if err := sc.Scan(&id, &task, &r.Number, &r.Slot, &r.Branch, &token, &b.FixRounds, &b.ReviewNudges, &b.Stalls,
		&b.InputTokens, &b.OutputTokens, &b.TotalTokens, &state, &res, &started, &ended); err != nil {
		return model.Run{}, err
	}
	r.ID, r.TaskID, r.Token = model.RunID(id), model.TaskID(task), model.Token(token)
	var err error
	if r.State, err = model.ParseRunState(state); err != nil {
		return model.Run{}, corrupt("run", id, err)
	}
	if res != "" {
		r.Result = &model.Result{}
		if err := json.Unmarshal([]byte(res), r.Result); err != nil {
			return model.Run{}, corrupt("run", id, err)
		}
	}
	r.StartedAt, r.EndedAt = fromDB(started), fromDB(ended)
	return r, nil
}

func scanEvent(sc scanner) (model.Event, error) {
	var (
		ev             model.Event
		typ, task, run string
		at             int64
	)
	if err := sc.Scan(&ev.Seq, &typ, &ev.Project, &task, &run, &at, &ev.Detail); err != nil {
		return model.Event{}, err
	}
	var err error
	if ev.Type, err = model.ParseEventType(typ); err != nil {
		return model.Event{}, corrupt("event", fmt.Sprint(ev.Seq), err)
	}
	ev.TaskID, ev.RunID, ev.At = model.TaskID(task), model.RunID(run), fromDB(at)
	return ev, nil
}

func readTask(ctx context.Context, q querier, id model.TaskID) (model.Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, string(id)))
	if err != nil {
		return model.Task{}, notFound(err, "task", string(id))
	}
	return t, nil
}

func readRun(ctx context.Context, q querier, id model.RunID) (model.Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, string(id)))
	if err != nil {
		return model.Run{}, notFound(err, "run", string(id))
	}
	return r, nil
}

// Task reads one task; a missing one is ErrNotFound.
func (s *Store) Task(ctx context.Context, id model.TaskID) (model.Task, error) {
	return readTask(ctx, s.db, id)
}

// Run reads one run; a missing one is ErrNotFound.
func (s *Store) Run(ctx context.Context, id model.RunID) (model.Run, error) {
	return readRun(ctx, s.db, id)
}

// Runs returns the runs of a task by number.
func (s *Store) Runs(ctx context.Context, task model.TaskID) ([]model.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE task_id = ? ORDER BY number`,
		string(task))
	if err != nil {
		return nil, fmt.Errorf("store: read runs of task %q: %w", task, err)
	}
	return scanAll(rows, scanRun)
}

// Events returns the event log of a task in append order.
func (s *Store) Events(ctx context.Context, task model.TaskID) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, type, project, task_id, run_id, at, detail FROM events WHERE task_id = ? ORDER BY seq`,
		string(task))
	if err != nil {
		return nil, fmt.Errorf("store: read events of task %q: %w", task, err)
	}
	return scanAll(rows, scanEvent)
}

// OpenTasks returns every task that is not in a terminal state, oldest first; a restart resumes these.
func (s *Store) OpenTasks(ctx context.Context) ([]model.Task, error) {
	var terminal []any
	for _, st := range model.AllTaskStates {
		if st.Terminal() {
			terminal = append(terminal, string(st))
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(terminal)), ", ")
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE state NOT IN (`+marks+`)
		ORDER BY created_at, id`, terminal...)
	if err != nil {
		return nil, fmt.Errorf("store: read open tasks: %w", err)
	}
	return scanAll(rows, scanTask)
}

// EventsAfter returns up to limit events with a sequence number above seq, in append order.
func (s *Store) EventsAfter(ctx context.Context, seq int64, limit int) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, type, project, task_id, run_id, at, detail FROM events WHERE seq > ? ORDER BY seq LIMIT ?`,
		seq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read events after %d: %w", seq, err)
	}
	return scanAll(rows, scanEvent)
}
