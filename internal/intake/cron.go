package intake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// CronTimerPrefix starts the kind of a cron entry's timer row; the rest is the entry name.
const CronTimerPrefix = "cron:"

// CronRetry is the wait after a failed arm or fire before the next try.
const CronRetry = time.Minute

// Cron queues triggers.yaml's cron entries through timer rows, once per due date (see SCENARIOS IN-4).
type Cron struct {
	Config config.Config
	Store  *store.Store
	Intake Enqueuer
	Clock  clock.Clock
	// Log is nil for no logging.
	Log *slog.Logger
}

// CronTaskID names the task of an entry's due date, so a refire after a crash queues nothing more.
func CronTaskID(project, name string, due time.Time) model.TaskID {
	return model.TaskID(fmt.Sprintf("%s-cron-%s-%d", project, name, due.Unix()))
}

// Arm arms a timer one interval ahead for each entry without one and disarms those of removed entries.
func (c *Cron) Arm(ctx context.Context) error {
	armed, err := c.Store.ArmedTimers(ctx)
	if err != nil {
		return err
	}
	return c.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		have := map[string]bool{}
		for _, tm := range armed {
			name, ok := strings.CutPrefix(tm.Kind, CronTimerPrefix)
			if !ok {
				continue
			}
			if e, ok := c.Config.Triggers.Cron[name]; ok && e.Project == tm.Project && !have[name] {
				have[name] = true
				continue
			}
			if err := tx.DisarmTimer(ctx, tm.ID); err != nil {
				return err
			}
		}
		for name, e := range c.Config.Triggers.Cron {
			if have[name] {
				continue
			}
			due := c.Clock.Now().Add(time.Duration(e.Every))
			if _, err := tx.ArmTimer(ctx, store.Timer{Project: e.Project, Kind: CronTimerPrefix + name,
				DueAt: due}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Fire queues the task of every due entry and arms its next due date after now; missed dates fire once.
func (c *Cron) Fire(ctx context.Context) error {
	due, err := c.Store.DueTimers(ctx)
	if err != nil {
		return err
	}
	for _, tm := range due {
		name, ok := strings.CutPrefix(tm.Kind, CronTimerPrefix)
		if !ok {
			continue
		}
		e, ok := c.Config.Triggers.Cron[name]
		if ok {
			id := CronTaskID(e.Project, name, tm.DueAt)
			_, err := c.Intake.Enqueue(ctx, TaskRequest{ID: id, Project: e.Project, Task: e.Task, Analyze: e.Analyze,
				Model: e.Model})
			if err != nil && !errors.Is(err, ErrExists) {
				return fmt.Errorf("intake: cron %s: %w", name, err)
			}
			c.log().InfoContext(ctx, "intake: cron task queued", "task_id", id, "project", e.Project)
		}
		if err := c.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			if err := tx.DisarmTimer(ctx, tm.ID); err != nil {
				return err
			}
			if !ok {
				return nil
			}
			_, err := tx.ArmTimer(ctx, store.Timer{Project: e.Project, Kind: tm.Kind,
				DueAt: nextDue(tm.DueAt, time.Duration(e.Every), c.Clock.Now())})
			return err
		}); err != nil {
			return fmt.Errorf("intake: rearm cron %s: %w", name, err)
		}
	}
	return nil
}

// nextDue is the first due date after now on the grid of due and every.
func nextDue(due time.Time, every time.Duration, now time.Time) time.Time {
	next := due.Add(every)
	if !next.After(now) {
		next = next.Add(now.Sub(next).Truncate(every) + every)
	}
	return next
}

// Run arms the entries, then fires each due date until ctx ends; a failure is logged and retried.
func (c *Cron) Run(ctx context.Context) {
	armed := false
	for {
		var err error
		if !armed {
			err = c.Arm(ctx)
			armed = err == nil
		}
		if err == nil {
			err = c.Fire(ctx)
		}
		wait := CronRetry
		if err == nil {
			wait, err = c.untilNext(ctx)
		}
		if err != nil {
			c.log().ErrorContext(ctx, "intake: cron failed; retrying", "err", err, "retry", CronRetry)
			wait = CronRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-c.Clock.After(wait):
		}
	}
}

// untilNext is the wait until the earliest armed cron timer, CronRetry without one.
func (c *Cron) untilNext(ctx context.Context) (time.Duration, error) {
	armed, err := c.Store.ArmedTimers(ctx)
	if err != nil {
		return 0, err
	}
	for _, tm := range armed {
		if strings.HasPrefix(tm.Kind, CronTimerPrefix) {
			return max(tm.DueAt.Sub(c.Clock.Now()), 0), nil
		}
	}
	return CronRetry, nil
}

func (c *Cron) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}
