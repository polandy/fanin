package intake

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	probeImage  = "ghcr.io/acme/alpha"
	probeModule = "modules/alpha/docker-compose.yml"
	probeGlob   = "modules/*/docker-compose.yml"
	probeDigest = "sha256:d1"
)

var (
	probeApp = config.Project{Name: "alpha", Path: "/path/to/alpha", Host: config.HostLocal, Base: "main"}
	probeCfg = config.Project{Name: "nixos", Path: "/path/to/nixos", Host: config.HostLocal, Base: "master"}
	gitApp   = []string{"git", "-C", "/path/to/alpha"}
	gitCfg   = []string{"git", "-C", "/path/to/nixos"}
	grepArgv = append(gitCfg, "grep", "-lE", `image: *"?ghcr\.io/acme/alpha[:@]`, "origin/master", "--", probeGlob)
	showArgv = append(gitCfg, "show", "origin/master:"+probeModule)
	inspect  = []string{"docker", "buildx", "imagetools", "inspect", probeImage + ":1.2.3", "--format",
		"{{json .Manifest}}"}
)

func newProbe(t *testing.T) (GitProbe, *testutil.RecordingRunner) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	t.Cleanup(r.Done)
	return GitProbe{Runner: r}, r
}

func TestGitProbe(t *testing.T) {
	ctx := t.Context()

	t.Run("DR-1_tag_commit", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(append(gitApp, "fetch", "--tags", "--force", "origin"), testutil.Reply{})
		r.Expect(append(gitApp, "rev-parse", "--verify", "--quiet", "refs/tags/v1.2.3^{commit}"),
			testutil.Reply{Stdout: "abc123\n"})
		r.Expect(append(gitApp, "rev-parse", "--verify", "--quiet", "refs/tags/1.2.3^{commit}"),
			testutil.Reply{ExitCode: 1})
		if err := p.FetchTags(ctx, probeApp); err != nil {
			t.Fatal(err)
		}
		sha, ok, err := p.TagCommit(ctx, probeApp, "v1.2.3")
		if err != nil || !ok || sha != "abc123" {
			t.Errorf("TagCommit = %q, %v, %v; want abc123", sha, ok, err)
		}
		if _, ok, err := p.TagCommit(ctx, probeApp, "1.2.3"); err != nil || ok {
			t.Errorf("TagCommit of a missing tag = %v, %v; want not found", ok, err)
		}
	})

	t.Run("DR-1_workflows_at_the_tag", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(append(gitApp, "grep", "-lE", DockerPushPattern, "v1.2.3", "--", WorkflowDir),
			testutil.Reply{Stdout: "v1.2.3:.github/workflows/docker.yml\nv1.2.3:.github/workflows/push.yaml\n"})
		r.Expect(append(gitApp, "grep", "-lE", CallerPattern("x.yml"), "v1.2.3", "--", WorkflowDir),
			testutil.Reply{ExitCode: 1})
		got, err := p.WorkflowsAt(ctx, probeApp, "v1.2.3", DockerPushPattern)
		if d := cmp.Diff([]string{"docker.yml", "push.yaml"}, got); err != nil || d != "" {
			t.Errorf("WorkflowsAt = %v (-want +got):\n%s", err, d)
		}
		if got, err := p.WorkflowsAt(ctx, probeApp, "v1.2.3", CallerPattern("x.yml")); err != nil || len(got) != 0 {
			t.Errorf("WorkflowsAt without a match = %q, %v; want none", got, err)
		}
	})

	t.Run("DR-1_image_digest", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(inspect, testutil.Reply{Stdout: `{"mediaType":"x","digest":"sha256:d1","size":1}`})
		if got, err := p.ImageDigest(ctx, probeImage+":1.2.3"); err != nil || got != probeDigest {
			t.Errorf("ImageDigest = %q, %v; want %s", got, err, probeDigest)
		}
	})

	t.Run("DR-1_compose_module_and_its_images", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(append(gitCfg, "fetch", "origin", "master"), testutil.Reply{})
		r.Expect(grepArgv, testutil.Reply{Stdout: "origin/master:" + probeModule + "\n"})
		r.Expect(showArgv, testutil.Reply{Stdout: "services:\n  a:\n    image: \"ghcr.io/acme/alpha:1.2.2@sha256:old\"\n" +
			"  b:\n    image: ghcr.io/acme/alpha:1.2.2@sha256:old\n  db:\n    image: postgres:16\n"})
		if err := p.FetchBase(ctx, probeCfg); err != nil {
			t.Fatal(err)
		}
		mods, err := p.ComposeModules(ctx, probeCfg, probeGlob, probeImage)
		if d := cmp.Diff([]string{probeModule}, mods); err != nil || d != "" {
			t.Errorf("ComposeModules = %v (-want +got):\n%s", err, d)
		}
		imgs, err := p.ModuleImages(ctx, probeCfg, probeModule, probeImage)
		if d := cmp.Diff([]string{"ghcr.io/acme/alpha:1.2.2@sha256:old"}, imgs); err != nil || d != "" {
			t.Errorf("ModuleImages = %v (-want +got):\n%s", err, d)
		}
	})

	t.Run("DR-2_no_compose_module_is_none", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(grepArgv, testutil.Reply{ExitCode: 1})
		if got, err := p.ComposeModules(ctx, probeCfg, probeGlob, probeImage); err != nil || len(got) != 0 {
			t.Errorf("ComposeModules = %q, %v; want none", got, err)
		}
	})

	failures := []struct {
		name string
		argv []string
		call func(GitProbe) error
	}{
		{"DR-2_failed_fetch", append(gitApp, "fetch", "--tags", "--force", "origin"),
			func(p GitProbe) error { return p.FetchTags(ctx, probeApp) }},
		{"DR-2_failed_rev_parse", append(gitApp, "rev-parse", "--verify", "--quiet", "refs/tags/v1^{commit}"),
			func(p GitProbe) error { _, _, err := p.TagCommit(ctx, probeApp, "v1"); return err }},
		{"DR-2_failed_grep", grepArgv,
			func(p GitProbe) error { _, err := p.ComposeModules(ctx, probeCfg, probeGlob, probeImage); return err }},
		{"DR-2_failed_show", showArgv,
			func(p GitProbe) error { _, err := p.ModuleImages(ctx, probeCfg, probeModule, probeImage); return err }},
		{"DR-2_failed_inspect", inspect,
			func(p GitProbe) error { _, err := p.ImageDigest(ctx, probeImage+":1.2.3"); return err }},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			p, r := newProbe(t)
			r.Expect(tc.argv, testutil.Reply{ExitCode: 128, Stderr: "fatal: broken"})
			var exit *runtime.ExitError
			if err := tc.call(p); !errors.As(err, &exit) {
				t.Errorf("err = %v; want the exit error", err)
			}
		})
	}

	t.Run("DR-2_unreadable_manifest", func(t *testing.T) {
		p, r := newProbe(t)
		r.Expect(inspect, testutil.Reply{Stdout: "not json"})
		if _, err := p.ImageDigest(ctx, probeImage+":1.2.3"); err == nil {
			t.Error("ImageDigest of an unreadable manifest succeeded")
		}
	})

	t.Run("DR-2_remote_clone_is_refused", func(t *testing.T) {
		p, _ := newProbe(t)
		remote := probeApp
		remote.Host = "host-a"
		if err := p.FetchTags(ctx, remote); !errors.Is(err, ErrRelease) {
			t.Errorf("FetchTags on host-a = %v; want ErrRelease", err)
		}
		if err := p.FetchBase(ctx, remote); !errors.Is(err, ErrRelease) {
			t.Errorf("FetchBase on host-a = %v; want ErrRelease", err)
		}
	})
}
