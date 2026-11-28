// Package worktree names and creates the git worktree and branch each worker run works in (SCENARIOS SP-12).
// internal/cleanup removes them after the merge.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/runtime"
)

// The git program, the remote it works against and the porcelain keys of `git worktree list`.
const (
	gitProgram  = "git"
	remote      = "origin"
	headsRef    = "refs/heads/"
	keyWorktree = "worktree "
	keyBranch   = "branch "
	// showRefMissing is the exit code of `git show-ref --verify --quiet` for a ref that does not exist.
	showRefMissing = 1
)

// The branch types a hint may lead with; any other text makes a TypeDefault branch.
const (
	TypeDefault  = "feat"
	TypeAnalysis = "docs"
	// AnalysisWord leads the slug of an analysis branch.
	AnalysisWord = "analysis"
)

// Types are the Conventional Commit types a branch may take from its hint.
var Types = []string{"feat", "fix", "docs", "chore", "refactor", "test", "ci"}

// How much of the hint goes into a slug.
const (
	maxWords    = 4
	maxWordsLen = 40
)

var (
	// ErrInUse is a worktree path that has another branch checked out, or a branch checked out at another path.
	ErrInUse = errors.New("worktree: in use")
	// ErrBranchExists is a run's branch that exists locally without a worktree.
	ErrBranchExists = errors.New("worktree: branch exists")
	// ErrGit is a git command that failed or could not run.
	ErrGit = errors.New("worktree: git failed")
)

var (
	typeRe    = regexp.MustCompile(`^\s*([a-z]+)(\([^)]*\))?!?:\s*`)
	nonWordRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// Tree is a run's worktree and the branch checked out there.
type Tree struct {
	Path   string
	Branch string
}

// For names the worktree of a task: <dir>/<repo>-<slug> on <type>/<slug>, where dir is the project's worktree_dir
// or the checkout's parent, type comes from the hint's Conventional Commit prefix and slug is the task number
// plus the hint's first words. The task ID makes it unique within the project.
func For(p config.Project, task string, analysis bool, text string) Tree {
	typ := TypeDefault
	if m := typeRe.FindStringSubmatch(text); m != nil && slices.Contains(Types, m[1]) {
		typ, text = m[1], text[len(m[0]):]
	}
	words := strings.Fields(nonWordRe.ReplaceAllString(strings.ToLower(text), " "))
	if analysis {
		typ, words = TypeAnalysis, append([]string{AnalysisWord}, words...)
	}
	slug := sanitize(strings.TrimPrefix(task, p.Name+"-"))
	if w := cut(words[:min(len(words), maxWords)]); w != "" {
		slug += "-" + w
	}
	dir := p.WorktreeDir
	if dir == "" {
		dir = filepath.Dir(p.Path)
	}
	return Tree{Path: filepath.Join(dir, filepath.Base(p.Path)+"-"+slug), Branch: typ + "/" + slug}
}

func sanitize(s string) string {
	return strings.Join(strings.Fields(nonWordRe.ReplaceAllString(strings.ToLower(s), " ")), "-")
}

// cut joins words with dashes, dropping trailing words beyond maxWordsLen; the first word always stays.
func cut(words []string) string {
	out := ""
	for i, w := range words {
		if i > 0 && len(out)+1+len(w) > maxWordsLen {
			break
		}
		if i > 0 {
			out += "-"
		}
		out += w
	}
	return out
}

// Maker creates worktrees through a runner, one project at a time.
type Maker struct {
	r     runtime.Runner
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns a Maker that runs git through r.
func New(r runtime.Runner) *Maker { return &Maker{r: r, locks: map[string]*sync.Mutex{}} }

// Prepare makes t ready in p's checkout: a worktree that already has t.Branch at t.Path is reused (a follow-up
// run); else origin/<base> is fetched and `git worktree add -b` creates both. A path or branch taken by another
// worktree is ErrInUse, a local branch without a worktree ErrBranchExists, any git failure ErrGit.
func (m *Maker) Prepare(ctx context.Context, p config.Project, t Tree) error {
	lock := m.lock(p.Path)
	lock.Lock()
	defer lock.Unlock()
	res, err := m.git(ctx, p, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	at, of := lookup(string(res.Stdout), t)
	switch {
	case at == t.Branch:
		return nil
	case at != "":
		return fmt.Errorf("%w: project %s: %s has branch %s checked out, not %s; remove that worktree with "+
			"`git worktree remove %s`", ErrInUse, p.Name, t.Path, at, t.Branch, t.Path)
	case of != "":
		return fmt.Errorf("%w: project %s: branch %s is checked out in %s, not %s; remove that worktree with "+
			"`git worktree remove %s`", ErrInUse, p.Name, t.Branch, of, t.Path, of)
	}
	_, err = m.git(ctx, p, "show-ref", "--verify", "--quiet", headsRef+t.Branch)
	var exit *runtime.ExitError
	switch {
	case err == nil:
		return fmt.Errorf("%w: project %s: branch %s exists without a worktree; check it out at %s with "+
			"`git worktree add %s %s` or delete it with `git branch -D %s`", ErrBranchExists, p.Name, t.Branch, t.Path,
			t.Path, t.Branch, t.Branch)
	case !errors.As(err, &exit) || exit.Code != showRefMissing:
		return err
	}
	if _, err := m.git(ctx, p, "fetch", "--quiet", remote, p.Base); err != nil {
		return err
	}
	_, err = m.git(ctx, p, "worktree", "add", "-b", t.Branch, t.Path, remote+"/"+p.Base)
	return err
}

func (m *Maker) lock(path string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[path]
	if !ok {
		l = &sync.Mutex{}
		m.locks[path] = l
	}
	return l
}

// git runs git in p's checkout; a failure wraps ErrGit and names the command.
func (m *Maker) git(ctx context.Context, p config.Project, args ...string) (runtime.Result, error) {
	argv := append([]string{gitProgram, "-C", p.Path}, args...)
	res, err := m.r.Run(ctx, runtime.Cmd{Argv: argv})
	if err != nil {
		return res, fmt.Errorf("%w: project %s: git %s: %w", ErrGit, p.Name, strings.Join(args, " "), err)
	}
	return res, nil
}

// lookup reads a porcelain listing: at is the branch checked out at t.Path, of the path that has t.Branch.
func lookup(porcelain string, t Tree) (at, of string) {
	var tree string
	for _, line := range strings.Split(porcelain, "\n") {
		switch {
		case strings.HasPrefix(line, keyWorktree):
			tree = strings.TrimPrefix(line, keyWorktree)
		case strings.HasPrefix(line, keyBranch+headsRef):
			b := strings.TrimPrefix(line, keyBranch+headsRef)
			if filepath.Clean(tree) == filepath.Clean(t.Path) {
				at = b
			}
			if b == t.Branch {
				of = tree
			}
		}
	}
	return at, of
}
