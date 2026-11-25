package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/model"
)

// Status words of a project line (see SCENARIOS ST-1 and docs/dev/spec/daemon.md).
const (
	loopActive  = "active"
	loopIdle    = "idle"
	mergeFree   = "free"
	statusNone  = "-"
	fieldSpacer = "  "

	openReady      = "ready"
	openBlocked    = "blocked"
	openNeedsOwner = "needs-owner"
)

func init() {
	register(Command{
		Name:    "status",
		Summary: "print one line per project: loop, worker, merge lock, last event, open work",
		Run:     runStatus,
	})
}

func runStatus(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if g.DaemonURL == "" {
		return cli.Usagef("status needs the daemon URL; set --daemon-url or $%s", envURL)
	}
	snap, err := client.New(g.DaemonURL, g.Token, nil).Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("status: read the snapshot from %s: %w", g.DaemonURL, err)
	}
	projects := snap.Projects
	if len(args) > 0 {
		for _, p := range args {
			if !slices.Contains(snap.Projects, p) {
				return fmt.Errorf("status: project %q is not in the config or the store; known: %s",
					p, strings.Join(snap.Projects, ", "))
			}
		}
		projects = args
	}
	for _, p := range projects {
		line, err := statusLine(snap, p)
		if err != nil {
			return err
		}
		out.Println(line)
	}
	return nil
}

// statusLine renders one project; an unknown state in the snapshot is an error, never a guess.
func statusLine(snap wire.Snapshot, project string) (string, error) {
	var (
		active                                bool
		ready, blocked, paused, owner, failed int
	)
	open := map[string]bool{}
	tasks := map[string]bool{}
	for _, t := range snap.Tasks {
		open[t.ID] = true
		if t.Project == project {
			tasks[t.ID] = true
		}
	}
	for _, t := range snap.Tasks {
		if t.Project != project {
			continue
		}
		state, err := model.ParseTaskState(t.State)
		if err != nil {
			return "", fmt.Errorf("status: task %q: %w", t.ID, err)
		}
		switch state {
		case model.TaskRunning, model.TaskGating, model.TaskPostMerge:
			active = true
		case model.TaskQueued:
			if slices.ContainsFunc(t.After, func(id string) bool { return open[id] }) {
				blocked++
			} else {
				ready++
			}
		case model.TaskPaused:
			paused++
		case model.TaskNeedsOwner:
			owner++
		case model.TaskFailed:
			failed++
		}
	}
	workers := 0
	for _, r := range snap.Runs {
		state, err := model.ParseRunState(r.State)
		if err != nil {
			return "", fmt.Errorf("status: run %q: %w", r.ID, err)
		}
		if tasks[r.TaskID] && !state.Terminal() {
			workers++
		}
	}
	loop := loopIdle
	if active {
		loop = loopActive
	}
	items := counts([]string{openReady, openBlocked, string(model.TaskPaused), openNeedsOwner,
		string(model.TaskFailed)}, []int{ready, blocked, paused, owner, failed})
	for _, h := range snap.Holds {
		if h.Project != project {
			continue
		}
		item := h.Kind + " hold"
		if h.FixTask != "" {
			item += " (fix " + h.FixTask + ")"
		}
		items = append(items, item)
	}
	fields := []string{
		project,
		"loop=" + loop,
		fmt.Sprintf("worker=%d", workers),
		"merge=" + mergeField(snap.Locks, project),
		"last: " + lastField(snap.Latest, project),
		"open: " + orNone(strings.Join(items, ", ")),
	}
	return strings.Join(fields, fieldSpacer), nil
}

func counts(names []string, ns []int) []string {
	var out []string
	for i, n := range ns {
		if n > 0 {
			out = append(out, fmt.Sprintf("%s %d", names[i], n))
		}
	}
	return out
}

func mergeField(locks []wire.Lock, project string) string {
	for _, l := range locks {
		if l.Project != project {
			continue
		}
		if l.Stale {
			return "stale (" + l.Holder + ")"
		}
		return "held by " + l.Holder
	}
	return mergeFree
}

func lastField(latest []wire.Event, project string) string {
	for _, ev := range latest {
		if ev.Project != project {
			continue
		}
		what := ev.Type
		if ev.TaskID != "" {
			what += " " + ev.TaskID
		}
		return ev.At.Format(time.RFC3339) + " " + what + ": " + ev.Detail
	}
	return statusNone
}

func orNone(s string) string {
	if s == "" {
		return statusNone
	}
	return s
}
