package owner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
)

var errPush = errors.New("push failed")

// fakeNotifier records every note and fails with err when set.
type fakeNotifier struct {
	got []notify.Note
	err error
}

func (f *fakeNotifier) Notify(_ context.Context, n notify.Note) error {
	f.got = append(f.got, n)
	return f.err
}

func TestAskNotifies(t *testing.T) {
	t.Run("OA-7_a_question_sends_a_note_and_logs_the_event_with_the_project", func(t *testing.T) {
		e := newEnv(t)
		fn := &fakeNotifier{}
		e.svc.SetNotifier(fn)
		e.ask(t, model.QuestionTranscript, true)
		if len(fn.got) != 1 {
			t.Fatalf("notes = %d, want 1", len(fn.got))
		}
		if n := fn.got[0]; n.Project != proj || n.TaskID != taskID || !strings.Contains(n.Text, "ship the cache now?") {
			t.Errorf("note = %+v", n)
		}
		evs, err := e.st.EventsAfter(context.Background(), 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, ev := range evs {
			if ev.Type == model.EventNotified && ev.Project == proj && ev.TaskID == taskID {
				found = true
			}
		}
		if !found {
			t.Errorf("no notified event with project %q in %v", proj, evs)
		}
	})
	t.Run("OA-7_a_failed_send_logs_an_error_event_and_the_question_stays", func(t *testing.T) {
		e := newEnv(t)
		e.svc.SetNotifier(&fakeNotifier{err: errPush})
		id := e.ask(t, model.QuestionTranscript, true)
		if got := e.events(t, model.EventNotifyFailed); len(got) != 1 || !strings.Contains(got[0], errPush.Error()) {
			t.Errorf("notify_failed events = %v, want one naming %q", got, errPush)
		}
		q, err := e.st.Question(context.Background(), id)
		if err != nil || q.Answer != "" || e.state(t) != model.TaskNeedsOwner {
			t.Errorf("question %v err %v state %s; want an open question and needs_owner", q, err, e.state(t))
		}
	})
	t.Run("OA-7_no_notifier_sends_nothing", func(t *testing.T) {
		e := newEnv(t)
		e.ask(t, model.QuestionTranscript, true)
		if got := e.events(t, model.EventNotified); len(got) != 0 {
			t.Errorf("notified events = %v, want none", got)
		}
	})
}
