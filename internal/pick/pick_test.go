package pick_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	repoDir    = "/work/repo"
	baseBranch = "trunk"
)

var cfg = pick.Config{Model: "haiku", Timeout: 600 * time.Second}

func argv(tools string) []string {
	return []string{"claude", "-p", "--output-format", "json", "--model", "haiku", "--allowedTools", tools}
}

func reply(out string) testutil.Reply {
	return testutil.Reply{Stdout: fmt.Sprintf(`{"is_error":false,"result":%q,"usage":{"input_tokens":7,"output_tokens":3}}`, out)}
}

func picker(r runtime.Runner, clk clock.Clock) *pick.Picker {
	h := headless.New(r, clk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return pick.New(h, cfg)
}

func req(forge pick.Forge) pick.Request {
	return pick.Request{Project: "fanin", Forge: forge, Dir: repoDir, BaseBranch: baseBranch, InProgress: []string{"AP-18"}}
}

func TestPickPK1_GitHubReadOnlyPickLine(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(argv("Read,Glob,Grep,Bash(gh pr list:*),Bash(gh issue list:*)"),
		reply("thinking...\nPICK opus: AP-19 headless runtime"))
	got, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), req(pick.ForgeGitHub))
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	rr.Done()
	want := pick.Result{Task: "AP-19 headless runtime", Model: pick.ModelOpus, Usage: headless.Usage{Input: 7, Output: 3}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("result (-want +got):\n%s", diff)
	}
	c := rr.Calls()[0]
	if c.Dir != repoDir {
		t.Errorf("dir = %q, want %q", c.Dir, repoDir)
	}
	for _, s := range []string{"fanin", baseBranch, "AP-18", "gh pr list", "PICK sonnet|opus: <task>", "NOTHING: <reason>"} {
		if !strings.Contains(string(c.Stdin), s) {
			t.Errorf("prompt lacks %q:\n%s", s, c.Stdin)
		}
	}
}

func TestPickPK1_TimeoutIs600s(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	br := &hangRunner{started: make(chan struct{})}
	errc := make(chan error, 1)
	go func() {
		_, err := picker(br, clk).Pick(context.Background(), req(pick.ForgeGitHub))
		errc <- err
	}()
	<-br.started
	clk.Advance(599 * time.Second)
	if clk.Pending() != 1 {
		t.Fatalf("pending timers = %d, want the picker timeout still armed", clk.Pending())
	}
	clk.Advance(time.Second)
	if err := <-errc; !errors.Is(err, headless.ErrTimeout) || !errors.Is(err, pick.ErrBadPick) {
		t.Errorf("err = %v, want ErrTimeout and ErrBadPick (PK-4)", err)
	}
}

type hangRunner struct{ started chan struct{} }

func (h *hangRunner) Run(ctx context.Context, _ runtime.Cmd) (runtime.Result, error) {
	close(h.started)
	<-ctx.Done()
	return runtime.Result{}, ctx.Err()
}

// see SCENARIOS PK-2
func TestPickPK2_GiteaGetsNoGhTools(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(argv("Read,Glob,Grep"), reply("PICK sonnet: fix the thing"))
	got, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), req(pick.ForgeGitea))
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	rr.Done()
	if got.Model != pick.ModelSonnet || got.Task != "fix the thing" {
		t.Errorf("pick = %+v", got)
	}
	in := string(rr.Calls()[0].Stdin)
	if strings.Contains(in, "gh ") || !strings.Contains(in, baseBranch) || !strings.Contains(in, "AP-18") {
		t.Errorf("gitea prompt must hold the base branch and the in-progress list and no gh:\n%s", in)
	}
}

func TestPickPK3_NothingIsIdle(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(argv("Read,Glob,Grep,Bash(gh pr list:*),Bash(gh issue list:*)"), reply("NOTHING: every todo is in progress\n"))
	got, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), req(pick.ForgeGitHub))
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if !got.Idle || got.Reason != "every todo is in progress" || got.Task != "" || got.Model != "" {
		t.Errorf("result = %+v, want idle with the reason and no task", got)
	}
}

func TestPickPK4_BadOutputIsAnErrorWithTail(t *testing.T) {
	tests := []struct{ name, out string }{
		{"junk", "I could not decide"},
		{"empty", ""},
		{"unknown model", "PICK haiku: do it"},
		{"pick inside a line", "I would say PICK sonnet: do it"},
		{"pick not last", "PICK sonnet: do it\nbut wait"},
		{"no task", "PICK sonnet: "},
		{"no colon", "PICK sonnet do it"},
		{"nothing without reason", "NOTHING:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			rr.Expect(argv("Read,Glob,Grep"), reply(tc.out))
			got, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), req(pick.ForgeGitea))
			if !errors.Is(err, pick.ErrBadPick) {
				t.Fatalf("err = %v, want ErrBadPick", err)
			}
			if !strings.Contains(err.Error(), strings.TrimSpace(tc.out)) {
				t.Errorf("err %q lacks the output tail %q", err, tc.out)
			}
			if got.Task != "" || got.Model != "" || got.Idle {
				t.Errorf("result = %+v, want no pick", got)
			}
		})
	}
}

func TestPickPK4_RunnerFailureIsAnError(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(argv("Read,Glob,Grep"), testutil.Reply{Stderr: "boom", ExitCode: 2})
	_, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), req(pick.ForgeGitea))
	var ee *runtime.ExitError
	if !errors.As(err, &ee) || !errors.Is(err, pick.ErrBadPick) {
		t.Errorf("err = %v, want *ExitError and ErrBadPick", err)
	}
}

func TestPickInvalidRequest(t *testing.T) {
	for name, mut := range map[string]func(*pick.Request){
		"no dir":        func(r *pick.Request) { r.Dir = "" },
		"no base":       func(r *pick.Request) { r.BaseBranch = "" },
		"unknown forge": func(r *pick.Request) { r.Forge = "svn" },
	} {
		t.Run(name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			r := req(pick.ForgeGitHub)
			mut(&r)
			if _, err := picker(rr, clock.NewManual(time.Unix(0, 0))).Pick(context.Background(), r); !errors.Is(err, runtime.ErrInvalidSpec) {
				t.Errorf("err = %v, want ErrInvalidSpec", err)
			}
		})
	}
}
