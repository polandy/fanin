// Package owner is the owner channel: questions to the owner, their answers, the go on a ready task, hold lifts
// and project pauses (concept: migration step 4, C-LOOP; spec in docs/dev/spec/owner.md).
package owner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/store"
)

// MaxOptionNumber is the highest option number an answer can press (see SCENARIOS OA-1).
const MaxOptionNumber = 9

// Texts the owner channel writes into questions and task_state events.
const (
	// unreadableDialog is the question text of a dialog whose text could not be read (see SCENARIOS AW-9).
	unreadableDialog = "dialog: unreadable"
	askedPrefix      = "asks the owner: "
	answeredDetail   = "owner answered"
	notifiedDetail   = "owner notified of question "
	notifyFailedText = "owner note for question %s failed: %v"
)

// Errors of the owner channel; the API maps them to status codes.
var (
	// ErrInvalidAnswer is an answer the question does not take; nothing was sent or recorded.
	ErrInvalidAnswer = errors.New("owner: invalid answer")
	// ErrDelivery is a failed delivery to the worker; nothing was recorded.
	ErrDelivery = errors.New("owner: delivery failed")
	// ErrNotReady is a go on a task that is not waiting for one.
	ErrNotReady = errors.New("owner: task not ready")
	// ErrNoDeliverer is returned by NoDeliverer.
	ErrNoDeliverer = errors.New("owner: no deliverer")
)

// DeliveryMode is how an answer reaches the worker.
type DeliveryMode string

// The delivery modes.
const (
	// DeliverKey presses the option number in the worker's dialog.
	DeliverKey DeliveryMode = "key"
	// DeliverPrompt sends the answer as the worker's next prompt.
	DeliverPrompt DeliveryMode = "prompt"
)

// Delivery is one validated answer on its way to the worker.
type Delivery struct {
	Question model.OwnerQuestion
	Mode     DeliveryMode
	// Option is the picked option number from 1, or 0 for free text.
	Option int
	// Text is the picked option's label or the free text.
	Text string
}

// Deliverer hands an answer to the worker of the question's run. The scheduler provides the real one.
type Deliverer interface {
	Deliver(ctx context.Context, d Delivery) error
}

// NoDeliverer refuses every delivery, so an answer fails closed until the scheduler delivers to workers.
type NoDeliverer struct{}

// Deliver returns ErrNoDeliverer.
func (NoDeliverer) Deliver(_ context.Context, d Delivery) error {
	return fmt.Errorf("%w: answer to question %q cannot reach run %q; fanind has no worker delivery yet",
		ErrNoDeliverer, d.Question.ID, d.Question.RunID)
}

// Service runs the owner commands against the store.
type Service struct {
	store   *store.Store
	deliver Deliverer
	log     *slog.Logger
	// notifier gets a note per new question; nil sends none.
	notifier notify.Notifier
	// answering serialises answers, so one question is never delivered twice.
	answering sync.Mutex
}

// New returns the owner channel over st; log may be nil.
func New(st *store.Store, d Deliverer, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, deliver: d, log: log}
}

// SetNotifier sets the push channel that gets a note per new question; call it before serving.
func (s *Service) SetNotifier(n notify.Notifier) { s.notifier = n }

// Dialog is a dialog read from a worker's pane: its text and the options it numbers.
type Dialog struct {
	TaskID  model.TaskID
	RunID   model.RunID
	Text    string
	Options []string
}

// Ask stores q and moves its task to needs_owner in one transition (see SCENARIOS AW-9).
func (s *Service) Ask(ctx context.Context, q model.OwnerQuestion) (string, error) {
	var id, project string
	err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, q.TaskID)
		if err != nil {
			return err
		}
		project = task.Project
		if id, err = tx.AskQuestion(ctx, q); err != nil {
			return err
		}
		if task.State == model.TaskNeedsOwner {
			return nil
		}
		return tx.SetTaskState(ctx, q.TaskID, model.TaskNeedsOwner, askedPrefix+q.Question)
	})
	if err != nil {
		return "", fmt.Errorf("owner: ask for task %q: %w", q.TaskID, err)
	}
	s.notifyQuestion(ctx, project, id, q)
	return id, nil
}

// notifyQuestion pushes a note about the stored question and logs the outcome as an event. A failed push
// is logged and never undoes the question (see SCENARIOS OA-7).
func (s *Service) notifyQuestion(ctx context.Context, project, id string, q model.OwnerQuestion) {
	if s.notifier == nil {
		return
	}
	ev := model.Event{Type: model.EventNotified, Project: project, TaskID: q.TaskID, RunID: q.RunID,
		Detail: notifiedDetail + id}
	if err := s.notifier.Notify(ctx, notify.Note{Project: project, TaskID: q.TaskID, Text: q.Question}); err != nil {
		ev.Type = model.EventNotifyFailed
		ev.Detail = fmt.Sprintf(notifyFailedText, id, err)
		s.log.ErrorContext(ctx, "owner: note failed", "task_id", q.TaskID, "run_id", q.RunID, "project", project, "err", err)
	}
	err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil {
		s.log.ErrorContext(ctx, "owner: note event not recorded", "task_id", q.TaskID, "run_id", q.RunID,
			"project", project, "err", err)
	}
}

// AskDialog turns a worker's dialog into a dialog question; unreadable text is said so (see SCENARIOS AW-9).
func (s *Service) AskDialog(ctx context.Context, d Dialog) (string, error) {
	text := strings.TrimSpace(d.Text)
	if text == "" {
		text = unreadableDialog
	}
	opts := make([]model.Option, 0, len(d.Options))
	for _, o := range d.Options {
		opts = append(opts, model.Option{Label: o})
	}
	return s.Ask(ctx, model.OwnerQuestion{TaskID: d.TaskID, RunID: d.RunID, Kind: model.QuestionDialog,
		Question: text, Options: opts})
}

// Answer validates raw against the task's open question, delivers it and only then records it. An invalid
// answer or a failed delivery records nothing (see SCENARIOS OA-1, OA-2, OA-3).
func (s *Service) Answer(ctx context.Context, task model.TaskID, raw string) (Delivery, error) {
	s.answering.Lock()
	defer s.answering.Unlock()
	q, err := s.store.OpenTaskQuestion(ctx, task)
	if err != nil {
		return Delivery{}, fmt.Errorf("owner: answer task %q: %w", task, err)
	}
	d, err := Parse(q, raw)
	if err != nil {
		return Delivery{}, err
	}
	if err := s.deliver.Deliver(ctx, d); err != nil {
		return Delivery{}, fmt.Errorf("%w: question %q of task %q: %w; nothing was recorded, answer again",
			ErrDelivery, q.ID, task, err)
	}
	err = s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.AnswerQuestion(ctx, q.ID, d.Text); err != nil {
			return err
		}
		return resumeTask(ctx, tx, q)
	})
	if err != nil {
		s.log.ErrorContext(ctx, "owner: answer delivered but not recorded", "task_id", task, "run_id", q.RunID,
			"project", q.Project, "err", err)
		return Delivery{}, fmt.Errorf("owner: record the answer to question %q: %w", q.ID, err)
	}
	return d, nil
}

// resumeTask moves a needs_owner task on once its question is answered: back to its run, else to the queue.
func resumeTask(ctx context.Context, tx *store.Tx, q model.OwnerQuestion) error {
	task, err := tx.Task(ctx, q.TaskID)
	if err != nil {
		return err
	}
	if task.State != model.TaskNeedsOwner {
		return nil
	}
	to := model.TaskQueued
	if q.RunID != "" {
		to = model.TaskRunning
	}
	return tx.SetTaskState(ctx, q.TaskID, to, answeredDetail)
}

// Parse reads raw as an answer to q: an option number 1..MaxOptionNumber that q has, or free text when q allows
// it. Anything else is ErrInvalidAnswer, with the options (see SCENARIOS OA-1).
func Parse(q model.OwnerQuestion, raw string) (Delivery, error) {
	raw = strings.TrimSpace(raw)
	mode := DeliverPrompt
	if q.Kind == model.QuestionDialog {
		mode = DeliverKey
	}
	if len(raw) == 1 && raw[0] >= '1' && raw[0] <= '0'+MaxOptionNumber {
		n := int(raw[0] - '0')
		if n <= len(q.Options) {
			return Delivery{Question: q, Mode: mode, Option: n, Text: q.Options[n-1].Label}, nil
		}
		return Delivery{}, fmt.Errorf("%w: option %d of question %q; pick one of %s", ErrInvalidAnswer, n, q.ID,
			optionList(q))
	}
	if raw != "" && q.AllowText {
		return Delivery{Question: q, Mode: DeliverPrompt, Text: raw}, nil
	}
	return Delivery{}, fmt.Errorf("%w: %q to question %q takes only an option number; pick one of %s",
		ErrInvalidAnswer, raw, q.ID, optionList(q))
}

func optionList(q model.OwnerQuestion) string {
	parts := make([]string, 0, len(q.Options))
	for i, o := range q.Options {
		parts = append(parts, fmt.Sprintf("%d) %s", i+1, o.Label))
	}
	return strings.Join(parts, ", ")
}
