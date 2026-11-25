// Package exec is the only production code that starts real processes.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/polandy/fanin/internal/runtime"
)

// Runner runs commands with os/exec.
type Runner struct{}

// Run starts cmd, captures stdout and stderr and maps a non-zero exit to *runtime.ExitError.
func (Runner) Run(ctx context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	if len(cmd.Argv) == 0 {
		return runtime.Result{}, runtime.ErrEmptyArgv
	}
	c := osexec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	if cmd.Stdin != nil {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	res := func() runtime.Result { return runtime.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()} }
	if err := c.Run(); err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			return res(), &runtime.ExitError{Argv: cmd.Argv, Code: ee.ExitCode(), StderrTail: runtime.Tail(stderr.Bytes())}
		}
		return res(), fmt.Errorf("run %q: %w", cmd.Argv[0], err)
	}
	return res(), nil
}
