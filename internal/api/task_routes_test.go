package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

var errRefused = errors.New("refused")

type fakeIntake struct {
	got []intake.TaskRequest
	err error
}

func (f *fakeIntake) Enqueue(_ context.Context, r intake.TaskRequest) (model.Task, error) {
	if f.err != nil {
		return model.Task{}, f.err
	}
	f.got = append(f.got, r)
	return model.Task{ID: "alpha-1", Project: r.Project, Title: r.Task, Shape: model.ShapePR, State: model.TaskQueued,
		OwnerTask: true, After: r.After}, nil
}

func startTaskAPI(t *testing.T, in Intake) (*store.Store, *client.Client) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clock.NewManual(t0))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	reg := NewRegistry()
	RegisterBuiltins(reg)
	h, err := New(Config{Source: st, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg,
		Projects: configured, Intake: in})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return st, client.New(srv.URL, ownerToken, nil)
}

func TestPostTasksQueuesTheRequest(t *testing.T) {
	in := &fakeIntake{}
	_, c := startTaskAPI(t, in)
	got, err := c.AddTask(context.Background(), wire.TaskRequest{Project: projAlpha, Task: "x", After: []string{"t0"}})
	if err != nil || got.ID != "alpha-1" || !got.OwnerTask {
		t.Fatalf("AddTask = %+v, %v", got, err)
	}
	if len(in.got) != 1 || in.got[0].Task != "x" || len(in.got[0].After) != 1 || in.got[0].After[0] != "t0" {
		t.Errorf("intake saw %+v", in.got)
	}
}

func TestPostTasksErrorsMapToStatus(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", intake.ErrInvalid, http.StatusBadRequest},
		{"unknown project", intake.ErrUnknownProject, http.StatusNotFound},
		{"exists", intake.ErrExists, http.StatusConflict},
		{"other", errRefused, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := startTaskAPI(t, &fakeIntake{err: tt.err})
			_, err := c.AddTask(context.Background(), wire.TaskRequest{Project: projAlpha, Task: "x"})
			var ce *client.Error
			if !errors.As(err, &ce) || ce.Status != tt.status {
				t.Errorf("err = %v; want status %d", err, tt.status)
			}
		})
	}
}

func TestPostTasksWithoutIntakeIs500(t *testing.T) {
	_, c := startTaskAPI(t, nil)
	_, err := c.AddTask(context.Background(), wire.TaskRequest{Project: projAlpha, Task: "x"})
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Status != http.StatusInternalServerError {
		t.Errorf("err = %v; want 500", err)
	}
}

func TestGetTasksListsOpenTasksOfAProject(t *testing.T) {
	st, c := startTaskAPI(t, nil)
	err := st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
		for _, tk := range []model.Task{
			{ID: "a1", Project: projAlpha, Title: "one", Shape: model.ShapePR},
			{ID: "b1", Project: projBeta, Title: "two", Shape: model.ShapePR},
		} {
			if err := tx.CreateTask(ctx, tk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := c.Tasks(context.Background(), "")
	if err != nil || len(all.Tasks) != 2 {
		t.Fatalf("Tasks(all) = %+v, %v; want 2", all, err)
	}
	one, err := c.Tasks(context.Background(), projAlpha)
	if err != nil || len(one.Tasks) != 1 || one.Tasks[0].ID != "a1" {
		t.Errorf("Tasks(alpha) = %+v, %v; want a1", one, err)
	}
}
