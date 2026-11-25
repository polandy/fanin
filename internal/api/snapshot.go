package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
)

func init() { RegisterBuiltin(registerSnapshot) }

func registerSnapshot(r *Registry) {
	r.Add(Route{Pattern: "GET /api/v1/snapshot", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			snap, err := buildSnapshot(s, r)
			if err != nil {
				s.Logger().ErrorContext(r.Context(), "api: snapshot", "err", err)
				WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
					"the store could not be read: "+err.Error())
				return
			}
			WriteJSON(w, http.StatusOK, snap)
		})
	}})
}

func buildSnapshot(s *Server, r *http.Request) (wire.Snapshot, error) {
	ctx, src := r.Context(), s.Source()
	// The cursor first: an event committed while we read is then replayed by the stream, never lost.
	seq, err := src.LastEventSeq(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read last event: %w", err)
	}
	tasks, err := src.OpenTasks(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read tasks: %w", err)
	}
	questions, err := src.OpenQuestions(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read questions: %w", err)
	}
	holds, err := src.AllHolds(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read holds: %w", err)
	}
	snap := wire.Snapshot{
		Tasks: []wire.Task{}, Runs: []wire.Run{}, Questions: []wire.Question{}, Holds: []wire.Hold{}, LastSeq: seq,
	}
	projects := map[string]bool{}
	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, taskDTO(t))
		projects[t.Project] = true
		runs, err := src.Runs(ctx, t.ID)
		if err != nil {
			return wire.Snapshot{}, fmt.Errorf("read runs of task %q: %w", t.ID, err)
		}
		for _, run := range runs {
			snap.Runs = append(snap.Runs, runDTO(run))
		}
	}
	for _, q := range questions {
		snap.Questions = append(snap.Questions, questionDTO(q))
		projects[q.Project] = true
	}
	for _, h := range holds {
		snap.Holds = append(snap.Holds, holdDTO(h))
		projects[h.Project] = true
	}
	snap.Projects = make([]string, 0, len(projects))
	for p := range projects {
		snap.Projects = append(snap.Projects, p)
	}
	slices.Sort(snap.Projects)
	return snap, nil
}

func taskDTO(t model.Task) wire.Task {
	after := make([]string, 0, len(t.After))
	for _, id := range t.After {
		after = append(after, string(id))
	}
	return wire.Task{
		ID: string(t.ID), Project: t.Project, Title: t.Title, Prompt: t.Prompt, Shape: string(t.Shape),
		State: string(t.State), OwnerTask: t.OwnerTask, After: after, Model: t.Model(),
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func runDTO(r model.Run) wire.Run {
	out := wire.Run{
		ID: string(r.ID), TaskID: string(r.TaskID), Number: r.Number, Slot: r.Slot, Branch: r.Branch,
		State: string(r.State), StartedAt: r.StartedAt, EndedAt: r.EndedAt,
		Budget: wire.Budget{
			FixRounds: r.Budget.FixRounds, ReviewNudges: r.Budget.ReviewNudges, Stalls: r.Budget.Stalls,
			InputTokens: r.Budget.InputTokens, OutputTokens: r.Budget.OutputTokens, TotalTokens: r.Budget.TotalTokens,
		},
	}
	if r.Result != nil {
		out.Result = &wire.Result{Kind: string(r.Result.Kind), PR: r.Result.PR, Branch: r.Result.Branch,
			Issue: r.Result.Issue, Text: r.Result.Text}
	}
	return out
}

func questionDTO(q model.OwnerQuestion) wire.Question {
	opts := make([]wire.Option, 0, len(q.Options))
	for _, o := range q.Options {
		opts = append(opts, wire.Option{Label: o.Label, Description: o.Description})
	}
	return wire.Question{ID: q.ID, TaskID: string(q.TaskID), RunID: string(q.RunID), Project: q.Project,
		Question: q.Question, Options: opts, Recommended: q.Recommended, AskedAt: q.AskedAt}
}

func holdDTO(h model.Hold) wire.Hold {
	return wire.Hold{ID: h.ID, Project: h.Project, Kind: string(h.Kind), Reason: h.Reason, FixTask: string(h.FixTask),
		CreatedAt: h.CreatedAt}
}

func eventDTO(ev model.Event) wire.Event {
	return wire.Event{Seq: ev.Seq, Type: string(ev.Type), Project: ev.Project, TaskID: string(ev.TaskID),
		RunID: string(ev.RunID), At: ev.At, Detail: ev.Detail}
}
