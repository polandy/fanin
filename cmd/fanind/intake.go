package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// startIntake builds the webhooks of triggers.yaml and runs the release queue and the cron entries until the
// returned stop, which waits for them; an unset webhook secret is an error.
func startIntake(ctx context.Context, cfg config.Config, st *store.Store, svc intake.Enqueuer, log *slog.Logger,
	d deps) (*intake.Webhooks, func(), error) {
	loopCtx, cancel := context.WithCancel(ctx)
	var loops []func(context.Context)
	var releases intake.Releaser
	if cfg.Triggers.Release != nil {
		q := intake.NewReleaseQueue(&intake.Deployer{Config: cfg, Probe: intake.GitProbe{Runner: exec.Runner{}},
			Workflows: github.New(exec.Runner{}, d.clock, log), Intake: svc, Clock: d.clock, Log: log},
			intake.ReleaseQueueSize)
		releases = q
		loops = append(loops, q.Run)
	}
	hooks, err := intake.NewWebhooks(cfg, d.getenv, svc, releases)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("fanind: %w; fix webhooks in triggers.yaml", err)
	}
	if len(cfg.Triggers.Cron) > 0 {
		c := &intake.Cron{Config: cfg, Store: st, Intake: svc, Clock: d.clock, Log: log}
		loops = append(loops, c.Run)
	}
	dones := make([]chan struct{}, 0, len(loops))
	for _, loop := range loops {
		done := make(chan struct{})
		dones = append(dones, done)
		safego.Go(loopCtx, log, func(c context.Context) {
			defer close(done)
			loop(c)
		})
	}
	log.Info("fanind: intake running", "webhooks", len(cfg.Triggers.Webhooks), "cron", len(cfg.Triggers.Cron),
		"release", cfg.Triggers.Release != nil)
	return hooks, func() {
		cancel()
		for _, done := range dones {
			<-done
		}
	}, nil
}
