// Command fanind is the fanin daemon: it loads the config, opens the store and serves the HTTP API until it is
// stopped (spec in docs/dev/spec/daemon.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/cleanup"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/metrics"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/report"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/runtime/opencode"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/shadow"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/internal/version"
	"github.com/polandy/fanin/internal/worktree"
)

// envConfig is the fallback of --config.
const envConfig = "FANIN_CONFIG"

// envShadow is the fallback of --shadow.
const envShadow = "FANIN_SHADOW"

// ShutdownGrace is how long open requests get to finish once fanind is stopped.
const ShutdownGrace = 10 * time.Second

// The start-up errors.
var (
	errUsage        = errors.New("fanind: usage")
	errNoConfig     = errors.New("fanind: no config")
	errNoOwnerToken = errors.New("fanind: no owner token")
	errListen       = errors.New("fanind: cannot listen")
	errSchedStopped = errors.New("fanind: scheduler stopped")
)

// deps are the seams of run; main passes the real ones.
type deps struct {
	getenv func(string) string
	logOut io.Writer
	clock  clock.Clock
	// ready is called with the bound address once the API accepts connections.
	ready func(addr string)
	// probe replaces the shadow importer's flock check when set.
	probe shadow.Probe
	// workers replaces the scheduler's worker runtime and picker when set.
	workers func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker)
	// runner replaces the runner of gh and git for the forges, and of the hooks, when set.
	runner runtime.Runner
}

// RunsDir is the directory next to the store that holds the per-run files (MCP config).
const RunsDir = "runs"

// StateDir is the directory next to the store where the post-merge hooks run and the pre-go checks keep their
// worktrees.
const StateDir = "state"

// OCShimDir is the directory next to the store that holds the opencode launcher shim.
const OCShimDir = "oc-bin"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], deps{getenv: os.Getenv, logOut: os.Stderr, clock: clock.Real{}, ready: func(string) {}})
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run serves until ctx ends or the scheduler stops on an error; a stop by ctx is a clean exit, never a task
// failure.
func run(ctx context.Context, args []string, d deps) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	err := serveDaemon(ctx, cancel, args, d)
	if cause := context.Cause(ctx); errors.Is(cause, errSchedStopped) {
		return cause
	}
	return err
}

// serveDaemon is run with the cancel that the scheduler calls with its error.
func serveDaemon(ctx context.Context, cancel context.CancelCauseFunc, args []string, d deps) error {
	fs := flag.NewFlagSet("fanind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", d.getenv(envConfig), "path to the config directory ($"+envConfig+")")
	shadowDir := fs.String("shadow", d.getenv(envShadow), "bash state directory to import ($"+envShadow+")")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w; usage: fanind [--config DIR] [--shadow DIR]", errUsage, err)
	}
	if *cfgPath == "" {
		return fmt.Errorf("%w: set --config or $%s to the config directory", errNoConfig, envConfig)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := cfg.Logger(d.logOut)
	token := d.getenv(cfg.OwnerTokenEnv)
	if token == "" {
		return fmt.Errorf("%w: $%s is empty; export the owner token there (owner_token_env in fanin.yaml of %s)",
			errNoOwnerToken, cfg.OwnerTokenEnv, *cfgPath)
	}
	st, err := store.Open(ctx, cfg.Store, d.clock)
	if err != nil {
		return fmt.Errorf("fanind: open the store %s: %w", cfg.Store, err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("fanind: close the store", "err", err)
		}
	}()
	host, err := os.Hostname()
	if err != nil {
		log.Warn("fanind: hostname unreadable; notes are prefixed with local", "err", err)
		host = config.HostLocal
	}
	notifier, err := notify.New(cfg.Notify, exec.Runner{}, host, log)
	if err != nil {
		return fmt.Errorf("fanind: %w; fix notify.kind in fanin.yaml of %s", err, *cfgPath)
	}
	svc := owner.New(st, owner.NoDeliverer{}, log)
	svc.SetNotifier(notifier)
	stopSched, err := startSched(ctx, cancel, cfg, st, notifier, svc, log, d)
	if err != nil {
		return fmt.Errorf("fanind: %w; fix the project files or forges.yaml of %s", err, *cfgPath)
	}
	defer stopSched()
	if *shadowDir != "" {
		stop, err := startShadow(ctx, *shadowDir, cfg, st, log, d)
		if err != nil {
			return err
		}
		defer stop()
	}
	in := intake.New(cfg, st)
	hooks, stopIntake, err := startIntake(ctx, cfg, st, in, log, d)
	if err != nil {
		return err
	}
	defer stopIntake()
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	h, err := api.New(api.Config{Source: st, OwnerToken: token, Clock: d.clock, Logger: log, Registry: reg,
		Projects: cfg.ProjectNames(), Owner: svc, Intake: in, Webhooks: hooks, Reports: report.New(st, log),
		Metrics: metrics.Handler(st, log)})
	if err != nil {
		return fmt.Errorf("fanind: %w", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("%w: listen %q: %w; set listen in fanin.yaml of %s to a free host:port", errListen, cfg.Listen, err, *cfgPath)
	}
	return serve(ctx, ln, h, log, d)
}

// fanin names the projects with loop: fanin.
func fanin(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	for _, p := range cfg.Projects {
		if p.Loop == config.LoopFanin {
			out[p.Name] = true
		}
	}
	return out
}

// startSched runs the scheduler and its owner delivery for loop: fanin projects; its error stops fanind (AW-9, OA-1).
func startSched(ctx context.Context, cancel context.CancelCauseFunc, cfg config.Config, st *store.Store,
	notifier notify.Notifier, svc *owner.Service, log *slog.Logger, d deps) (func(), error) {
	if len(fanin(cfg)) == 0 {
		return func() {}, nil
	}
	sd, err := schedDeps(cfg, st, notifier, log, d)
	if err != nil {
		return nil, err
	}
	svc.SetDeliverer(sched.OwnerDelivery{Store: st, Runtime: sd.Runtime})
	sd.Owner = svc
	s, err := sched.New(sd)
	if err != nil {
		return nil, err
	}
	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	safego.Go(loopCtx, log, func(c context.Context) {
		defer close(done)
		if err := s.Run(c); err != nil {
			cancel(fmt.Errorf("%w: %w; fix the store, then start fanind again", errSchedStopped, err))
		}
	})
	log.Info("fanind: scheduler running", "projects", s.Projects())
	return func() {
		stop()
		<-done
	}, nil
}

// schedDeps are the scheduler's collaborators; the forges of the loop: fanin projects read the PR gates and the
// base CI, and fanin's review posts fanin/review on a PR head with green CI.
func schedDeps(cfg config.Config, st *store.Store, notifier notify.Notifier, log *slog.Logger,
	d deps) (sched.Deps, error) {
	workers := d.workers
	if workers == nil {
		workers = func(cfg config.Config, log *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
			return realWorkers(cfg, d.clock, log)
		}
	}
	runner := d.runner
	if runner == nil {
		runner = exec.Runner{}
	}
	fg, err := newForges(cfg, runner, d.getenv, d.clock, log)
	if err != nil {
		return sched.Deps{}, err
	}
	state := filepath.Join(filepath.Dir(cfg.Store), StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return sched.Deps{}, fmt.Errorf("make the state directory %s: %w; make it writable or move the store", state, err)
	}
	rt, pk := workers(cfg, log)
	return sched.Deps{Config: cfg, Store: st, Runtime: rt, Picker: pk, Clock: d.clock, Log: log,
		Unpushed: sched.GitUnpushed{Runner: exec.Runner{}}, Notify: notifier, Gates: fg, BaseCI: fg, FailLog: fg, ReviewText: fg,
		Merger: fg, Cleaner: cleanup.New(runner, fg), Hook: hook.New(st, runner), PreGo: hook.NewPreGo(runner), StateDir: state,
		Worktrees: worktree.New(runner),
		Reviewer:  newReviews(fg, runner, d.clock, log, st),
		Scopes:    fg,
		Files:     sched.Files{Dir: filepath.Join(filepath.Dir(cfg.Store), RunsDir), DaemonURL: "http://" + cfg.Listen}}, nil
}

// realWorkers is the herdr runtime behind the opencode sandbox runtime, and the headless picker.
func realWorkers(cfg config.Config, clk clock.Clock, log *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
	oc := opencode.DefaultConfig()
	oc.ShimDir = filepath.Join(filepath.Dir(cfg.Store), OCShimDir)
	return opencode.New(herdr.New(exec.Runner{}, clk, log, herdr.DefaultConfig()), exec.Runner{}, oc),
		pick.New(headless.New(exec.Runner{}, clk, log), pick.Config{Model: cfg.Picker.Model})
}

// startShadow imports dir once, then keeps importing it until the returned stop, which waits for the loop; the
// projects with loop: fanin are skipped.
func startShadow(ctx context.Context, dir string, cfg config.Config, st *store.Store, log *slog.Logger,
	d deps) (func(), error) {
	im := shadow.New(dir, st, d.clock, log)
	im.Skip = fanin(cfg)
	if d.probe != nil {
		im.Probe = d.probe
	}
	if err := im.Import(ctx); err != nil {
		return nil, fmt.Errorf("fanind: first shadow import: %w; fix the state directory or drop --shadow", err)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	safego.Go(loopCtx, log, func(c context.Context) {
		defer close(done)
		im.Run(c)
	})
	log.Info("fanind: shadow import running", "dir", dir, "interval", shadow.Interval)
	return func() {
		cancel()
		<-done
	}, nil
}

// serve runs the API on ln until ctx ends, then cancels open requests (event streams) and waits for them.
func serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger, d deps) error {
	reqCtx, cancelReqs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelReqs()
	srv := &http.Server{Handler: h, BaseContext: func(net.Listener) context.Context { return reqCtx },
		ReadHeaderTimeout: ShutdownGrace}
	served := make(chan error, 1)
	safego.Go(ctx, log, func(context.Context) { served <- srv.Serve(ln) })
	log.Info("fanind: serving", "version", version.String("fanind"), "addr", ln.Addr().String())
	d.ready(ln.Addr().String())

	select {
	case err := <-served:
		return fmt.Errorf("fanind: serve: %w", err)
	case <-ctx.Done():
	}
	log.Info("fanind: stopping")
	cancelReqs()
	shutCtx, cancelShut := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelShut()
	safego.Go(shutCtx, log, func(c context.Context) {
		select {
		case <-d.clock.After(ShutdownGrace):
			cancelShut()
		case <-c.Done():
		}
	})
	if err := srv.Shutdown(shutCtx); err != nil {
		if cerr := srv.Close(); cerr != nil {
			log.Error("fanind: close the listener", "err", cerr)
		}
		return fmt.Errorf("fanind: shut down: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("fanind: serve: %w", err)
	}
	return nil
}
