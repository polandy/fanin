// Package prompt renders the worker prompts and nudges: embedded templates with {{INCLUDE:part}}, {{REPO}},
// {{BASE}}, {{HINT}}, plus a follow-up run's handover.
package prompt

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

//go:embed templates
var templates embed.FS

// Name is a template: worker[-oc][-gitea][-analyze].
type Name string

// The shipped templates.
const (
	Worker             Name = "worker"
	WorkerOC           Name = "worker-oc"
	WorkerGitea        Name = "worker-gitea"
	WorkerAnalyze      Name = "worker-analyze"
	WorkerGiteaAnalyze Name = "worker-gitea-analyze"
)

// The nudges the scheduler sends a live worker (SCENARIOS AW-10, AW-11).
const (
	NudgeContinue Name = "nudge-continue"
	NudgeSecure   Name = "nudge-secure"
	NudgeStall    Name = "nudge-stall"
	// NudgeRegate carries the reasons of a red re-gate under the merge lock (SCENARIOS MG-2).
	NudgeRegate Name = "nudge-regate"
	// NudgeCIFixed tells a worker that the base branch is green again (SCENARIOS MG-8).
	NudgeCIFixed Name = "nudge-ci-fixed"
	// NudgeFix carries the reasons of a red CI gate on the worker's PR (SCENARIOS GH-9).
	NudgeFix Name = "nudge-fix"
	// NudgeReview carries the reviewer's text of a red review (SCENARIOS GH-10, OC-8).
	NudgeReview Name = "nudge-review"
	// NudgeConflict asks the worker to merge the base into a conflicting PR (SCENARIOS CF-1).
	NudgeConflict Name = "nudge-conflict"
)

// Names lists every shipped worker template.
var Names = []Name{Worker, WorkerOC, WorkerGitea, WorkerAnalyze, WorkerGiteaAnalyze}

// Nudges lists every shipped nudge.
var Nudges = []Name{NudgeContinue, NudgeSecure, NudgeStall, NudgeRegate, NudgeCIFixed, NudgeFix, NudgeReview,
	NudgeConflict}

// ErrTemplate is a template or include part that is missing or unreadable.
var ErrTemplate = errors.New("prompt: template unavailable")

// ErrPlaceholder is a placeholder the renderer does not know.
var ErrPlaceholder = errors.New("prompt: unknown placeholder")

// Vars are the values a template takes.
type Vars struct {
	Repo string
	Base string
	Hint string
	// Handover, when set, is appended as it is: a follow-up run's handover document (Handover.Markdown).
	Handover string
}

const (
	templateDir = "templates"
	partsDir    = "parts"
	maxIncludes = 16
)

var (
	includeRe     = regexp.MustCompile(`\{\{INCLUDE:([a-z-]+)\}\}`)
	placeholderRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)
)

// Render fills the named embedded template.
func Render(name Name, v Vars) (string, error) {
	return render(templates, name, v)
}

func render(fsys fs.FS, name Name, v Vars) (string, error) {
	p, err := readText(fsys, templateDir+"/"+string(name)+".md")
	if err != nil {
		return "", err
	}
	for i := 0; ; i++ {
		m := includeRe.FindStringSubmatch(p)
		if m == nil {
			break
		}
		if i >= maxIncludes {
			return "", fmt.Errorf("%w: %s includes more than %d parts (a cycle?); remove the loop", ErrTemplate, name, maxIncludes)
		}
		part, err := readText(fsys, templateDir+"/"+partsDir+"/"+m[1]+".md")
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		p = strings.ReplaceAll(p, m[0], part)
	}
	if left := unknownPlaceholder(p); left != "" {
		return "", fmt.Errorf("%w: %s in %s; use REPO, BASE, HINT or INCLUDE:<part>", ErrPlaceholder, left, name)
	}
	// Placeholders are gone before the values go in, so a hint is never expanded.
	out := strings.NewReplacer("{{REPO}}", v.Repo, "{{BASE}}", v.Base, "{{HINT}}", v.Hint).Replace(p) + "\n"
	if v.Handover != "" {
		out += "\n" + strings.TrimRight(v.Handover, "\n") + "\n"
	}
	return out, nil
}

func unknownPlaceholder(p string) string {
	for _, m := range placeholderRe.FindAllString(p, -1) {
		switch m {
		case "{{REPO}}", "{{BASE}}", "{{HINT}}":
		default:
			return m
		}
	}
	return ""
}

// readText returns the file without its trailing newlines.
func readText(fsys fs.FS, path string) (string, error) {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w; ship the file under internal/prompt", ErrTemplate, path, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}
