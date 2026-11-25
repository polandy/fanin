package sched

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	ocSubject   = "fix: alpha flag"
	ocRemoteURL = "/srv/sandbox/alpha\n"
)

// fakePRForge lists open PRs and records the PRs it opens.
type fakePRForge struct {
	open    []forge.PR
	listErr error
	opened  []forge.OpenRequest
}

func (f *fakePRForge) ListOpen(_ context.Context, _ string) ([]forge.PR, error) {
	return f.open, f.listErr
}

func (f *fakePRForge) Open(_ context.Context, req forge.OpenRequest) (int, error) {
	f.opened = append(f.opened, req)
	return prNumber, nil
}

func ocProject() config.Project {
	return config.Project{Name: projAlpha, Path: pathAlpha, Forge: "gh", Base: baseMain}
}

func git(args ...string) []string { return append([]string{"git", "-C", pathAlpha}, args...) }

var (
	remoteArgv = git("remote", "get-url", OCRemote)
	fetchArgv  = git("fetch", "-q", OCRemote, "+refs/heads/"+ocBranch+":refs/remotes/"+OCRemote+"/"+ocBranch)
	pushArgv   = git("push", "-q", "origin", "refs/remotes/"+OCRemote+"/"+ocBranch+":refs/heads/"+ocBranch)
	subjArgv   = git("log", "-1", "--format=%s", "refs/remotes/"+OCRemote+"/"+ocBranch)
)

func newPush(t *testing.T, f *fakePRForge) (*testutil.RecordingRunner, OCPush) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	return r, OCPush{Runner: r, Forge: f, Forges: testConfig().Forges,
		Repo: func(config.Project) (string, error) { return ocRepo, nil }}
}

func TestOCPush(t *testing.T) {
	t.Run("OC-1_fetches_pushes_fast_forward_only_and_opens_the_PR_once_with_the_subject_and_reviewer", func(t *testing.T) {
		f := &fakePRForge{open: []forge.PR{{Number: 7, HeadBranch: "oc/other"}}}
		r, push := newPush(t, f)
		r.Expect(remoteArgv, testutil.Reply{Stdout: ocRemoteURL})
		r.Expect(fetchArgv, testutil.Reply{})
		r.Expect(pushArgv, testutil.Reply{})
		r.Expect(subjArgv, testutil.Reply{Stdout: ocSubject + "\n"})

		pr, err := push.Deliver(t.Context(), ocProject(), ocBranch)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		r.Done()
		if pr != (OCPR{Repo: ocRepo, Number: prNumber}) {
			t.Errorf("pr = %+v, want %s#%d", pr, ocRepo, prNumber)
		}
		want := []forge.OpenRequest{{Repo: ocRepo, Head: ocBranch, Base: baseMain, Title: ocSubject, Body: OCPRBody}}
		if diff := cmp.Diff(want, f.opened); diff != "" {
			t.Errorf("opened (-want +got):\n%s", diff)
		}
		if !strings.Contains(OCPRBody, OCReviewerName) {
			t.Errorf("body %q does not name the reviewer %q", OCPRBody, OCReviewerName)
		}
		for _, c := range r.Calls() {
			for _, a := range c.Argv {
				if a == "--force" || a == "-f" || strings.HasPrefix(a, "+refs/remotes/") {
					t.Errorf("call %q forces", c.Argv)
				}
			}
		}
	})

	t.Run("OC-1_reuses_the_open_PR_on_fix_commits", func(t *testing.T) {
		f := &fakePRForge{open: []forge.PR{{Repo: ocRepo, Number: 9, HeadBranch: ocBranch}}}
		r, push := newPush(t, f)
		r.Expect(remoteArgv, testutil.Reply{Stdout: ocRemoteURL})
		r.Expect(fetchArgv, testutil.Reply{})
		r.Expect(pushArgv, testutil.Reply{})

		pr, err := push.Deliver(t.Context(), ocProject(), ocBranch)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		r.Done()
		if pr.Number != 9 || len(f.opened) != 0 {
			t.Errorf("pr = %+v, opened %d; want #9 reused and none opened", pr, len(f.opened))
		}
	})

	t.Run("CF-4_a_rewritten_branch_is_refused_it_is_never_forced", func(t *testing.T) {
		f := &fakePRForge{}
		r, push := newPush(t, f)
		r.Expect(remoteArgv, testutil.Reply{Stdout: ocRemoteURL})
		r.Expect(fetchArgv, testutil.Reply{})
		r.Expect(pushArgv, testutil.Reply{Stderr: "! [rejected] (non-fast-forward)", ExitCode: 1})

		_, err := push.Deliver(t.Context(), ocProject(), ocBranch)
		if !errors.Is(err, ErrOCPush) || !strings.Contains(err.Error(), "it is never forced") {
			t.Fatalf("err = %v, want ErrOCPush saying it is never forced", err)
		}
		r.Done()
		if len(f.opened) != 0 {
			t.Errorf("opened %d PRs, want none", len(f.opened))
		}
	})

	gitea := ocProject()
	gitea.Forge = "gt"
	refused := []struct {
		name    string
		p       config.Project
		branch  string
		script  func(*testutil.RecordingRunner)
		want    error
		message string
	}{
		{"OC-1_refuses_a_branch_without_the_oc_prefix", ocProject(), "feat/alpha", nil, ErrOCBranch, "oc/"},
		{"OC-1_refuses_a_Gitea_project", gitea, ocBranch, nil, ErrOCForge, "GitHub"},
		{"OC-1_refuses_a_missing_sandbox_remote", ocProject(), ocBranch, func(r *testutil.RecordingRunner) {
			r.Expect(remoteArgv, testutil.Reply{Stderr: "error: No such remote", ExitCode: 2})
		}, ErrOCRemote, "git remote add " + OCRemote},
		{"OC-1_refuses_a_branch_the_clone_lacks", ocProject(), ocBranch, func(r *testutil.RecordingRunner) {
			r.Expect(remoteArgv, testutil.Reply{Stdout: ocRemoteURL})
			r.Expect(fetchArgv, testutil.Reply{Stderr: "couldn't find remote ref", ExitCode: 128})
		}, ErrOCFetch, "cannot fetch " + ocBranch},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePRForge{}
			r, push := newPush(t, f)
			if tc.script != nil {
				tc.script(r)
			}
			_, err := push.Deliver(t.Context(), tc.p, tc.branch)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %v, want %v with %q", err, tc.want, tc.message)
			}
			r.Done()
			if len(f.opened) != 0 {
				t.Errorf("opened %d PRs, want none", len(f.opened))
			}
		})
	}

	t.Run("OC-1_an_unreadable_PR_list_is_an_error_and_opens_nothing", func(t *testing.T) {
		f := &fakePRForge{listErr: errors.New("gh: HTTP 502")}
		r, push := newPush(t, f)
		r.Expect(remoteArgv, testutil.Reply{Stdout: ocRemoteURL})
		r.Expect(fetchArgv, testutil.Reply{})
		r.Expect(pushArgv, testutil.Reply{})
		if _, err := push.Deliver(t.Context(), ocProject(), ocBranch); err == nil || !strings.Contains(err.Error(), "502") {
			t.Fatalf("err = %v, want the list error", err)
		}
		r.Done()
		if len(f.opened) != 0 {
			t.Errorf("opened %d PRs, want none", len(f.opened))
		}
	})
}
