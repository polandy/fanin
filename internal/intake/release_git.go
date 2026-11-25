package intake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/runtime"
)

// Programs the probe runs, resolved on PATH of the control host.
const (
	gitProgram    = "git"
	dockerProgram = "docker"
	remoteOrigin  = "origin"
	// gitNoMatch is the exit code of git grep without a match and of rev-parse --verify --quiet without the ref.
	gitNoMatch = 1
)

var imageLine = regexp.MustCompile(`image: *"?([^"\s]+)`)

// GitProbe is the ReleaseProbe on the clones of this host and docker buildx; a clone on another host is ErrRelease.
type GitProbe struct {
	Runner runtime.Runner
}

var _ ReleaseProbe = GitProbe{}

// FetchTags fetches the tags of p's clone, moving retagged ones.
func (g GitProbe) FetchTags(ctx context.Context, p config.Project) error {
	_, err := g.git(ctx, p, "fetch", "--tags", "--force", remoteOrigin)
	return err
}

// TagCommit returns the commit of tag; ok is false when the tag does not exist.
func (g GitProbe) TagCommit(ctx context.Context, p config.Project, tag string) (string, bool, error) {
	out, err := g.git(ctx, p, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}")
	if noMatch(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// WorkflowsAt lists the workflow file names under WorkflowDir at tag whose text matches pattern.
func (g GitProbe) WorkflowsAt(ctx context.Context, p config.Project, tag, pattern string) ([]string, error) {
	files, err := g.grep(ctx, p, pattern, tag, WorkflowDir)
	if err != nil {
		return nil, err
	}
	for i, f := range files {
		files[i] = path.Base(f)
	}
	return files, nil
}

// ImageDigest returns the manifest digest of the image ref ("image:tag").
func (g GitProbe) ImageDigest(ctx context.Context, ref string) (string, error) {
	res, err := g.Runner.Run(ctx, runtime.Cmd{Argv: []string{dockerProgram, "buildx", "imagetools", "inspect", ref,
		"--format", "{{json .Manifest}}"}})
	if err != nil {
		return "", fmt.Errorf("intake: inspect %s: %w", ref, err)
	}
	var m struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(res.Stdout, &m); err != nil {
		return "", fmt.Errorf("intake: manifest of %s: %w", ref, err)
	}
	return m.Digest, nil
}

// FetchBase fetches the base branch of the config repo.
func (g GitProbe) FetchBase(ctx context.Context, target config.Project) error {
	_, err := g.git(ctx, target, "fetch", remoteOrigin, target.Base)
	return err
}

// ComposeModules lists the files matching glob on the config repo's base that run image.
func (g GitProbe) ComposeModules(ctx context.Context, target config.Project, glob, image string) ([]string, error) {
	return g.grep(ctx, target, `image: *"?`+regexp.QuoteMeta(image)+`[:@]`, remoteOrigin+"/"+target.Base, glob)
}

// ModuleImages lists the distinct references of image in module on the config repo's base.
func (g GitProbe) ModuleImages(ctx context.Context, target config.Project, module, image string) ([]string, error) {
	out, err := g.git(ctx, target, "show", remoteOrigin+"/"+target.Base+":"+module)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, m := range imageLine.FindAllStringSubmatch(out, -1) {
		if ref := m[1]; strings.HasPrefix(ref, image+":") || strings.HasPrefix(ref, image+"@") {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}

// grep returns the files under pathspec at rev whose text matches the extended regexp; none is no error.
func (g GitProbe) grep(ctx context.Context, p config.Project, pattern, rev, pathspec string) ([]string, error) {
	out, err := g.git(ctx, p, "grep", "-lE", pattern, rev, "--", pathspec)
	if noMatch(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			files = append(files, strings.TrimPrefix(line, rev+":"))
		}
	}
	return files, nil
}

// git runs git in p's clone, which must be on this host.
func (g GitProbe) git(ctx context.Context, p config.Project, args ...string) (string, error) {
	if p.Host != config.HostLocal {
		return "", releasef("project %s is on host %s; a release deploy reads clones on host %s only", p.Name,
			p.Host, config.HostLocal)
	}
	res, err := g.Runner.Run(ctx, runtime.Cmd{Argv: append([]string{gitProgram, "-C", p.Path}, args...)})
	if err != nil {
		return "", fmt.Errorf("intake: git %s in %s: %w", args[0], p.Name, err)
	}
	return string(res.Stdout), nil
}

func noMatch(err error) bool {
	var exit *runtime.ExitError
	return errors.As(err, &exit) && exit.Code == gitNoMatch
}
