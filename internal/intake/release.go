package intake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
)

// The tag and run lookups of a release: a release-please tag and its run register a few seconds after the
// release (see SCENARIOS DR-1).
const (
	ReleaseLookups    = 10
	ReleaseLookupWait = 30 * time.Second
)

// DockerPushPattern marks a workflow file as the image build.
const DockerPushPattern = `docker/build-push-action|docker push `

// WorkflowDir holds a repo's workflow files.
const WorkflowDir = ".github/workflows"

// digestPrefix starts every image digest.
const digestPrefix = "sha256:"

// ErrRelease is a release that cannot be deployed: no tag, workflow, run, digest or exactly one compose module.
var ErrRelease = errors.New("intake: release not deployable")

var versionPattern = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z._+-]*$`)

// repoPattern is a GitHub "owner/name"; neither part may start with a dash, so it is never read as a flag.
var repoPattern = regexp.MustCompile(`^[0-9A-Za-z_][0-9A-Za-z_.-]*/[0-9A-Za-z_.][0-9A-Za-z_.-]*$`)

// ReleaseEvent is a published release of a GitHub project.
type ReleaseEvent struct {
	// Project is the fanin project of the released repo.
	Project string
	// Repo is "owner/name" on GitHub.
	Repo string
	// Version is the release tag as published, such as v1.2.3.
	Version string
}

// ReleaseProbe reads the git clones and the registry for a release deploy.
type ReleaseProbe interface {
	// FetchTags fetches the tags of p's clone.
	FetchTags(ctx context.Context, p config.Project) error
	// TagCommit returns the commit of tag; ok is false when the tag does not exist.
	TagCommit(ctx context.Context, p config.Project, tag string) (sha string, ok bool, err error)
	// WorkflowsAt lists the workflow file names under WorkflowDir at tag whose text matches pattern.
	WorkflowsAt(ctx context.Context, p config.Project, tag, pattern string) ([]string, error)
	// ImageDigest returns the manifest digest of the image ref ("image:tag").
	ImageDigest(ctx context.Context, ref string) (string, error)
	// FetchBase fetches the base branch of the config repo.
	FetchBase(ctx context.Context, target config.Project) error
	// ComposeModules lists the files matching glob on the config repo's base that run image.
	ComposeModules(ctx context.Context, target config.Project, glob, image string) ([]string, error)
	// ModuleImages lists the distinct "image:…" references of image in module on the config repo's base.
	ModuleImages(ctx context.Context, target config.Project, module, image string) ([]string, error)
}

// Outcome is what an intake source did with one event.
type Outcome string

// Outcomes of an intake event.
const (
	// OutcomeQueued left a new task in model.TaskQueued.
	OutcomeQueued            = Outcome(model.TaskQueued)
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeNothing   Outcome = "nothing"
	OutcomeIgnored   Outcome = "ignored"
	OutcomeDeploying Outcome = "deploying"
)

// Deployer turns a release into a bump task on the config repo (see SCENARIOS DR-1, DR-2).
type Deployer struct {
	Config    config.Config
	Probe     ReleaseProbe
	Workflows forge.Workflows
	Intake    Enqueuer
	Clock     clock.Clock
	// Log is nil for no logging.
	Log *slog.Logger
}

// DeployResult is what a deploy did.
type DeployResult struct {
	Outcome Outcome
	// Task is the queued bump task, for OutcomeQueued.
	Task model.Task
	// Module and Image ("image:tag@digest") are what the task bumps.
	Module string
	Image  string
}

// CallerPattern matches a workflow that calls the reusable workflow file wf as a job.
func CallerPattern(wf string) string {
	return `uses: *\./` + regexp.QuoteMeta(WorkflowDir+"/"+wf)
}

func releasef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRelease, fmt.Sprintf(format, args...))
}

// Deploy finds the release's tag, watches its image run, reads the digest and queues the bump of the one
// compose module that runs the image; an already current module is OutcomeNothing. On an error nothing is queued.
func (d *Deployer) Deploy(ctx context.Context, ev ReleaseEvent) (DeployResult, error) {
	p, target, rel, err := d.projects(ev)
	if err != nil {
		return DeployResult{}, err
	}
	if !repoPattern.MatchString(ev.Repo) {
		return DeployResult{}, releasef("repository %q; a release names its GitHub repository as owner/name", ev.Repo)
	}
	if !versionPattern.MatchString(ev.Version) {
		return DeployResult{}, releasef("version %q of %s; a release tag is letters, digits and . _ + -", ev.Version,
			ev.Repo)
	}
	imgTag := strings.TrimPrefix(ev.Version, "v")
	tag, sha, err := d.findTag(ctx, p, ev, imgTag)
	if err != nil {
		return DeployResult{}, err
	}
	run, wf, err := d.findRun(ctx, p, ev.Repo, tag, sha)
	if err != nil {
		return DeployResult{}, err
	}
	if err := d.Workflows.WatchRun(ctx, ev.Repo, run); err != nil {
		return DeployResult{}, fmt.Errorf("intake: release %s of %s: run %d (%s): %w", tag, ev.Repo, run, wf, err)
	}
	image := rel.Registry + "/" + strings.ToLower(ev.Repo)
	digest, err := d.Probe.ImageDigest(ctx, image+":"+imgTag)
	if err != nil {
		return DeployResult{}, fmt.Errorf("%w: digest of %s:%s: %w", ErrRelease, image, imgTag, err)
	}
	if !strings.HasPrefix(digest, digestPrefix) {
		return DeployResult{}, releasef("no %s digest for %s:%s (got %q); check the image was pushed", digestPrefix,
			image, imgTag, digest)
	}
	if err := d.Probe.FetchBase(ctx, target); err != nil {
		return DeployResult{}, fmt.Errorf("intake: fetch %s of %s: %w", target.Base, target.Name, err)
	}
	modules, err := d.Probe.ComposeModules(ctx, target, rel.ComposeGlob, image)
	if err != nil {
		return DeployResult{}, fmt.Errorf("intake: compose modules of %s: %w", target.Name, err)
	}
	if len(modules) != 1 {
		return DeployResult{}, releasef("want one module of %s matching %s that runs %s, found %d: %s; fix "+
			"release.compose_glob in triggers.yaml or the modules", target.Name, rel.ComposeGlob, image,
			len(modules), strings.Join(modules, ", "))
	}
	module, want := modules[0], image+":"+imgTag+"@"+digest
	current, err := d.Probe.ModuleImages(ctx, target, module, image)
	if err != nil {
		return DeployResult{}, fmt.Errorf("intake: read %s of %s: %w", module, target.Name, err)
	}
	res := DeployResult{Module: module, Image: want, Outcome: OutcomeNothing}
	if slices.Equal(current, []string{want}) {
		d.log().Info("intake: release already deployed", "project", target.Name, "module", module, "image", want)
		return res, nil
	}
	task := fmt.Sprintf("bump %s to %s in %s (every image line of it; now: %s). Release %s of %s, built by "+
		"run %d (%s). Change nothing else.", image, want, module, strings.Join(current, " "), tag, ev.Repo,
		run, wf)
	res.Task, err = d.Intake.Enqueue(ctx, TaskRequest{ID: releaseTaskID(target.Name, ev.Repo, imgTag),
		Project: target.Name, Task: task, Model: rel.Model})
	switch {
	case errors.Is(err, ErrExists):
		res.Outcome = OutcomeDuplicate
		return res, nil
	case err != nil:
		return DeployResult{}, err
	}
	res.Outcome = OutcomeQueued
	d.log().Info("intake: release bump queued", "task_id", res.Task.ID, "project", target.Name, "image", want)
	return res, nil
}

// projects returns the released project, the config repo project and the release section.
func (d *Deployer) projects(ev ReleaseEvent) (config.Project, config.Project, config.Release, error) {
	rel := d.Config.Triggers.Release
	if rel == nil {
		return config.Project{}, config.Project{}, config.Release{}, releasef("no release section in triggers.yaml; " +
			"set release.project to the config repo project")
	}
	p, ok := d.Config.Project(ev.Project)
	if !ok {
		return config.Project{}, config.Project{}, config.Release{}, fmt.Errorf("%w: %q", ErrUnknownProject, ev.Project)
	}
	if d.Config.Forges[p.Forge].Kind != config.ForgeGitHub {
		return config.Project{}, config.Project{}, config.Release{}, releasef("project %s is not on GitHub; a "+
			"release deploy reads GitHub workflow runs", p.Name)
	}
	target, ok := d.Config.Project(rel.Project)
	if !ok {
		return config.Project{}, config.Project{}, config.Release{}, fmt.Errorf("%w: release.project %q",
			ErrUnknownProject, rel.Project)
	}
	return p, target, *rel, nil
}

// findTag looks the tag up as given, with and without v, and as <repo>-v<version>.
func (d *Deployer) findTag(ctx context.Context, p config.Project, ev ReleaseEvent, imgTag string) (string, string,
	error) {
	_, name, _ := strings.Cut(ev.Repo, "/")
	name = strings.ToLower(name)
	var candidates []string
	for _, t := range []string{ev.Version, "v" + imgTag, imgTag, name + "-v" + imgTag} {
		if !slices.Contains(candidates, t) {
			candidates = append(candidates, t)
		}
	}
	for try := 1; ; try++ {
		if err := d.Probe.FetchTags(ctx, p); err != nil {
			return "", "", fmt.Errorf("intake: fetch the tags of %s: %w", p.Name, err)
		}
		for _, t := range candidates {
			sha, ok, err := d.Probe.TagCommit(ctx, p, t)
			if err != nil {
				return "", "", fmt.Errorf("intake: tag %s of %s: %w", t, p.Name, err)
			}
			if ok {
				return t, sha, nil
			}
		}
		if try == ReleaseLookups {
			return "", "", releasef("no tag for %s in %s (tried %s, %d lookups)", ev.Version, p.Name,
				strings.Join(candidates, " "), ReleaseLookups)
		}
		if err := d.wait(ctx); err != nil {
			return "", "", err
		}
	}
}

// findRun returns the run on sha of a workflow that pushes the image, or of a workflow that calls one.
func (d *Deployer) findRun(ctx context.Context, p config.Project, repo, tag, sha string) (int64, string, error) {
	workflows, err := d.Probe.WorkflowsAt(ctx, p, tag, DockerPushPattern)
	if err != nil {
		return 0, "", fmt.Errorf("intake: workflows of %s at %s: %w", p.Name, tag, err)
	}
	if len(workflows) == 0 {
		return 0, "", releasef("no workflow in %s of %s pushes an image (%s) at %s", WorkflowDir, p.Name,
			DockerPushPattern, tag)
	}
	for _, wf := range slices.Clone(workflows) {
		callers, err := d.Probe.WorkflowsAt(ctx, p, tag, CallerPattern(wf))
		if err != nil {
			return 0, "", fmt.Errorf("intake: callers of %s in %s: %w", wf, p.Name, err)
		}
		workflows = append(workflows, callers...)
	}
	for try := 1; ; try++ {
		for _, wf := range workflows {
			id, err := d.Workflows.LatestRun(ctx, repo, wf, sha)
			if errors.Is(err, forge.ErrNotFound) {
				continue
			}
			if err != nil {
				return 0, "", fmt.Errorf("intake: runs of %s in %s: %w", wf, repo, err)
			}
			return id, wf, nil
		}
		if try == ReleaseLookups {
			return 0, "", releasef("no run of %s for %s (%s) after %d lookups", strings.Join(workflows, " "), tag,
				sha, ReleaseLookups)
		}
		if err := d.wait(ctx); err != nil {
			return 0, "", err
		}
	}
}

func (d *Deployer) wait(ctx context.Context) error {
	select {
	case <-d.Clock.After(ReleaseLookupWait):
		return nil
	case <-ctx.Done():
		return fmt.Errorf("intake: release lookup: %w", ctx.Err())
	}
}

func (d *Deployer) log() *slog.Logger {
	if d.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Log
}

// releaseTaskID names the bump task, so a redelivered release queues nothing more.
func releaseTaskID(target, repo, imgTag string) model.TaskID {
	_, name, _ := strings.Cut(repo, "/")
	return model.TaskID(fmt.Sprintf("%s-release-%s-%s", target, strings.ToLower(name), imgTag))
}
