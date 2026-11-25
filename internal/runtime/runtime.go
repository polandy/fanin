// Package runtime is the seam for every external process: callers depend on Runner, never on os/exec.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// StderrTailLimit is the most stderr bytes an ExitError keeps.
const StderrTailLimit = 2048

// ErrEmptyArgv is returned when a Cmd has no program to run.
var ErrEmptyArgv = errors.New("runtime: empty argv; set Cmd.Argv to the program and its arguments")

// Cmd is one process invocation; Argv[0] is the program, resolved on PATH.
type Cmd struct {
	Argv  []string
	Stdin []byte
	// Env entries (KEY=value) are added to the parent environment.
	Env []string
	Dir string
}

// Result is the captured output of a process that exited 0.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Runner runs an external process to completion.
type Runner interface {
	// Run starts cmd and waits. A non-zero exit is an *ExitError; ctx cancellation kills the process.
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// ExitError reports a process that ran and exited non-zero.
type ExitError struct {
	Argv       []string
	Code       int
	StderrTail string
}

// Error names the command, the exit code and the stderr tail.
func (e *ExitError) Error() string {
	return fmt.Sprintf("runtime: %s exited with code %d: %s", strings.Join(e.Argv, " "), e.Code, e.StderrTail)
}

// Tail returns the trailing StderrTailLimit bytes of stderr without the final newline.
func Tail(stderr []byte) string {
	if len(stderr) > StderrTailLimit {
		stderr = stderr[len(stderr)-StderrTailLimit:]
	}
	return strings.TrimRight(string(stderr), "\n")
}
