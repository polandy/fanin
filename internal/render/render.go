// Package render writes a project's Markdown status log from the store. The Markdown is a window onto the store
// and is never read back (see SCENARIOS RS-1, RS-2, docs/dev/spec/render.md).
package render

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/atomicfile"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// DecisionWindow is how far back owner decisions are shown.
const DecisionWindow = 7 * 24 * time.Hour

const (
	fileMode   = 0o644
	timeLayout = time.RFC3339
	none       = "- none\n"
)

// Renderer renders projects from a store, taking the window's end from its clock.
type Renderer struct {
	store *store.Store
	clock clock.Clock
}

// New returns a Renderer reading st and dating the window with clk.
func New(st *store.Store, clk clock.Clock) *Renderer { return &Renderer{store: st, clock: clk} }

// Render returns the Markdown window of one project.
func (r *Renderer) Render(ctx context.Context, project string) ([]byte, error) {
	now := r.clock.Now().UTC()
	tasks, err := r.store.ProjectTasks(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("render: project %q: %w", project, err)
	}
	questions, err := r.store.ProjectQuestions(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("render: project %q: %w", project, err)
	}
	holds, err := r.store.Holds(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("render: project %q: %w", project, err)
	}
	run, runTask, err := r.currentRun(ctx, tasks)
	if err != nil {
		return nil, fmt.Errorf("render: project %q: %w", project, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# fanin: %s\n\nRendered %s from the store; older entries are in the store.\n\n", project,
		now.Format(timeLayout))
	fmt.Fprintf(&b, "%d tasks, %d holds\n", len(tasks), len(holds))
	for _, h := range holds {
		fmt.Fprintf(&b, "- hold %s (%s) since %s: %s\n", h.ID, h.Kind, h.CreatedAt.UTC().Format(timeLayout), h.Reason)
	}
	section(&b, "Ready", taskLines(filterTasks(tasks, model.TaskGating, model.TaskPostMerge)))
	section(&b, "Blocked", taskLines(newestOf(tasks, model.TaskNeedsOwner, model.TaskPaused)))
	section(&b, "Queued", taskLines(filterTasks(tasks, model.TaskQueued)))
	section(&b, "Owner decisions (last 7 days)", decisionLines(questions, now.Add(-DecisionWindow)))
	section(&b, "Current run", runLines(run, runTask))
	return []byte(b.String()), nil
}

// Write renders the project and replaces the file at path atomically; a failure leaves the old file as it was.
func (r *Renderer) Write(ctx context.Context, project, path string) error {
	md, err := r.Render(ctx, project)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, md, fileMode); err != nil {
		return fmt.Errorf("render: project %q: %w", project, err)
	}
	return nil
}

func section(b *strings.Builder, title string, lines []string) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	if len(lines) == 0 {
		b.WriteString(none)
		return
	}
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
}

func filterTasks(tasks []model.Task, states ...model.TaskState) []model.Task {
	return slices.DeleteFunc(slices.Clone(tasks), func(t model.Task) bool { return !slices.Contains(states, t.State) })
}

// newestOf returns the most recently updated task in one of states, if any.
func newestOf(tasks []model.Task, states ...model.TaskState) []model.Task {
	got := filterTasks(tasks, states...)
	if len(got) == 0 {
		return nil
	}
	return []model.Task{slices.MaxFunc(got, func(a, b model.Task) int {
		return cmp.Or(a.UpdatedAt.Compare(b.UpdatedAt), cmp.Compare(a.ID, b.ID))
	})}
}

func taskLines(tasks []model.Task) []string {
	var out []string
	for _, t := range tasks {
		out = append(out, fmt.Sprintf("`%s` %s [%s] updated %s", t.ID, t.Title, t.State,
			t.UpdatedAt.UTC().Format(timeLayout)))
	}
	return out
}

func decisionLines(qs []model.OwnerQuestion, since time.Time) []string {
	var out []string
	for _, q := range qs {
		if q.AskedAt.Before(since) {
			continue
		}
		answer := "open"
		if !q.AnsweredAt.IsZero() {
			answer = "answered: " + q.Answer
		}
		out = append(out, fmt.Sprintf("%s `%s` %s (%s)", q.AskedAt.UTC().Format(timeLayout), q.TaskID, q.Question,
			answer))
	}
	return out
}

// currentRun returns the newest unfinished run of the project's open tasks.
func (r *Renderer) currentRun(ctx context.Context, tasks []model.Task) (*model.Run, model.TaskID, error) {
	var (
		best *model.Run
		of   model.TaskID
	)
	for _, t := range tasks {
		if t.State.Terminal() {
			continue
		}
		runs, err := r.store.Runs(ctx, t.ID)
		if err != nil {
			return nil, "", err
		}
		for _, run := range runs {
			if run.State.Terminal() || (best != nil && !run.StartedAt.After(best.StartedAt)) {
				continue
			}
			best, of = &run, t.ID
		}
	}
	return best, of, nil
}

func runLines(run *model.Run, task model.TaskID) []string {
	if run == nil {
		return nil
	}
	b := run.Budget
	return []string{fmt.Sprintf("`%s` of `%s` run %d [%s] slot %d branch %s started %s; fix rounds %d, tokens %d",
		run.ID, task, run.Number, run.State, run.Slot, run.Branch, run.StartedAt.UTC().Format(timeLayout),
		b.FixRounds, b.TotalTokens)}
}
