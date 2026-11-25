//go:build integration

package pick_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/testutil/fakebin"
)

const claudeAnswer = `{"is_error":false,"result":"Looked at the todo.\nPICK sonnet: AP-22 integration harness",` +
	`"usage":{"input_tokens":3,"output_tokens":5,"cache_creation_input_tokens":7,"cache_read_input_tokens":11}}`

func TestPickOverExecAgainstFakeClaude(t *testing.T) {
	t.Run("PK-1_fake_claude_prints_a_PICK_line", func(t *testing.T) {
		repo, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		argv := []string{headless.Command, "-p", "--output-format", "json", "--model", "haiku",
			"--allowedTools", "Read,Glob,Grep,Bash(gh pr list:*),Bash(gh issue list:*)"}
		bin := fakebin.New(t)
		bin.Fake(headless.Command).On(argv[1:], fakebin.Answer{Stdout: claudeAnswer})
		bin.Install()
		// The clock never advances: the 600 s timeout is armed, not waited for.
		rt := headless.New(exec.Runner{}, clock.NewManual(time.Unix(0, 0)), slog.New(slog.NewTextHandler(io.Discard, nil)))

		got, err := pick.New(rt, pick.Config{Model: "haiku"}).Pick(context.Background(), pick.Request{
			Project: "proj", Forge: pick.ForgeGitHub, Dir: repo, BaseBranch: "main",
		})
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		want := pick.Result{Task: "AP-22 integration harness", Model: pick.ModelSonnet,
			Usage: headless.Usage{Input: 3, Output: 5, CacheCreation: 7, CacheRead: 11}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("result (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]fakebin.Call{{Argv: argv[1:], Dir: repo}}, bin.Calls(headless.Command)); diff != "" {
			t.Errorf("claude calls (-want +got):\n%s", diff)
		}
	})
}
