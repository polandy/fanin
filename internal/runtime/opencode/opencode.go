// Package opencode is the WorkerRuntime adapter for opencode workers in the sandbox (concept D2, SCENARIOS SP-9): it
// readies the sandbox clone and the launcher shim, then lets the herdr adapter start the agent.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/polandy/fanin/internal/atomicfile"
	"github.com/polandy/fanin/internal/runtime"
)

// The sandbox defaults; the shim directory has none and must be set.
const (
	DefaultUser     = "aiagent"
	DefaultSrv      = "/srv/aiagent"
	DefaultLauncher = "/usr/local/bin/oc"
	// ShimName is the program herdr starts for --kind opencode; the shim hands it to the launcher.
	ShimName = "opencode"
	// StatusLines is how many lines of a dirty status the error quotes.
	StatusLines = 5
)

// SyncScript runs as the sandbox user: it reads a bundle of origin/<base> on stdin into the clone at $1; $2 is the
// base. The bundle file lives beside the clone, never in it.
const SyncScript = `umask 077; cd "$1" || exit 1; b=../.fanin-sync-$$.bundle; cat >"$b" && ` +
	`git fetch -q "$b" "+refs/remotes/origin/$2:refs/remotes/origin/$2"; s=$?; rm -f "$b"; exit $s`

const (
	shimMode    = 0o755
	shimDirMode = 0o700
	sudoMarker  = "a password is required"
)

var sudoBase = []string{"sudo", "-n", "-u"}

// ErrDirtyClone is a sandbox clone with changed or untracked files; the spawn stops before herdr.
var ErrDirtyClone = errors.New("opencode: sandbox clone is not clean")

// Config names the sandbox user, the clones' parent directory, the launcher and the shim directory.
type Config struct {
	User     string
	Srv      string
	Launcher string
	ShimDir  string
}

// DefaultConfig returns the sandbox defaults without a shim directory.
func DefaultConfig() Config {
	return Config{User: DefaultUser, Srv: DefaultSrv, Launcher: DefaultLauncher}
}

// Runtime starts opencode specs in the sandbox and hands every other call to the inner runtime.
type Runtime struct {
	runtime.WorkerRuntime
	runner runtime.Runner
	cfg    Config
}

var _ runtime.WorkerRuntime = (*Runtime)(nil)

// New wraps inner, which starts the agents; runner runs git and sudo.
func New(inner runtime.WorkerRuntime, runner runtime.Runner, cfg Config) *Runtime {
	return &Runtime{WorkerRuntime: inner, runner: runner, cfg: cfg}
}

// Start passes a claude spec through; an opencode spec gets a clean, synced clone and the shim on PATH first.
func (r *Runtime) Start(ctx context.Context, spec runtime.RunSpec) (runtime.RunHandle, error) {
	switch spec.Kind {
	case "", runtime.KindClaude:
		return r.WorkerRuntime.Start(ctx, spec)
	case runtime.KindOpencode:
	default:
		return runtime.RunHandle{}, fmt.Errorf("%w: RunSpec.Kind %q for agent %q; use %s or %s", runtime.ErrInvalidSpec,
			spec.Kind, spec.Agent, runtime.KindClaude, runtime.KindOpencode)
	}
	if err := r.validate(spec); err != nil {
		return runtime.RunHandle{}, err
	}
	clone := filepath.Join(r.cfg.Srv, spec.Project)
	if err := r.clean(ctx, clone); err != nil {
		return runtime.RunHandle{}, err
	}
	if err := r.sync(ctx, spec, clone); err != nil {
		return runtime.RunHandle{}, err
	}
	if err := r.writeShim(); err != nil {
		return runtime.RunHandle{}, err
	}
	spec.PathDir = r.cfg.ShimDir
	return r.WorkerRuntime.Start(ctx, spec)
}

func (r *Runtime) validate(s runtime.RunSpec) error {
	fields := []struct{ key, value string }{
		{"RunSpec.Project", s.Project}, {"RunSpec.Worktree", s.Worktree}, {"RunSpec.Base", s.Base},
		{"Config.User", r.cfg.User}, {"Config.Srv", r.cfg.Srv}, {"Config.Launcher", r.cfg.Launcher},
		{"Config.ShimDir", r.cfg.ShimDir},
	}
	for _, f := range fields {
		if f.value == "" || strings.HasPrefix(f.value, "-") {
			return fmt.Errorf("%w: opencode %s is %q for agent %q; set it to a name or path", runtime.ErrInvalidSpec,
				f.key, f.value, s.Agent)
		}
	}
	return nil
}

// clean stops on any changed or untracked file: leftovers may be a worker's uncommitted work.
func (r *Runtime) clean(ctx context.Context, clone string) error {
	res, err := r.sudo(ctx, nil, "git", "-C", clone, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("opencode: read the status of %s: %w", clone, err)
	}
	out := strings.TrimRight(string(res.Stdout), "\n")
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	return fmt.Errorf("%w: %s is not clean: %s (hint: move the leftovers out of the clone; never delete or git "+
		"clean them, they may be uncommitted work)", ErrDirtyClone, clone,
		strings.Join(lines[:min(StatusLines, len(lines))], "; "))
}

// sync fetches origin/<base> here and hands it to the clone as a bundle, since the sandbox has no network.
func (r *Runtime) sync(ctx context.Context, spec runtime.RunSpec, clone string) error {
	git := []string{"git", "-C", spec.Worktree}
	if _, err := r.runner.Run(ctx, runtime.Cmd{Argv: append(git, "fetch", "-q", "origin", spec.Base)}); err != nil {
		return fmt.Errorf("opencode: fetch origin %s in %s: %w", spec.Base, spec.Worktree, err)
	}
	ref := "refs/remotes/origin/" + spec.Base
	bundle, err := r.runner.Run(ctx, runtime.Cmd{Argv: append(git, "bundle", "create", "-q", "-", ref)})
	if err != nil {
		return fmt.Errorf("opencode: bundle %s in %s: %w", ref, spec.Worktree, err)
	}
	if _, err := r.sudo(ctx, bundle.Stdout, "sh", "-c", SyncScript, "sh", clone, spec.Base); err != nil {
		return fmt.Errorf("opencode: sync %s to origin/%s: %w", clone, spec.Base, err)
	}
	return nil
}

func (r *Runtime) writeShim() error {
	if err := os.MkdirAll(r.cfg.ShimDir, shimDirMode); err != nil {
		return fmt.Errorf("opencode: create the shim directory %s: %w", r.cfg.ShimDir, err)
	}
	body := fmt.Sprintf("#!/bin/sh\nexec %s \"$@\"\n", r.cfg.Launcher)
	if err := atomicfile.Write(filepath.Join(r.cfg.ShimDir, ShimName), []byte(body), shimMode); err != nil {
		return fmt.Errorf("opencode: write the shim: %w", err)
	}
	return nil
}

// sudo runs argv as the sandbox user; a password prompt names the missing sudoers line.
func (r *Runtime) sudo(ctx context.Context, stdin []byte, argv ...string) (runtime.Result, error) {
	full := append(append(append([]string{}, sudoBase...), r.cfg.User), argv...)
	res, err := r.runner.Run(ctx, runtime.Cmd{Argv: full, Stdin: stdin})
	var exit *runtime.ExitError
	if errors.As(err, &exit) && strings.Contains(exit.StderrTail, sudoMarker) {
		return res, fmt.Errorf("sudo -u %s asked for a password; add a NOPASSWD sudoers line for %s: %w",
			r.cfg.User, argv[0], err)
	}
	return res, err
}
