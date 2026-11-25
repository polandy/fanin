// Package headless runs short, one-shot claude sessions (`claude -p`) for pickers and reviews.
package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
)

// Command is the claude binary, resolved on PATH.
const Command = "claude"

// ErrTimeout is returned when the process ran past Spec.Timeout and was killed.
var ErrTimeout = errors.New("headless: run timed out")

// Spec is one headless run.
type Spec struct {
	Model  string
	Prompt string
	// Dir is the working directory of the process.
	Dir string
	// AllowedTools are claude's tool rules; empty allows none beyond claude's defaults.
	AllowedTools []string
	Timeout      time.Duration
}

// Usage is the token count of one run.
type Usage struct {
	Input         int
	Output        int
	CacheCreation int
	CacheRead     int
}

// Total is every token of the run.
func (u Usage) Total() int { return u.Input + u.Output + u.CacheCreation + u.CacheRead }

// Result is the final answer of a run and what it cost.
type Result struct {
	Output string
	Usage  Usage
}

// Runtime starts headless runs through a runtime.Runner and bounds them with a Clock.
type Runtime struct {
	runner runtime.Runner
	clock  clock.Clock
	logger *slog.Logger
}

// New returns a Runtime.
func New(r runtime.Runner, c clock.Clock, logger *slog.Logger) *Runtime {
	return &Runtime{runner: r, clock: c, logger: logger}
}

// claudeAnswer is the part of `--output-format json` fanin reads.
type claudeAnswer struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		Input         int `json:"input_tokens"`
		Output        int `json:"output_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (s Spec) validate() error {
	switch {
	case s.Model == "":
		return fmt.Errorf("%w: headless model is empty; set the worker kind's picker model", runtime.ErrInvalidSpec)
	case s.Prompt == "":
		return fmt.Errorf("%w: headless prompt is empty; build the prompt before the run", runtime.ErrInvalidSpec)
	case s.Timeout <= 0:
		return fmt.Errorf("%w: headless timeout is %s; set a positive timeout", runtime.ErrInvalidSpec, s.Timeout)
	}
	return nil
}

func (s Spec) argv() []string {
	argv := []string{Command, "-p", "--output-format", "json", "--model", s.Model}
	if len(s.AllowedTools) > 0 {
		argv = append(argv, "--allowedTools", strings.Join(s.AllowedTools, ","))
	}
	return argv
}

// Run starts claude with the prompt on stdin and waits for its answer; a run past Timeout is killed (ErrTimeout).
func (r *Runtime) Run(ctx context.Context, spec Spec) (Result, error) {
	if err := spec.validate(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	deadline := r.clock.After(spec.Timeout)
	safego.Go(ctx, r.logger, func(ctx context.Context) {
		select {
		case <-deadline:
			timedOut.Store(true)
			cancel()
		case <-ctx.Done():
		}
	})
	res, err := r.runner.Run(ctx, runtime.Cmd{Argv: spec.argv(), Stdin: []byte(spec.Prompt), Dir: spec.Dir})
	if timedOut.Load() {
		return Result{}, fmt.Errorf("%w after %s: output tail: %s", ErrTimeout, spec.Timeout, runtime.Tail(res.Stdout))
	}
	if err != nil {
		return Result{}, fmt.Errorf("headless %s run in %s: %w", spec.Model, spec.Dir, err)
	}
	return parse(res.Stdout)
}

func parse(stdout []byte) (Result, error) {
	var a claudeAnswer
	if err := json.Unmarshal(stdout, &a); err != nil {
		return Result{}, fmt.Errorf("%w: claude -p answered no JSON (%w): %s", runtime.ErrOddAnswer, err, runtime.Tail(stdout))
	}
	if a.IsError || a.Result == "" {
		return Result{}, fmt.Errorf("%w: claude -p reported an error or an empty result: %s", runtime.ErrOddAnswer, runtime.Tail(stdout))
	}
	u := a.Usage
	return Result{Output: a.Result, Usage: Usage{Input: u.Input, Output: u.Output, CacheCreation: u.CacheCreation, CacheRead: u.CacheRead}}, nil
}
