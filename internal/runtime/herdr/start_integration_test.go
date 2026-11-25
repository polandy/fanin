//go:build integration

package herdr_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/testutil/fakebin"
)

func TestStartOverExecAgainstFakeHerdr(t *testing.T) {
	t.Run("SP-1_end_to_end_over_exec", func(t *testing.T) {
		bin := fakebin.New(t)
		fake := bin.Fake(herdr.Program)
		script := [][]string{
			hd("workspace", "list")[1:], hd("tab", "create", "--workspace", ws, "--cwd", worktree, "--label", agent, "--no-focus")[1:],
			startArgv[1:], promptArgv[1:], getArgv[1:],
		}
		fake.On(script[0], fakebin.Answer{Stdout: wsList})
		fake.On(script[1], fakebin.Answer{Stdout: tabCreated})
		fake.On(script[2], fakebin.Answer{})
		fake.On(script[3], fakebin.Answer{})
		fake.On(script[4], fakebin.Answer{Stdout: working})
		bin.Install()
		clk := &signalClock{Manual: clock.NewManual(start), waits: make(chan time.Duration)}
		f := &fixture{clock: clk, rt: herdr.New(exec.Runner{}, clk, logger, herdr.DefaultConfig())}

		h, waits, err := f.start(t, spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if diff := cmp.Diff(want, h); diff != "" {
			t.Errorf("handle (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{shellPause, check}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
		var got [][]string
		for _, c := range bin.Calls(herdr.Program) {
			got = append(got, c.Argv)
		}
		if diff := cmp.Diff(script, got); diff != "" {
			t.Errorf("herdr argv (-want +got):\n%s", diff)
		}
	})
}
