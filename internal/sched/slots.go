package sched

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

// load is what a project's live runs and pickers hold.
type load struct {
	used map[int]bool
	// taken are the titles of the project's tasks that are no longer queued.
	taken []string
}

// load reads the slots of the project's live runs and adds the slots its pickers hold (concept C-SLOTS).
func (s *Scheduler) load(ctx context.Context, p config.Project) (load, error) {
	l := load{used: map[int]bool{}}
	tasks, err := s.d.Store.ProjectTasks(ctx, p.Name)
	if err != nil {
		return l, fmt.Errorf("sched: read the tasks of %s: %w", p.Name, err)
	}
	for _, t := range tasks {
		if t.State.Terminal() || t.State == model.TaskQueued {
			continue
		}
		l.taken = append(l.taken, t.Title)
		runs, err := s.d.Store.Runs(ctx, t.ID)
		if err != nil {
			return l, fmt.Errorf("sched: read the runs of %s: %w", t.ID, err)
		}
		if n := len(runs); n > 0 && !runs[n-1].State.Terminal() {
			l.used[runs[n-1].Slot] = true
		}
	}
	for _, r := range s.picking {
		if r.project == p.Name {
			l.used[r.slot] = true
		}
	}
	return l, nil
}

// free returns the lowest free slot from 1 to max, or 0 when all are used.
func (l load) free(max int) int {
	for slot := 1; slot <= max; slot++ {
		if !l.used[slot] {
			return slot
		}
	}
	return 0
}
