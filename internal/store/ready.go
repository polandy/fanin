package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/polandy/fanin/internal/model"
)

// Ready returns the queued tasks of a project whose After tasks are all done, oldest first. A task that is not
// done holds back only the tasks that name it (see SCENARIOS CH-2).
func (s *Store) Ready(ctx context.Context, project string) ([]model.Task, error) {
	tasks, err := s.ProjectTasks(ctx, project)
	if err != nil {
		return nil, err
	}
	done := map[model.TaskID]bool{}
	for _, t := range tasks {
		done[t.ID] = t.State == model.TaskDone
	}
	var ready []model.Task
	for _, t := range tasks {
		if t.State != model.TaskQueued {
			continue
		}
		free := true
		for _, dep := range t.After {
			free = free && done[dep]
		}
		if free {
			ready = append(ready, t)
		}
	}
	return ready, nil
}

// NextTaskID returns the first unused "<project>-<n>" ID, counting from the project's task count.
func (t *Tx) NextTaskID(ctx context.Context, project string) (model.TaskID, error) {
	var n int
	if err := t.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project = ?`, project).Scan(&n); err != nil {
		return "", fmt.Errorf("store: count tasks of project %q: %w", project, err)
	}
	for {
		n++
		id := model.TaskID(fmt.Sprintf("%s-%d", project, n))
		if _, err := readTask(ctx, t.tx, id); err != nil {
			if errors.Is(err, ErrNotFound) {
				return id, nil
			}
			return "", err
		}
	}
}
