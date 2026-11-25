package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

func paused(t *testing.T, s *Store, p string) bool {
	t.Helper()
	ok, err := s.Paused(context.Background(), p)
	if err != nil {
		t.Fatalf("Paused(%s): %v", p, err)
	}
	return ok
}

func TestPause(t *testing.T) {
	// see SCENARIOS FP-2, CH-3
	t.Run("FP-2_CH-3_pause_stops_the_next_worker_once_and_is_consumed", func(t *testing.T) {
		s, _ := newStore(t)
		var first, again, consumed, twice, other bool
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			var err error
			first, err = tx.RequestPause(ctx, project)
			return err
		})
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			var err error
			again, err = tx.RequestPause(ctx, project)
			return err
		})
		if !first || again || !paused(t, s, project) || paused(t, s, projectB) {
			t.Fatalf("RequestPause = %v then %v, paused %v/%v; want one flag on %s only", first, again,
				paused(t, s, project), paused(t, s, projectB), project)
		}
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			var err error
			if other, err = tx.ConsumePause(ctx, projectB); err != nil {
				return err
			}
			if consumed, err = tx.ConsumePause(ctx, project); err != nil {
				return err
			}
			twice, err = tx.ConsumePause(ctx, project)
			return err
		})
		if other || !consumed || twice || paused(t, s, project) {
			t.Errorf("ConsumePause other=%v first=%v second=%v paused=%v; want only the first to stop", other,
				consumed, twice, paused(t, s, project))
		}
		evs, err := s.EventsAfter(context.Background(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range evs {
			got = append(got, string(e.Type)+" "+e.Project+": "+e.Detail)
		}
		want := []string{"project_paused proj-a: pause requested", "pause_consumed proj-a: " + pauseConsumedDetail}
		if d := cmp.Diff(want, got); d != "" {
			t.Errorf("events (-want +got):\n%s", d)
		}
	})
	t.Run("resume_clears_an_unconsumed_pause", func(t *testing.T) {
		s, _ := newStore(t)
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			if _, err := tx.RequestPause(ctx, project); err != nil {
				return err
			}
			return tx.Resume(ctx, project)
		})
		var consumed bool
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			var err error
			consumed, err = tx.ConsumePause(ctx, project)
			return err
		})
		if paused(t, s, project) || consumed {
			t.Errorf("after resume: paused=%v consumed=%v, want neither", paused(t, s, project), consumed)
		}
		evs, err := s.EventsAfter(context.Background(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(evs); n != 2 || evs[1].Type != model.EventProjectResumed {
			t.Errorf("events %+v, want project_paused then project_resumed", evs)
		}
	})
	t.Run("no_project", func(t *testing.T) {
		s, _ := newStore(t)
		for name, fn := range map[string]func(ctx context.Context, tx *Tx) error{
			"pause": func(ctx context.Context, tx *Tx) error {
				_, err := tx.RequestPause(ctx, "")
				return err
			},
			"resume": func(ctx context.Context, tx *Tx) error { return tx.Resume(ctx, "") },
		} {
			if err := s.Transition(context.Background(), fn); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s without a project = %v, want ErrInvalid", name, err)
			}
		}
	})
	t.Run("closed_store", func(t *testing.T) {
		s, _ := newStore(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Paused(context.Background(), project); err == nil {
			t.Error("Paused on a closed store = nil, want an error")
		}
	})
}
