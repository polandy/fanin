package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/model"
)

const questionColumns = `id, task_id, run_id, project, kind, allow_text, question, options, recommended, answer,
	asked_at, answered_at`

// AskQuestion stores a validated owner question, with a new ID when q.ID is empty and kind transcript when q.Kind
// is empty, and appends question_asked.
func (t *Tx) AskQuestion(ctx context.Context, q model.OwnerQuestion) (string, error) {
	if q.Kind == "" {
		q.Kind = model.QuestionTranscript
	}
	if err := q.Validate(); err != nil {
		return "", fmt.Errorf("store: question of task %q: %w", q.TaskID, err)
	}
	task, err := readTask(ctx, t.tx, q.TaskID)
	if err != nil {
		return "", err
	}
	if q.ID == "" {
		q.ID = rand.Text()
	}
	opts, err := json.Marshal(q.Options)
	if err != nil {
		return "", fmt.Errorf("store: encode options of question %q: %w", q.ID, err)
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO owner_questions (`+questionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, 0)`, q.ID, string(q.TaskID), string(q.RunID), task.Project,
		string(q.Kind), boolToDB(q.AllowText), q.Question, string(opts), q.Recommended, toDB(t.now)); err != nil {
		return "", fmt.Errorf("store: ask question %q of task %q: %w", q.ID, q.TaskID, err)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventQuestionAsked, Project: task.Project, TaskID: q.TaskID, RunID: q.RunID, Detail: q.Question,
	})
	return q.ID, err
}

// AnswerQuestion records the owner's answer to an open question and appends question_answered, the
// "owner answer for <run>: <answer>" line of the log (see SCENARIOS OA-2).
func (t *Tx) AnswerQuestion(ctx context.Context, id, answer string) error {
	if strings.TrimSpace(answer) == "" {
		return fmt.Errorf("%w: empty answer to question %q; pick an option or give a text", ErrInvalid, id)
	}
	q, err := scanQuestion(t.tx.QueryRowContext(ctx,
		`SELECT `+questionColumns+` FROM owner_questions WHERE id = ? AND answered_at = 0`, id))
	if err != nil {
		return notFound(err, "open question", id)
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE owner_questions SET answer = ?, answered_at = ? WHERE id = ?`,
		answer, toDB(t.now), id); err != nil {
		return fmt.Errorf("store: answer question %q: %w", id, err)
	}
	to := string(q.RunID)
	if to == "" {
		to = string(q.TaskID)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventQuestionAnswered, Project: q.Project, TaskID: q.TaskID, RunID: q.RunID,
		Detail: fmt.Sprintf("owner answer for %s: %s", to, answer),
	})
	return err
}

func scanQuestion(sc scanner) (model.OwnerQuestion, error) {
	var (
		q                    model.OwnerQuestion
		task, run, opt, kind string
		text, asked, ans     int64
	)
	if err := sc.Scan(&q.ID, &task, &run, &q.Project, &kind, &text, &q.Question, &opt, &q.Recommended, &q.Answer,
		&asked, &ans); err != nil {
		return model.OwnerQuestion{}, err
	}
	var err error
	if q.Kind, err = model.ParseQuestionKind(kind); err != nil {
		return model.OwnerQuestion{}, corrupt("question", q.ID, err)
	}
	q.AllowText = text != 0
	if err := json.Unmarshal([]byte(opt), &q.Options); err != nil {
		return model.OwnerQuestion{}, corrupt("question", q.ID, err)
	}
	q.TaskID, q.RunID, q.AskedAt, q.AnsweredAt = model.TaskID(task), model.RunID(run), fromDB(asked), fromDB(ans)
	return q, nil
}

// Question reads one owner question; a missing one is ErrNotFound.
func (s *Store) Question(ctx context.Context, id string) (model.OwnerQuestion, error) {
	q, err := scanQuestion(s.db.QueryRowContext(ctx, `SELECT `+questionColumns+` FROM owner_questions WHERE id = ?`,
		id))
	if err != nil {
		return model.OwnerQuestion{}, notFound(err, "question", id)
	}
	return q, nil
}

// openTaskQuestion selects the newest open question of a task.
const openTaskQuestion = `SELECT ` + questionColumns + ` FROM owner_questions
	WHERE task_id = ? AND answered_at = 0 ORDER BY asked_at DESC, id DESC LIMIT 1`

// OpenTaskQuestion reads the newest open question of a task; none is ErrNotFound.
func (s *Store) OpenTaskQuestion(ctx context.Context, task model.TaskID) (model.OwnerQuestion, error) {
	q, err := scanQuestion(s.db.QueryRowContext(ctx, openTaskQuestion, string(task)))
	if err != nil {
		return model.OwnerQuestion{}, notFound(err, "open question of task", string(task))
	}
	return q, nil
}

// OpenTaskQuestion reads the newest open question of a task inside the transaction; none is ErrNotFound.
func (t *Tx) OpenTaskQuestion(ctx context.Context, task model.TaskID) (model.OwnerQuestion, error) {
	q, err := scanQuestion(t.tx.QueryRowContext(ctx, openTaskQuestion, string(task)))
	if err != nil {
		return model.OwnerQuestion{}, notFound(err, "open question of task", string(task))
	}
	return q, nil
}

// OpenRunQuestions returns the unanswered questions of a run inside the transaction, oldest first.
func (t *Tx) OpenRunQuestions(ctx context.Context, run model.RunID) ([]model.OwnerQuestion, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+questionColumns+` FROM owner_questions
		WHERE run_id = ? AND answered_at = 0 ORDER BY asked_at, id`, string(run))
	if err != nil {
		return nil, fmt.Errorf("store: read open questions of run %q: %w", run, err)
	}
	return scanAll(rows, scanQuestion)
}

// OpenQuestions returns every unanswered owner question, oldest first.
func (s *Store) OpenQuestions(ctx context.Context) ([]model.OwnerQuestion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+questionColumns+` FROM owner_questions WHERE answered_at = 0
		ORDER BY asked_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: read open questions: %w", err)
	}
	return scanAll(rows, scanQuestion)
}

// ProjectQuestions returns every owner question of a project, open or answered, oldest first.
func (s *Store) ProjectQuestions(ctx context.Context, project string) ([]model.OwnerQuestion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+questionColumns+` FROM owner_questions WHERE project = ?
		ORDER BY asked_at, id`, project)
	if err != nil {
		return nil, fmt.Errorf("store: read questions of project %q: %w", project, err)
	}
	return scanAll(rows, scanQuestion)
}
