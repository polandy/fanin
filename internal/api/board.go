package api

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
)

// boardDoneShown is how many of the newest done tasks the Erledigt section lists.
const boardDoneShown = 4

// buildBoard derives the ST-2 sections of every project in snap.Projects from the snapshot's open tasks and the
// done tasks.
func buildBoard(ctx context.Context, src Source, snap wire.Snapshot, done []model.Task) ([]wire.BoardProject, error) {
	open := map[string]bool{}
	for _, t := range snap.Tasks {
		open[t.ID] = true
	}
	question := map[string]string{}
	for _, q := range snap.Questions {
		question[q.TaskID] = q.ID
	}
	byProject := make(map[string]*wire.BoardProject, len(snap.Projects))
	out := make([]wire.BoardProject, 0, len(snap.Projects))
	for _, p := range snap.Projects {
		out = append(out, wire.BoardProject{Project: p, Done: wire.BoardDone{Newest: []wire.BoardTask{}},
			Next: []wire.BoardTask{}, Waiting: []wire.BoardTask{}, Ready: []wire.BoardReady{}, Holds: []wire.Hold{}, Decisions: []wire.BoardTask{}})
	}
	for i := range out {
		byProject[out[i].Project] = &out[i]
	}
	// The batch of a project starts when its oldest open task was created; snap.Tasks is oldest first.
	start := map[string]wire.Task{}
	for _, t := range snap.Tasks {
		b := byProject[t.Project]
		if _, ok := start[t.Project]; !ok {
			start[t.Project] = t
		}
		bt := wire.BoardTask{ID: t.ID, Title: t.Title, State: t.State, Shape: t.Shape, UpdatedAt: t.UpdatedAt,
			QuestionID: question[t.ID]}
		ready, ok, err := readyPR(ctx, src, t)
		if err != nil {
			return nil, err
		}
		switch {
		case ok:
			b.Ready = append(b.Ready, ready)
		case t.State == string(model.TaskNeedsOwner) || t.State == string(model.TaskPaused):
			b.Waiting = append(b.Waiting, bt)
		case t.Shape == string(model.ShapeDecision):
			b.Decisions = append(b.Decisions, bt)
		case t.State == string(model.TaskQueued):
			bt.Blocked = slices.ContainsFunc(t.After, func(id string) bool { return open[id] })
			b.Next = append(b.Next, bt)
		}
	}
	// done is the least recently updated first; the newest few are listed newest first.
	for _, t := range slices.Backward(done) {
		b, ok := byProject[t.Project]
		first, running := start[t.Project]
		if !ok || !running || t.UpdatedAt.Before(first.CreatedAt) {
			continue
		}
		b.Done.Count++
		if len(b.Done.Newest) < boardDoneShown {
			b.Done.Newest = append(b.Done.Newest, wire.BoardTask{ID: string(t.ID), Title: t.Title,
				State: string(t.State), Shape: string(t.Shape), UpdatedAt: t.UpdatedAt})
		}
	}
	for _, h := range snap.Holds {
		if b, ok := byProject[h.Project]; ok {
			b.Holds = append(b.Holds, h)
		}
	}
	return out, nil
}

// readyPR reports whether task t is a gating task whose latest gate verdict is a ready verdict. A ready verdict
// without a PR number is an error, never a ready PR.
func readyPR(ctx context.Context, src Source, t wire.Task) (wire.BoardReady, bool, error) {
	if t.State != string(model.TaskGating) {
		return wire.BoardReady{}, false, nil
	}
	events, err := src.Events(ctx, model.TaskID(t.ID))
	if err != nil {
		return wire.BoardReady{}, false, fmt.Errorf("read events of task %q: %w", t.ID, err)
	}
	for _, e := range slices.Backward(events) {
		if e.Type != model.EventGateVerdict {
			continue
		}
		rest, ok := strings.CutPrefix(e.Detail, model.ReadyVerdictPrefix)
		if !ok {
			return wire.BoardReady{}, false, nil
		}
		ref, line, _ := strings.Cut(rest, ": ")
		pr, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
		if err != nil {
			return wire.BoardReady{}, false, fmt.Errorf("gate verdict %q of task %q has no PR number (want %q#<n>: <verdict>): %w",
				e.Detail, t.ID, model.ReadyVerdictPrefix, err)
		}
		return wire.BoardReady{ID: t.ID, Title: t.Title, PR: pr, Verdict: line, UpdatedAt: e.At}, true, nil
	}
	return wire.BoardReady{}, false, nil
}

// hostsDTO orders each host's workers: those waiting on the owner first, then managed before owner sessions,
// then by agent name. Hosts keep the fleet's order, and a host without workers lists none (never null).
func hostsDTO(hosts []wire.Host) []wire.Host {
	out := make([]wire.Host, 0, len(hosts))
	for _, h := range hosts {
		workers := slices.Clone(h.Workers)
		if workers == nil {
			workers = []wire.Worker{}
		}
		slices.SortStableFunc(workers, func(a, b wire.Worker) int {
			if a.WaitingOwner != b.WaitingOwner {
				if a.WaitingOwner {
					return -1
				}
				return 1
			}
			if a.Kind != b.Kind {
				if a.Kind == wire.WorkerManaged {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Agent, b.Agent)
		})
		h.Workers = workers
		out = append(out, h)
	}
	return out
}
