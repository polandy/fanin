package sched

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

// ErrNoWorker is an owner answer whose question names no live run to deliver to.
var ErrNoWorker = errors.New("sched: no live worker for the answer")

// unreadableNote is the note on a dialog the owner can only answer in the pane (SCENARIOS AW-9).
const unreadableNote = "%s shows a dialog fanin cannot answer (dialog: %s); answer it in the pane"

// Questions is the owner channel the scheduler hands workers' dialogs and pane answers to; *owner.Service is the
// real one.
type Questions interface {
	AskDialog(ctx context.Context, d owner.Dialog) (string, error)
	AnsweredInPane(ctx context.Context, run model.RunID) (int, error)
}

// OwnerDelivery is the owner.Deliverer that hands an answer to the question's run: a key press for a dialog option,
// a prompt for a free-text answer (SCENARIOS OA-1, OA-2, OA-3).
type OwnerDelivery struct {
	Store   *store.Store
	Runtime runtime.WorkerRuntime
}

// Deliver sends d to the worker of its question's run; a question without a live run is ErrNoWorker.
func (o OwnerDelivery) Deliver(ctx context.Context, d owner.Delivery) error {
	q := d.Question
	if q.RunID == "" {
		return fmt.Errorf("%w: question %q of task %q names no run; answer the worker by hand", ErrNoWorker, q.ID,
			q.TaskID)
	}
	run, err := o.Store.Run(ctx, q.RunID)
	if err != nil {
		return fmt.Errorf("sched: read run %s of question %q: %w", q.RunID, q.ID, err)
	}
	if run.State.Terminal() || run.Worker.Agent == "" {
		return fmt.Errorf("%w: run %s of question %q is %s; cancel the task or answer a newer question",
			ErrNoWorker, run.ID, q.ID, run.State)
	}
	h := handle(q.Project, run)
	if d.Mode == owner.DeliverKey {
		return o.Runtime.Press(ctx, h, strconv.Itoa(d.Option))
	}
	return o.Runtime.Prompt(ctx, h, d.Text)
}

// payOwner hands the watch's owed dialog or pane answer to the owner channel; a failure keeps it owed for the next
// step. A dialog fanin cannot answer only sends a note (SCENARIOS AW-9, WA-3).
func (s *Scheduler) payOwner(ctx context.Context, w *watch) error {
	ev := w.owed
	if ev == nil || s.d.Owner == nil {
		w.owed = nil
		return nil
	}
	var err error
	switch ev.Type {
	case runtime.RunEventDialog:
		if len(ev.Options) > 0 {
			_, err = s.d.Owner.AskDialog(ctx, owner.Dialog{TaskID: w.run.TaskID, RunID: w.run.ID, Text: ev.Dialog,
				Options: ev.Options})
		}
		if len(ev.Options) == 0 || errors.Is(err, model.ErrInvalidQuestion) {
			err = nil
			s.noteDialog(ctx, w, ev.Dialog)
		}
	case runtime.RunEventAnsweredInPane:
		_, err = s.d.Owner.AnsweredInPane(ctx, w.run.ID)
	}
	if err != nil {
		return fmt.Errorf("sched: hand the %s of run %s to the owner: %w", ev.Type, w.run.ID, err)
	}
	w.owed = nil
	return nil
}

// noteDialog tells the owner about a dialog that has no question; a failed note is logged.
func (s *Scheduler) noteDialog(ctx context.Context, w *watch, dialog string) {
	log := s.logFor(w.project, w.run.TaskID, w.run.ID)
	n := notify.Note{Project: w.project, TaskID: w.run.TaskID, Text: fmt.Sprintf(unreadableNote, w.run.Worker.Agent, dialog)}
	if err := s.d.Notify.Notify(ctx, n); err != nil {
		log.ErrorContext(ctx, "sched: dialog note failed", "agent", w.run.Worker.Agent, "err", err)
	}
}

// payOwed retries what a watch still owes the owner channel.
func (s *Scheduler) payOwed(ctx context.Context) error {
	for _, w := range s.watched {
		if err := s.payOwner(ctx, w); err != nil {
			return err
		}
	}
	return nil
}
