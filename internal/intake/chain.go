package intake

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/polandy/fanin/internal/model"
)

const (
	chainSeparator = "|"
	chainComment   = "#"
)

// ChainRequests reads a chain file (one "model|task" line per task) into requests. Comment and blank lines are
// skipped but counted: a task's ID is "<project>-<file name>-<line>", so an edited file re-imports onto the same
// IDs. Each request waits for the one before it (see SCENARIOS CH-1).
func ChainRequests(project, file string, r io.Reader) ([]TaskRequest, error) {
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	var (
		out  []TaskRequest
		prev model.TaskID
	)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, chainComment) {
			continue
		}
		mdl, text, ok := strings.Cut(line, chainSeparator)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, invalidf("%s line %d: %q is not 'model%stask'", file, n, line, chainSeparator)
		}
		req := TaskRequest{
			ID: model.TaskID(fmt.Sprintf("%s-%s-%d", project, name, n)), Project: project,
			Task: strings.TrimSpace(text), Model: strings.TrimSpace(mdl),
		}
		if prev != "" {
			req.After = []model.TaskID{prev}
		}
		prev = req.ID
		out = append(out, req)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("intake: read chain file %s: %w", file, err)
	}
	return out, nil
}

// Enqueuer queues one request; ErrExists means it was queued before.
type Enqueuer interface {
	Enqueue(ctx context.Context, r TaskRequest) (model.Task, error)
}

// Import queues reqs in order and skips those already queued. It stops at the first other error.
func Import(ctx context.Context, e Enqueuer, reqs []TaskRequest) (added, skipped int, err error) {
	for _, r := range reqs {
		switch _, err := e.Enqueue(ctx, r); {
		case err == nil:
			added++
		case errors.Is(err, ErrExists):
			skipped++
		default:
			return added, skipped, fmt.Errorf("import task %s: %w", r.ID, err)
		}
	}
	return added, skipped, nil
}
