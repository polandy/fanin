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
	locks, err := src.Locks(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read locks: %w", err)
	}
	latest, err := src.LatestEvents(ctx)
	if err != nil {
		return wire.Snapshot{}, fmt.Errorf("read latest events: %w", err)
	}
	snap := wire.Snapshot{
		Tasks: []wire.Task{}, Runs: []wire.Run{}, Questions: []wire.Question{}, Holds: []wire.Hold{},
		Locks: []wire.Lock{}, Latest: []wire.Event{}, LastSeq: seq,
	}
	projects := map[string]bool{}
	// live are the holders a lock may name and still be held: open tasks and their unfinished runs.
	live := map[string]bool{}
	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, taskDTO(t))
		projects[t.Project] = true
		live[string(t.ID)] = true
		runs, err := src.Runs(ctx, t.ID)
		if err != nil {
			return wire.Snapshot{}, fmt.Errorf("read runs of task %q: %w", t.ID, err)
		}
		for _, run := range runs {
			snap.Runs = append(snap.Runs, runDTO(run))
			if !run.State.Terminal() {
				live[string(run.ID)] = true
			}
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
	now := s.Clock().Now()
	for _, l := range locks {
		snap.Locks = append(snap.Locks, wire.Lock{Project: l.Project, Holder: l.Holder, AcquiredAt: l.AcquiredAt,
			ExpiresAt: l.ExpiresAt, Stale: l.Expired(now) || !live[l.Holder]})
		projects[l.Project] = true
	}
	for _, ev := range latest {
		snap.Latest = append(snap.Latest, eventDTO(ev))
		projects[ev.Project] = true
	}
	snap.Projects = projectOrder(s.Projects(), projects)
	return snap, nil
}

// projectOrder lists the configured projects in order, then every other project in seen, sorted.
func projectOrder(configured []string, seen map[string]bool) []string {
	out := make([]string, 0, len(configured)+len(seen))
	known := make(map[string]bool, len(configured))
	for _, p := range configured {
		out = append(out, p)
		known[p] = true
	}
	var rest []string
	for p := range seen {
		if !known[p] {
			rest = append(rest, p)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
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
			Issue: r.Result.Issue, IssueURL: r.Result.IssueURL, Text: r.Result.Text}
	}
	return out
}

func questionDTO(q model.OwnerQuestion) wire.Question {
	opts := make([]wire.Option, 0, len(q.Options))
	for _, o := range q.Options {
		opts = append(opts, wire.Option{Label: o.Label, Description: o.Description})
	}
	return wire.Question{ID: q.ID, TaskID: string(q.TaskID), RunID: string(q.RunID), Project: q.Project,
		Kind: string(q.Kind), AllowText: q.AllowText, Question: q.Question, Options: opts, Recommended: q.Recommended, AskedAt: q.AskedAt}
}

func holdDTO(h model.Hold) wire.Hold {
	return wire.Hold{ID: h.ID, Project: h.Project, Kind: string(h.Kind), Reason: h.Reason, FixTask: string(h.FixTask),
		CreatedAt: h.CreatedAt}
}

func eventDTO(ev model.Event) wire.Event {
	return wire.Event{Seq: ev.Seq, Type: string(ev.Type), Project: ev.Project, TaskID: string(ev.TaskID),
		RunID: string(ev.RunID), At: ev.At, Detail: ev.Detail}
}
