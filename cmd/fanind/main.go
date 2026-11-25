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
	"syscall"
	"time"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/internal/version"
)

// envConfig is the fallback of --config.
const envConfig = "FANIN_CONFIG"

// ShutdownGrace is how long open requests get to finish once fanind is stopped.
const ShutdownGrace = 10 * time.Second

// The start-up errors.
var (
	errUsage        = errors.New("fanind: usage")
	errNoConfig     = errors.New("fanind: no config")
	errNoOwnerToken = errors.New("fanind: no owner token")
	errListen       = errors.New("fanind: cannot listen")
)

// deps are the seams of run; main passes the real ones.
type deps struct {
	getenv func(string) string
	logOut io.Writer
	clock  clock.Clock
	// ready is called with the bound address once the API accepts connections.
	ready func(addr string)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], deps{getenv: os.Getenv, logOut: os.Stderr, clock: clock.Real{}, ready: func(string) {}})
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run serves until ctx ends; a stop by ctx is a clean exit, never a task failure.
func run(ctx context.Context, args []string, d deps) error {
	fs := flag.NewFlagSet("fanind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", d.getenv(envConfig), "path to the config directory ($"+envConfig+")")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w; usage: fanind [--config DIR]", errUsage, err)
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
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	h, err := api.New(api.Config{Source: st, OwnerToken: token, Clock: d.clock, Logger: log, Registry: reg,
		Projects: cfg.ProjectNames(), Owner: owner.New(st, owner.NoDeliverer{}, log)})
	if err != nil {
		return fmt.Errorf("fanind: %w", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("%w: listen %q: %w; set listen in fanin.yaml of %s to a free host:port", errListen, cfg.Listen, err, *cfgPath)
	}
	return serve(ctx, ln, h, log, d)
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
