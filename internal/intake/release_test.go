package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
)

const (
	projNixos   = "nixos"
	relRepo     = "acme/Alpha"
	relImage    = "ghcr.io/acme/alpha"
	relVersion  = "v1.2.3"
	relSHA      = "abc123"
	relDigest   = "sha256:" + "d1"
	relModule   = "modules/alpha/docker-compose.yml"
	relWorkflow = "docker.yml"
	relRunID    = int64(42)
)

// instantClock fires every After at once and counts the waits.
type instantClock struct{ waits []time.Duration }

func (c *instantClock) Now() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) }

func (c *instantClock) After(d time.Duration) <-chan time.Time {
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// fakeProbe answers the git and registry reads of a release deploy.
type fakeProbe struct {
	tags map[string]string
	// tagAfterFetches hides the tags until this many tag fetches ran.
	tagAfterFetches int
	fetches         int
	docker          []string
	callers         map[string][]string
	digest          string
	digestErr       error
	modules         []string
	images          []string
	probed          []string
}

func (f *fakeProbe) FetchTags(_ context.Context, _ config.Project) error {
	f.fetches++
	return nil
}

func (f *fakeProbe) TagCommit(_ context.Context, _ config.Project, tag string) (string, bool, error) {
	f.probed = append(f.probed, tag)
	if f.fetches < f.tagAfterFetches {
		return "", false, nil
	}
	sha, ok := f.tags[tag]
	return sha, ok, nil
}

func (f *fakeProbe) WorkflowsAt(_ context.Context, _ config.Project, _, pattern string) ([]string, error) {
	if pattern == DockerPushPattern {
		return f.docker, nil
	}
	for wf, callers := range f.callers {
		if pattern == CallerPattern(wf) {
			return callers, nil
		}
	}
	return nil, nil
}

func (f *fakeProbe) ImageDigest(_ context.Context, ref string) (string, error) {
	if ref != relImage+":1.2.3" {
		return "", fmt.Errorf("unexpected ref %q", ref)
	}
	return f.digest, f.digestErr
}

func (f *fakeProbe) FetchBase(_ context.Context, _ config.Project) error { return nil }

func (f *fakeProbe) ComposeModules(_ context.Context, _ config.Project, _, _ string) ([]string, error) {
	return f.modules, nil
}

func (f *fakeProbe) ModuleImages(_ context.Context, _ config.Project, _, _ string) ([]string, error) {
	return f.images, nil
}

// fakeWorkflows holds the runs of each workflow and the verdict of a watch.
type fakeWorkflows struct {
	runs     map[string]int64
	watchErr error
	watched  []int64
}

func (f *fakeWorkflows) LatestRun(_ context.Context, repo, workflow, sha string) (int64, error) {
	if repo != relRepo || sha != relSHA {
		return 0, fmt.Errorf("unexpected run lookup %s %s", repo, sha)
	}
	if id, ok := f.runs[workflow]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("%w: no run of %s", forge.ErrNotFound, workflow)
}

func (f *fakeWorkflows) WatchRun(_ context.Context, _ string, id int64) error {
	f.watched = append(f.watched, id)
	return f.watchErr
}

func releaseConfig() config.Config {
	cfg := testConfig()
	cfg.Projects = append(cfg.Projects, config.Project{Name: projNixos, Forge: config.ForgeGitea, WorkerKind: kindClaude,
		Base: "master"})
	cfg.Triggers.Release = &config.Release{Project: projNixos, ComposeGlob: "modules/*/docker-compose.yml",
		Registry: "ghcr.io", Model: modelSonnet}
	return cfg
}

// goodRelease is a release whose tag, run, digest and one module exist, with the module on an older image.
func goodRelease() (*fakeProbe, *fakeWorkflows) {
	return &fakeProbe{
			tags: map[string]string{relVersion: relSHA}, docker: []string{relWorkflow}, digest: relDigest,
			modules: []string{relModule}, images: []string{relImage + ":1.2.2@sha256:old"},
		},
		&fakeWorkflows{runs: map[string]int64{relWorkflow: relRunID}}
}

func newDeployer(t *testing.T, p *fakeProbe, w *fakeWorkflows) (*Deployer, *instantClock, func() []model.Task) {
	t.Helper()
	_, st := newService(t)
	cfg := releaseConfig()
	clk := &instantClock{}
	d := &Deployer{Config: cfg, Probe: p, Workflows: w, Intake: New(cfg, st), Clock: clk}
	return d, clk, func() []model.Task { return openTasks(t, st) }
}

var relEvent = ReleaseEvent{Project: projGH, Repo: relRepo, Version: relVersion}

// TestDeployFailures covers SCENARIOS DR-2: every missing piece is an error and queues no task.
func TestDeployFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fakeProbe, *fakeWorkflows)
		ev   ReleaseEvent
		want error
	}{
		{"DR-2_no_compose_module", func(p *fakeProbe, _ *fakeWorkflows) { p.modules = nil }, relEvent, ErrRelease},
		{"DR-2_several_compose_modules", func(p *fakeProbe, _ *fakeWorkflows) {
			p.modules = []string{relModule, "modules/beta/docker-compose.yml"}
		}, relEvent, ErrRelease},
		{"DR-2_no_workflow_pushes_an_image", func(p *fakeProbe, _ *fakeWorkflows) { p.docker = nil }, relEvent,
			ErrRelease},
		{"DR-2_no_workflow_run", func(_ *fakeProbe, w *fakeWorkflows) { w.runs = nil }, relEvent, ErrRelease},
		{"DR-2_run_failed", func(_ *fakeProbe, w *fakeWorkflows) {
			w.watchErr = fmt.Errorf("%w: run 42", forge.ErrRunFailed)
		}, relEvent, forge.ErrRunFailed},
		{"DR-2_no_digest", func(p *fakeProbe, _ *fakeWorkflows) { p.digest = "" }, relEvent, ErrRelease},
		{"DR-2_digest_not_sha256", func(p *fakeProbe, _ *fakeWorkflows) { p.digest = "latest" }, relEvent,
			ErrRelease},
		{"DR-2_digest_unreadable", func(p *fakeProbe, _ *fakeWorkflows) { p.digestErr = errors.New("no manifest") },
			relEvent, ErrRelease},
		{"DR-2_no_tag", func(p *fakeProbe, _ *fakeWorkflows) { p.tags = nil }, relEvent, ErrRelease},
		{"DR-2_bad_repo", nil, ReleaseEvent{Project: projGH, Repo: "--exec/x", Version: relVersion}, ErrRelease},
		{"DR-2_bad_version", nil, ReleaseEvent{Project: projGH, Repo: relRepo, Version: "v1;rm -rf"}, ErrRelease},
		{"DR-2_not_a_github_project", nil, ReleaseEvent{Project: projGitea, Repo: relRepo, Version: relVersion},
			ErrRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, w := goodRelease()
			if tc.edit != nil {
				tc.edit(p, w)
			}
			d, _, tasks := newDeployer(t, p, w)
			res, err := d.Deploy(context.Background(), tc.ev)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Deploy = %+v, %v; want %v", res, err, tc.want)
			}
			if got := tasks(); len(got) != 0 {
				t.Errorf("queued %d tasks on an error, want none: %+v", len(got), got)
			}
		})
	}
}

// TestDeployLookups covers SCENARIOS DR-1 and DR-2: the tag and the run get 10 lookups 30 s apart.
func TestDeployLookups(t *testing.T) {
	t.Run("DR-2_no_tag_after_ten_lookups", func(t *testing.T) {
		p, w := goodRelease()
		p.tags = nil
		d, clk, _ := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), relEvent); !errors.Is(err, ErrRelease) {
			t.Fatalf("Deploy: %v, want ErrRelease", err)
		}
		if p.fetches != ReleaseLookups || len(clk.waits) != ReleaseLookups-1 || clk.waits[0] != ReleaseLookupWait {
			t.Errorf("fetches %d, waits %v; want %d fetches and %d waits of %s", p.fetches, clk.waits,
				ReleaseLookups, ReleaseLookups-1, ReleaseLookupWait)
		}
	})
	t.Run("DR-2_no_run_after_ten_lookups", func(t *testing.T) {
		p, w := goodRelease()
		w.runs = nil
		d, clk, _ := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), relEvent); !errors.Is(err, ErrRelease) {
			t.Fatalf("Deploy: %v, want ErrRelease", err)
		}
		if len(clk.waits) != ReleaseLookups-1 {
			t.Errorf("waits %v, want %d", clk.waits, ReleaseLookups-1)
		}
	})
	t.Run("DR-1_tag_pushed_after_the_release", func(t *testing.T) {
		p, w := goodRelease()
		p.tagAfterFetches = 3
		d, clk, tasks := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), relEvent); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if len(clk.waits) != 2 || len(tasks()) != 1 {
			t.Errorf("waits %v, tasks %d; want 2 waits and one task", clk.waits, len(tasks()))
		}
	})
}

// TestDeploy covers SCENARIOS DR-1.
func TestDeploy(t *testing.T) {
	want := relImage + ":1.2.3@" + relDigest
	t.Run("DR-1_already_current_is_nothing_to_do", func(t *testing.T) {
		p, w := goodRelease()
		p.images = []string{want}
		d, _, tasks := newDeployer(t, p, w)
		res, err := d.Deploy(context.Background(), relEvent)
		if err != nil || res.Outcome != OutcomeNothing {
			t.Fatalf("Deploy = %+v, %v; want nothing to do", res, err)
		}
		if got := tasks(); len(got) != 0 {
			t.Errorf("queued %+v, want nothing", got)
		}
	})
	t.Run("DR-1_bump_task_on_the_config_repo", func(t *testing.T) {
		p, w := goodRelease()
		d, _, tasks := newDeployer(t, p, w)
		res, err := d.Deploy(context.Background(), relEvent)
		if err != nil || res.Outcome != OutcomeQueued {
			t.Fatalf("Deploy = %+v, %v; want a queued task", res, err)
		}
		got := tasks()
		if len(got) != 1 {
			t.Fatalf("queued %d tasks, want 1", len(got))
		}
		task := got[0]
		// A plain owner PR task: the config repo's own gate and the owner's go decide the merge.
		if task.Project != projNixos || task.OwnerModel != modelSonnet || task.Shape != model.ShapePR ||
			!task.OwnerTask || task.WorkerKind != "" {
			t.Errorf("task = %+v; want an owner PR task on %s with model %s", task, projNixos, modelSonnet)
		}
		for _, s := range []string{want, relModule, relVersion, fmt.Sprint(relRunID)} {
			if !strings.Contains(task.Prompt, s) {
				t.Errorf("prompt %q does not name %q", task.Prompt, s)
			}
		}
		if len(w.watched) != 1 || w.watched[0] != relRunID {
			t.Errorf("watched %v, want run %d", w.watched, relRunID)
		}
	})
	t.Run("DR-1_redelivery_queues_nothing_more", func(t *testing.T) {
		p, w := goodRelease()
		d, _, tasks := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), relEvent); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		res, err := d.Deploy(context.Background(), relEvent)
		if err != nil || res.Outcome != OutcomeDuplicate || len(tasks()) != 1 {
			t.Errorf("second Deploy = %+v, %v with %d tasks; want a duplicate and one task", res, err, len(tasks()))
		}
	})
	t.Run("DR-1_component_tag", func(t *testing.T) {
		p, w := goodRelease()
		p.tags = map[string]string{"alpha-v1.2.3": relSHA}
		d, _, tasks := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), ReleaseEvent{Project: projGH, Repo: relRepo, Version: "1.2.3"}); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if len(tasks()) != 1 {
			t.Errorf("tasks %d, want 1", len(tasks()))
		}
	})
	t.Run("DR-1_run_of_the_calling_workflow", func(t *testing.T) {
		p, w := goodRelease()
		p.docker = []string{"publish.yml"}
		p.callers = map[string][]string{"publish.yml": {"release.yml"}}
		w.runs = map[string]int64{"release.yml": relRunID}
		d, _, tasks := newDeployer(t, p, w)
		if _, err := d.Deploy(context.Background(), relEvent); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if len(tasks()) != 1 {
			t.Errorf("tasks %d, want 1", len(tasks()))
		}
	})
}
