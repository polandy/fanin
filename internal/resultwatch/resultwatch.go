// Package resultwatch watches a run directory for result.json, the report fallback of workers without MCP
// (concept C-REPORT; spec in docs/dev/spec/worker-contract.md).
package resultwatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// DefaultEvery is how often Watch looks for result.json.
const DefaultEvery = 5 * time.Second

// ErrUnreadable is a result.json that exists but cannot be read or parsed; it is never a result.
var ErrUnreadable = errors.New("resultwatch: unreadable result.json")

// Read returns the run directory's result.json; ok is false while it does not exist.
func Read(dir string) (faninmcp.ResultFile, bool, error) {
	path := filepath.Join(dir, faninmcp.ResultFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return faninmcp.ResultFile{}, false, nil
	}
	if err != nil {
		return faninmcp.ResultFile{}, false, fmt.Errorf("%w: %s: %w; check the run directory's permissions",
			ErrUnreadable, path, err)
	}
	f, err := faninmcp.ParseResultFile(data)
	if err != nil {
		return faninmcp.ResultFile{}, false, fmt.Errorf("%w: %s: %w", ErrUnreadable, path, err)
	}
	return f, true, nil
}

// Watch looks for result.json in dir now and then every every (DefaultEvery when not above 0) on clk, until it
// appears, cannot be read, or ctx ends.
func Watch(ctx context.Context, clk clock.Clock, dir string, every time.Duration) (faninmcp.ResultFile, error) {
	if every <= 0 {
		every = DefaultEvery
	}
	for {
		f, ok, err := Read(dir)
		if err != nil || ok {
			return f, err
		}
		select {
		case <-ctx.Done():
			return faninmcp.ResultFile{}, fmt.Errorf("resultwatch: watch %s: %w", dir, ctx.Err())
		case <-clk.After(every):
		}
	}
}
