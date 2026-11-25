package intake

import (
	"context"
	"fmt"
)

// ReleaseQueueSize bounds the releases waiting for their deploy.
const ReleaseQueueSize = 16

// ReleaseQueue deploys submitted releases one after another; a release still queued at shutdown is lost and
// comes back with the sender's redelivery.
type ReleaseQueue struct {
	d  *Deployer
	ch chan ReleaseEvent
}

var _ Releaser = (*ReleaseQueue)(nil)

// NewReleaseQueue returns a queue of size releases for d.
func NewReleaseQueue(d *Deployer, size int) *ReleaseQueue {
	return &ReleaseQueue{d: d, ch: make(chan ReleaseEvent, size)}
}

// Submit queues ev; ErrBusy when the queue is full.
func (q *ReleaseQueue) Submit(ev ReleaseEvent) error {
	select {
	case q.ch <- ev:
		return nil
	default:
		return fmt.Errorf("%w: %d releases wait; redeliver %s of %s later", ErrBusy, cap(q.ch), ev.Version, ev.Repo)
	}
}

// Run deploys the queued releases until ctx ends; a failed deploy is logged and queues nothing.
func (q *ReleaseQueue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-q.ch:
			res, err := q.d.Deploy(ctx, ev)
			if err != nil {
				q.d.log().ErrorContext(ctx, "intake: release deploy failed", "project", ev.Project, "repo", ev.Repo,
					"version", ev.Version, "err", err)
				continue
			}
			q.d.log().InfoContext(ctx, "intake: release deployed", "project", ev.Project, "version", ev.Version,
				"outcome", res.Outcome)
		}
	}
}
