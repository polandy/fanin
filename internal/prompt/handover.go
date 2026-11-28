package prompt

import (
	"fmt"
	"strings"
)

// The texts of a handover section that has nothing recorded (spec: docs/dev/spec/handover.md).
const (
	NoState  = "No progress was reported."
	NoneOpen = "Nothing recorded."
	NoBranch = "Not recorded; find the run's branch with `git branch` and `git worktree list`."
	// NoWorktree is the worktree section of a run that had no prepared worktree.
	NoWorktree = "Not recorded; find the run's worktree with `git worktree list`."
	NoErrors   = "None recorded."
	NoTail     = "Not recorded."
)

// minFence is the shortest Markdown code fence.
const minFence = 3

// Handover is what a spent run leaves its follow-up run (concept C-BUDGET).
type Handover struct {
	// FromRun is the number of the spent run; Reason says why it ended.
	FromRun int
	Reason  string
	// State is the spent run's last progress or report.
	State  string
	Open   []string
	Branch string
	// Worktree is the prepared worktree the fresh run starts in.
	Worktree string
	// Errors are the latest failures, stalls and red verdicts of the task's earlier runs, oldest first.
	Errors []string
	Tail   string
}

// Markdown renders the handover in the fixed format of docs/dev/spec/handover.md.
func (h Handover) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Handover from run %d\n\n", h.FromRun)
	fmt.Fprintf(&b, "Run %d of this task ended: %s. You are its fresh run: carry on from this state on the same "+
		"branch and do not start over.\n\n", h.FromRun, h.Reason)
	fmt.Fprintf(&b, "### State\n\n%s\n\n", or(h.State, NoState))
	fmt.Fprintf(&b, "### Open\n\n%s\n\n", list(h.Open, NoneOpen))
	branch := NoBranch
	if h.Branch != "" {
		branch = "`" + h.Branch + "`"
	}
	fmt.Fprintf(&b, "### Branch\n\n%s\n\n", branch)
	tree := NoWorktree
	if h.Worktree != "" {
		tree = "`" + h.Worktree + "`"
	}
	fmt.Fprintf(&b, "### Worktree\n\n%s\n\n", tree)
	fmt.Fprintf(&b, "### Last errors\n\n%s\n\n", list(h.Errors, NoErrors))
	b.WriteString("### Tail\n\n")
	if tail := strings.TrimRight(h.Tail, "\n"); tail == "" {
		b.WriteString(NoTail + "\n")
	} else {
		f := fence(tail)
		fmt.Fprintf(&b, "%stext\n%s\n%s\n", f, tail, f)
	}
	return b.String()
}

func or(s, empty string) string {
	if s = strings.TrimSpace(s); s == "" {
		return empty
	}
	return s
}

func list(items []string, empty string) string {
	if len(items) == 0 {
		return "- " + empty
	}
	return "- " + strings.Join(items, "\n- ")
}

// fence is a backtick fence longer than any backtick run in text, so the tail cannot close it.
func fence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return strings.Repeat("`", max(minFence, longest+1))
}
