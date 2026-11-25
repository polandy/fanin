// Package prompt renders the worker prompts: embedded templates with {{INCLUDE:part}}, {{REPO}}, {{BASE}}, {{HINT}}.
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

// Names lists every shipped template.
var Names = []Name{Worker, WorkerOC, WorkerGitea, WorkerAnalyze, WorkerGiteaAnalyze}

// ErrTemplate is a template or include part that is missing or unreadable.
var ErrTemplate = errors.New("prompt: template unavailable")

// ErrPlaceholder is a placeholder the renderer does not know.
var ErrPlaceholder = errors.New("prompt: unknown placeholder")

// Vars are the values a template takes.
type Vars struct {
	Repo string
	Base string
	Hint string
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
	return strings.NewReplacer("{{REPO}}", v.Repo, "{{BASE}}", v.Base, "{{HINT}}", v.Hint).Replace(p) + "\n", nil
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
