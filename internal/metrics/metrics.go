// Package metrics serves the Prometheus text format (concept: observability). Every number is counted from the
// store at scrape time, so a restart loses nothing and a transition raises its counter in the same commit.
package metrics

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// ContentType is the Prometheus text exposition format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Adapter names label fanin_adapter_errors_total.
const (
	AdapterHook   = "hook"
	AdapterNotify = "notify"
)

// Source is what a scrape reads: the store, or a fake in tests.
type Source interface {
	TaskCounts(ctx context.Context) ([]store.Count, error)
	RunCounts(ctx context.Context) ([]store.Count, error)
	EventCounts(ctx context.Context) ([]store.Count, error)
}

// counters are the event-derived counters: metric name, help, and the event type it counts.
var counters = []struct {
	name, help string
	event      model.EventType
	adapter    string
}{
	{"fanin_runs_started_total", "Runs started.", model.EventRunStarted, ""},
	{"fanin_nudges_total", "Nudges sent to workers.", model.EventWorkerNudged, ""},
	{"fanin_merges_total", "PRs merged.", model.EventMerged, ""},
	{"fanin_adapter_errors_total", "Failed calls of an adapter.", model.EventHookFailed, AdapterHook},
	{"fanin_adapter_errors_total", "Failed calls of an adapter.", model.EventNotifyFailed, AdapterNotify},
}

// Handler serves GET /metrics. A store that cannot be read is a 500 with the cause, never partial numbers.
func Handler(src Source, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := Write(r.Context(), &buf, src); err != nil {
			log.ErrorContext(r.Context(), "metrics: scrape failed", "err", err)
			http.Error(w, "metrics: the store could not be read: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		_, _ = w.Write(buf.Bytes())
	})
}

// Write renders every metric to w.
func Write(ctx context.Context, w *bytes.Buffer, src Source) error {
	tasks, err := src.TaskCounts(ctx)
	if err != nil {
		return fmt.Errorf("metrics: tasks: %w", err)
	}
	runs, err := src.RunCounts(ctx)
	if err != nil {
		return fmt.Errorf("metrics: runs: %w", err)
	}
	events, err := src.EventCounts(ctx)
	if err != nil {
		return fmt.Errorf("metrics: events: %w", err)
	}
	projects := projectsOf(tasks, runs, events)
	var states []string
	for _, s := range model.AllTaskStates {
		states = append(states, string(s))
	}
	gauge(w, "fanin_tasks", "Tasks per project and state.", projects, states, tasks)
	states = states[:0]
	for _, s := range model.AllRunStates {
		states = append(states, string(s))
	}
	gauge(w, "fanin_runs", "Runs per project and state.", projects, states, runs)
	seen := map[string]bool{}
	for _, c := range counters {
		if !seen[c.name] {
			seen[c.name] = true
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
		}
		for _, p := range projects {
			label := fmt.Sprintf("project=%q", p)
			if c.adapter != "" {
				label = fmt.Sprintf("adapter=%q,%s", c.adapter, label)
			}
			fmt.Fprintf(w, "%s{%s} %d\n", c.name, label, lookup(events, p, string(c.event)))
		}
	}
	return nil
}

func gauge(w *bytes.Buffer, name, help string, projects, states []string, counts []store.Count) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	for _, p := range projects {
		for _, s := range states {
			fmt.Fprintf(w, "%s{project=%q,state=%q} %d\n", name, p, s, lookup(counts, p, s))
		}
	}
}

func lookup(counts []store.Count, project, key string) int {
	for _, c := range counts {
		if c.Project == project && c.Key == key {
			return c.N
		}
	}
	return 0
}

// projectsOf lists every project any count names, sorted; with none it lists the empty project so the series exist.
func projectsOf(groups ...[]store.Count) []string {
	set := map[string]bool{}
	for _, g := range groups {
		for _, c := range g {
			set[c.Project] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}
