// Package scope checks a task's allowed_paths and posts the fanin/scope commit status on its PR head (see
// SCENARIOS SC-1, SC-2; spec in docs/dev/spec/gate.md).
package scope

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/polandy/fanin/internal/forge"
)

// Context is the commit status fanind posts on every reported PR head; a branch protection may require it.
const Context = "fanin/scope"

const (
	// MaxPaths caps the allowed_paths of one task.
	MaxPaths = 20
	// MaxDescription is the longest status description a forge takes (GitHub's limit).
	MaxDescription = 140
	// anyDirs is the glob segment that matches zero or more directories.
	anyDirs = "**"
	// ellipsis ends a description cut to MaxDescription.
	ellipsis = "…"
)

// The fanin/scope descriptions; DescriptionOutside leads the list of files outside the scope.
const (
	DescriptionNone    = "the task has no allowed_paths"
	DescriptionInside  = "every changed file is inside the task's allowed_paths"
	DescriptionOutside = "outside allowed_paths: "
)

// ErrInvalid is an allowed_paths entry that breaks a rule of Validate.
var ErrInvalid = errors.New("scope: invalid allowed_paths")

// Validate checks paths: at most MaxPaths repo-relative paths or globs, none empty, absolute, with a drive
// prefix, a backslash (so no UNC path), a ".." segment, a control character or a malformed glob.
func Validate(paths []string) error {
	if len(paths) > MaxPaths {
		return fmt.Errorf("%w: %d entries; give at most %d paths or globs", ErrInvalid, len(paths), MaxPaths)
	}
	for _, p := range paths {
		if err := validOne(p); err != nil {
			return fmt.Errorf("%w: entry %q %s", ErrInvalid, p, err.Error())
		}
	}
	return nil
}

func validOne(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return errors.New("is empty; give a repo-relative path such as internal/gate/")
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return errors.New("holds a control character; remove it")
	case strings.HasPrefix(p, "/"):
		return errors.New("is absolute; give it relative to the repo root")
	case strings.Contains(p, `\`):
		return errors.New("holds a backslash (a UNC or Windows path); use / between directories")
	case len(p) >= 2 && p[1] == ':':
		return errors.New("has a drive prefix; give it relative to the repo root")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return errors.New("has a .. segment; name a path inside the repo")
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("is no valid glob (%w); fix the pattern", err)
		}
	}
	return nil
}

// Match reports whether file is inside pattern: a path ending in "/" holds everything below it, "*" stays in one
// directory and a "**" segment spans any number of directories.
func Match(pattern, file string) bool {
	if dir, ok := strings.CutSuffix(pattern, "/"); ok {
		pattern = dir + "/" + anyDirs
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == anyDirs {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && matchSegs(pat[1:], segs[1:])
}

// Outside lists the files that match none of paths, in input order; no paths means no scope, so nil.
func Outside(paths, files []string) []string {
	if len(paths) == 0 {
		return nil
	}
	var out []string
	for _, f := range files {
		inside := false
		for _, p := range paths {
			if Match(p, f) {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, f)
		}
	}
	return out
}

// Forge is what Post needs from a forge.
type Forge interface {
	Files(ctx context.Context, repo string, number int) ([]string, error)
	PostStatus(ctx context.Context, st forge.Status) error
}

// Post posts fanin/scope on head of PR number: failure when the PR changes a file outside paths, success
// otherwise or when paths is empty. A file list or post that fails is an error and nothing is posted on a failed
// read (fail closed).
func Post(ctx context.Context, f Forge, repo string, number int, head string, paths []string) (forge.StatusState,
	error) {
	if head == "" {
		return "", fmt.Errorf("%w: %s on %s#%d without a head SHA; read the PR first", forge.ErrInvalidRequest,
			Context, repo, number)
	}
	if err := Validate(paths); err != nil {
		return "", err
	}
	st := forge.Status{Repo: repo, SHA: head, Context: Context, State: forge.StatusSuccess, Description: DescriptionNone}
	if len(paths) > 0 {
		files, err := f.Files(ctx, repo, number)
		if err != nil {
			return "", fmt.Errorf("scope: read the files of %s#%d: %w", repo, number, err)
		}
		st.Description = DescriptionInside
		if out := Outside(paths, files); len(out) > 0 {
			st.State, st.Description = forge.StatusFailure, cut(DescriptionOutside+strings.Join(out, ", "))
		}
	}
	if err := f.PostStatus(ctx, st); err != nil {
		return "", fmt.Errorf("scope: post %s on %s@%s: %w", Context, repo, head, err)
	}
	return st.State, nil
}

// cut shortens s to MaxDescription runes.
func cut(s string) string {
	r := []rune(s)
	if len(r) <= MaxDescription {
		return s
	}
	return string(r[:MaxDescription-len([]rune(ellipsis))]) + ellipsis
}
