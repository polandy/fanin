// Package cli holds the exit codes and output helpers shared by the fanin subcommands.
package cli

import (
	"errors"
	"fmt"
	"io"
)

// Exit codes of the fanin binary.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// ErrUsage marks a usage error: bad flag, unknown subcommand or wrong arguments.
var ErrUsage = errors.New("usage error")

// Usagef returns an error wrapping ErrUsage with a formatted message.
func Usagef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}

// ExitCode maps an error from a subcommand to the process exit code.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrUsage):
		return ExitUsage
	default:
		return ExitError
	}
}

// Output holds the standard streams of a command; In is nil for a command that reads no input.
type Output struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Println writes a line to standard output.
func (o Output) Println(a ...any) {
	_, _ = fmt.Fprintln(o.Out, a...)
}

// Printf writes formatted text to standard output.
func (o Output) Printf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.Out, format, a...)
}

// Errorf writes one "fanin: ..." line to standard error.
func (o Output) Errorf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.Err, "fanin: "+format+"\n", a...)
}
