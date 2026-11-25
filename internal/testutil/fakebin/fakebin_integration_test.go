//go:build integration

package fakebin_test

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/testutil/fakebin"
)

func run(t *testing.T, dir string, argv ...string) (runtime.Result, error) {
	t.Helper()
	return exec.Runner{}.Run(context.Background(), runtime.Cmd{Argv: argv, Dir: dir})
}

func TestPathPointsAtTheFakes(t *testing.T) {
	b := fakebin.New(t)
	for _, name := range []string{"herdr", "gh", "claude", "git"} {
		b.Fake(name).On([]string{"--version"}, fakebin.Answer{Stdout: "fake " + name + "\n"})
	}
	b.Install()

	if got := os.Getenv("PATH"); got != b.Dir() {
		t.Fatalf("PATH = %q, want only the fake dir %q", got, b.Dir())
	}
	for _, name := range []string{"herdr", "gh", "claude", "git"} {
		got, err := osexec.LookPath(name)
		if err != nil || got != filepath.Join(b.Dir(), name) {
			t.Errorf("LookPath(%s) = %q, %v; want the fake in %s", name, got, err, b.Dir())
		}
		res, err := run(t, "", name, "--version")
		if err != nil || string(res.Stdout) != "fake "+name+"\n" {
			t.Errorf("%s --version = %q, %v; want the fake's answer", name, res.Stdout, err)
		}
	}
}

func TestAnswersInOrderAndLastRepeats(t *testing.T) {
	b := fakebin.New(t)
	b.Fake("herdr").On([]string{"agent", "get", "a"},
		fakebin.Answer{Stdout: "idle"}, fakebin.Answer{Stdout: "it's 'working'\n", Stderr: "warn", Exit: 3})
	b.Install()

	res, err := run(t, "", "herdr", "agent", "get", "a")
	if err != nil || string(res.Stdout) != "idle" {
		t.Fatalf("first = %q, %v", res.Stdout, err)
	}
	for range 2 {
		res, err = run(t, "", "herdr", "agent", "get", "a")
		var ee *runtime.ExitError
		if !errors.As(err, &ee) || ee.Code != 3 || string(res.Stdout) != "it's 'working'\n" || string(res.Stderr) != "warn" {
			t.Fatalf("later = %q %q, %v", res.Stdout, res.Stderr, err)
		}
	}
}

func TestPrefixMatchesLongerArgvOnly(t *testing.T) {
	b := fakebin.New(t)
	b.Fake("herdr").On([]string{"agent", "get", "a"}, fakebin.Answer{Stdout: "exact"}).
		OnPrefix([]string{"agent", "prompt", "a"}, fakebin.Answer{Stdout: "sent"})
	b.Install()

	for _, argv := range [][]string{{"agent", "prompt", "a"}, {"agent", "prompt", "a", "do 'x' * now\n"}} {
		res, err := run(t, "", append([]string{"herdr"}, argv...)...)
		if err != nil || string(res.Stdout) != "sent" {
			t.Errorf("herdr %q = %q, %v; want the prefix answer", argv, res.Stdout, err)
		}
	}
	if res, err := run(t, "", "herdr", "agent", "get", "a"); err != nil || string(res.Stdout) != "exact" {
		t.Errorf("exact rule = %q, %v", res.Stdout, err)
	}
	var ee *runtime.ExitError
	if _, err := run(t, "", "herdr", "agent", "prompted", "a"); !errors.As(err, &ee) || ee.Code != fakebin.ExitUnexpected {
		t.Errorf("argv that only shares a word = %v, want exit %d", err, fakebin.ExitUnexpected)
	}
	if err := os.Remove(filepath.Join(b.Dir(), "herdr.unexpected")); err != nil {
		t.Fatal(err)
	}
}

func TestLogsArgvAndDirIncludingAwkwardArgs(t *testing.T) {
	b := fakebin.New(t)
	awkward := []string{"agent", "prompt", "a", "line1\nline2 'q' \"d\" $x", ""}
	b.Fake("herdr").On(awkward)
	b.Install()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, dir, append([]string{"herdr"}, awkward...)...); err != nil {
		t.Fatal(err)
	}
	want := []fakebin.Call{{Argv: awkward, Dir: dir}}
	if diff := cmp.Diff(want, b.Calls("herdr")); diff != "" {
		t.Errorf("calls (-want +got):\n%s", diff)
	}
}

func TestUnscriptedArgvIsAnErrorOfTheFake(t *testing.T) {
	b := fakebin.New(t)
	b.Fake("gh").On([]string{"pr", "list"})
	b.Install()

	_, err := run(t, "", "gh", "pr", "merge")
	var ee *runtime.ExitError
	if !errors.As(err, &ee) || ee.Code != fakebin.ExitUnexpected || !strings.Contains(ee.StderrTail, "unscripted argv for gh: pr merge") {
		t.Fatalf("err = %v, want exit %d naming the argv", err, fakebin.ExitUnexpected)
	}
	if got := len(b.Calls("gh")); got != 1 {
		t.Errorf("calls = %d, want the unscripted call logged", got)
	}
	// The Cleanup check would fail this test; clear the marker after asserting it exists.
	if _, err := os.Stat(filepath.Join(b.Dir(), "gh.unexpected")); err != nil {
		t.Fatalf("no unexpected log: %v", err)
	}
	if err := os.Remove(filepath.Join(b.Dir(), "gh.unexpected")); err != nil {
		t.Fatal(err)
	}
}
