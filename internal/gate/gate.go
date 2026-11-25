// Package gate reads a PR from its forge and answers GREEN, PENDING or RED with reasons, or an error. It decides
// nudges, readies and waits, never the merge: the forge's branch protection merges (concept D4). An unreadable
// answer is an error, never a verdict (see SCENARIOS FP-6).
package gate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// The gate sentinels; match them with errors.Is.
var (
	// ErrUnreadable is a PR, file list or review the gate cannot read; it is never a verdict.
	ErrUnreadable = errors.New("gate: unreadable")
	// ErrUnreachable ends a gate whose reads kept failing; nobody is nudged (see SCENARIOS GH-6).
	ErrUnreachable = errors.New("gate unreachable")
)

// Verdict is the gate's answer for one PR head.
type Verdict string

// The verdicts; GREEN docs, GREEN code and NOCI are Gitea's (see SCENARIOS GT-3, GT-4).
const (
	Green     Verdict = "GREEN"
	GreenDocs Verdict = "GREEN docs"
	GreenCode Verdict = "GREEN code"
	NoCI      Verdict = "NOCI"
	Pending   Verdict = "PENDING"
	Red       Verdict = "RED"
)

// AllVerdicts lists every Verdict.
var AllVerdicts = []Verdict{Green, GreenDocs, GreenCode, NoCI, Pending, Red}

// IsGreen reports whether v lets the PR merge.
func (v Verdict) IsGreen() bool { return v == Green || v == GreenDocs || v == GreenCode }

// Wait says why a gate is PENDING; the wait policy treats Gitea's WaitNoChecks apart (see SCENARIOS GT-4).
type Wait string

// The pending reasons.
const (
	WaitChecks           Wait = "checks running"
	WaitNoChecks         Wait = "no checks"
	WaitMergeable        Wait = "mergeable UNKNOWN"
	WaitChecksUnreadable Wait = "statuses unreadable"
	WaitFilesUnreadable  Wait = "files unreadable"
)

// ReviewContext is the commit status the review run posts on the PR head (concept D5).
const ReviewContext = "fanin/review"

// Result is one gate answer.
type Result struct {
	Verdict Verdict
	// Title is the PR title, set for a green verdict.
	Title string
	// Wait is set for PENDING only.
	Wait    Wait
	Reasons []string
}

// String renders r as the loop shows it: "GREEN <title>", "PENDING: checks running", "RED: a; b".
func (r Result) String() string {
	if r.Verdict.IsGreen() {
		return strings.TrimSpace(string(r.Verdict) + " " + r.Title)
	}
	if len(r.Reasons) == 0 {
		return string(r.Verdict)
	}
	return string(r.Verdict) + ": " + strings.Join(r.Reasons, "; ")
}

// Reader is what the gate reads from a forge.
type Reader interface {
	PR(ctx context.Context, repo string, number int) (forge.PR, error)
	Files(ctx context.Context, repo string, number int) ([]string, error)
}

// Target is the PR one gate reads.
type Target struct {
	Kind   forge.Kind
	Repo   string
	Number int
	// Base is the branch the PR must aim at; Gitea refuses any other (see SCENARIOS GT-5).
	Base string
}

// Read reads t once and returns its verdict; a read that fails is an error wrapping ErrUnreadable.
func Read(ctx context.Context, r Reader, t Target) (Result, error) {
	if t.Repo == "" || t.Number <= 0 {
		return Result{}, fmt.Errorf("%w: gate of repo %q PR %d; pass the repo and a PR number above 0",
			forge.ErrInvalidRequest, t.Repo, t.Number)
	}
	switch t.Kind {
	case forge.KindGitHub:
		pr, err := r.PR(ctx, t.Repo, t.Number)
		if err != nil {
			return Result{}, fmt.Errorf("%w: PR %s#%d: %w", ErrUnreadable, t.Repo, t.Number, err)
		}
		return GitHub(pr), nil
	case forge.KindGitea:
		return readGitea(ctx, r, t)
	default:
		return Result{}, fmt.Errorf("%w: forge kind %q of %s; use one of %v", forge.ErrInvalidRequest, t.Kind,
			t.Repo, forge.AllKinds)
	}
}

// DocsOnly reports whether files is non-empty and every path is under docs/ or ends in .md, where any CLAUDE.md
// and anything under .claude/ count as code (see SCENARIOS GH-12).
func DocsOnly(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if path.Base(f) == "CLAUDE.md" || f == ".claude" || strings.HasPrefix(f, ".claude/") ||
			strings.Contains(f, "/.claude/") {
			return false
		}
		if !strings.HasPrefix(f, "docs/") && !strings.HasSuffix(f, ".md") {
			return false
		}
	}
	return true
}

// failed reports whether a CI check counts against the PR; an unknown state counts as failed (fail closed).
func failed(s forge.CheckState) bool {
	return s != forge.CheckSuccess && s != forge.CheckSkipped && s != forge.CheckPending
}

// sortChecks splits checks into failed and running names, in forge order.
func sortChecks(checks []forge.Check) (bad, running []string) {
	for _, c := range checks {
		switch {
		case c.State == forge.CheckPending:
			running = append(running, c.Name)
		case failed(c.State):
			bad = append(bad, c.Name+" "+string(c.State))
		}
	}
	return bad, running
}

func red(reasons ...string) Result         { return Result{Verdict: Red, Reasons: reasons} }
func pending(w Wait) Result                { return Result{Verdict: Pending, Wait: w, Reasons: []string{string(w)}} }
func green(v Verdict, title string) Result { return Result{Verdict: v, Title: title} }
