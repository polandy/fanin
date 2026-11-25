// Package safego starts goroutines that cannot take the process down with a panic.
package safego

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine; a panic is logged at error level with its stack and swallowed.
func Go(ctx context.Context, logger *slog.Logger, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "goroutine panic recovered", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn(ctx)
	}()
}
