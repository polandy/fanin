package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/model"
)

// Layout of the overview (see SCENARIOS ST-2).
const (
	overviewIndent = "  "
	overviewEmpty  = "-"
)

func init() {
	register(Command{
		Name:    "overview",
		Summary: "print the board per project: done this run, next, waiting for owner, holds, decisions",
		Run:     runOverview,
	})
}

func runOverview(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if g.DaemonURL == "" {
		return cli.Usagef("overview needs the daemon URL; set --daemon-url or $%s", envURL)
	}
	snap, err := client.New(g.DaemonURL, g.Token, nil).Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("overview: read the snapshot from %s: %w", g.DaemonURL, err)
	}
	for _, p := range args {
		if !slices.Contains(snap.Projects, p) {
			return fmt.Errorf("overview: project %q is not in the config or the store; known: %s",
				p, strings.Join(snap.Projects, ", "))
		}
	}
	for _, b := range snap.Board {
		if len(args) == 0 || slices.Contains(args, b.Project) {
			out.Printf("%s", renderBoard(b))
		}
	}
	return nil
}

// renderBoard renders one project's board as text.
func renderBoard(b wire.BoardProject) string {
	var sb strings.Builder
	sb.WriteString(b.Project + "\n")
	section := func(title string, n int, lines []string) {
		fmt.Fprintf(&sb, "%s%s (%d):\n", overviewIndent, title, n)
		if len(lines) == 0 {
			lines = []string{overviewEmpty}
		}
		for _, l := range lines {
			sb.WriteString(overviewIndent + overviewIndent + l + "\n")
		}
	}
	section("done this run", b.Done.Count, taskLines(b.Done.Newest))
	section("next", len(b.Next), taskLines(b.Next))
	section("waiting for owner", len(b.Waiting), taskLines(b.Waiting))
	var holds []string
	for _, h := range b.Holds {
		line := fmt.Sprintf("%s hold %s: %s", h.Kind, h.ID, h.Reason)
		if h.FixTask != "" {
			line += " (fix " + h.FixTask + ")"
		}
		holds = append(holds, line+"; lift with: fanin hold lift "+h.ID)
	}
	section("holds", len(b.Holds), holds)
	section("decisions", len(b.Decisions), taskLines(b.Decisions))
	return sb.String()
}

func taskLines(tasks []wire.BoardTask) []string {
	var out []string
	for _, t := range tasks {
		line := t.ID + " " + t.Title
		var notes []string
		if t.Blocked {
			notes = append(notes, "blocked")
		}
		if t.State != "" && t.State != string(model.TaskDone) && t.State != string(model.TaskQueued) {
			notes = append(notes, t.State)
		}
		if t.QuestionID != "" {
			notes = append(notes, "question "+t.QuestionID)
		}
		if len(notes) > 0 {
			line += " [" + strings.Join(notes, ", ") + "]"
		}
		out = append(out, line)
	}
	return out
}
