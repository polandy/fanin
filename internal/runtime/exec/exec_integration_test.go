//go:build integration

package exec_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/exec"
)

func TestRunCapturesOutputStdinEnvDir(t *testing.T) {
	dir := t.TempDir()
	res, err := exec.Runner{}.Run(context.Background(), runtime.Cmd{
		Argv:  []string{"sh", "-c", "cat; echo \"$FANIN_T\"; pwd; echo err >&2"},
		Stdin: []byte("in\n"),
		Env:   []string{"FANIN_T=v"},
		Dir:   dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(res.Stdout), "in\nv\n"+dir+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if string(res.Stderr) != "err\n" {
		t.Errorf("stderr = %q", res.Stderr)
	}
}

func TestRunExitErrorCarriesCodeAndStderrTail(t *testing.T) {
	_, err := exec.Runner{}.Run(context.Background(), runtime.Cmd{Argv: []string{"sh", "-c", "echo bad >&2; exit 7"}})
	var ee *runtime.ExitError
	if !errors.As(err, &ee) || ee.Code != 7 || !strings.Contains(ee.StderrTail, "bad") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStartFailureAndEmptyArgv(t *testing.T) {
	if _, err := (exec.Runner{}).Run(context.Background(), runtime.Cmd{Argv: []string{"/nonexistent/fanin-bin"}}); err == nil {
		t.Error("want start error")
	}
	if _, err := (exec.Runner{}).Run(context.Background(), runtime.Cmd{}); !errors.Is(err, runtime.ErrEmptyArgv) {
		t.Errorf("err = %v, want ErrEmptyArgv", err)
	}
}
