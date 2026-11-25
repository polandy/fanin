package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/polandy/fanin/internal/api/wire"
)

// FromNow is the Wait cursor that skips every event already logged.
const FromNow int64 = -1

// Answer calls POST /api/v1/tasks/{id}/answer with an option number or free text.
func (c *Client) Answer(ctx context.Context, task, answer string) (wire.Ack, error) {
	var ack wire.Ack
	err := c.Post(ctx, "/api/v1/tasks/"+url.PathEscape(task)+"/answer", wire.AnswerRequest{Answer: answer}, &ack)
	return ack, err
}

// Go calls POST /api/v1/tasks/{id}/go.
func (c *Client) Go(ctx context.Context, task string) (wire.Ack, error) {
	return c.command(ctx, "/api/v1/tasks/"+url.PathEscape(task)+"/go")
}

// LiftHold calls POST /api/v1/holds/{id}/lift.
func (c *Client) LiftHold(ctx context.Context, id string) (wire.Ack, error) {
	return c.command(ctx, "/api/v1/holds/"+url.PathEscape(id)+"/lift")
}

// Pause calls POST /api/v1/projects/{p}/pause.
func (c *Client) Pause(ctx context.Context, project string) (wire.Ack, error) {
	return c.command(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/pause")
}

// Resume calls POST /api/v1/projects/{p}/resume.
func (c *Client) Resume(ctx context.Context, project string) (wire.Ack, error) {
	return c.command(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/resume")
}

func (c *Client) command(ctx context.Context, path string) (wire.Ack, error) {
	var ack wire.Ack
	err := c.Post(ctx, path, nil, &ack)
	return ack, err
}

// Questions calls GET /api/v1/questions.
func (c *Client) Questions(ctx context.Context) (wire.Questions, error) {
	var qs wire.Questions
	err := c.do(ctx, http.MethodGet, "/api/v1/questions", nil, &qs)
	return qs, err
}

// Wait calls GET /api/v1/wait and returns the first event after the cursor (FromNow for only new ones), an
// owner-attention one when owner is set. It blocks until one is logged or ctx ends.
func (c *Client) Wait(ctx context.Context, owner bool, after int64) (wire.Event, error) {
	v := url.Values{}
	if owner {
		v.Set("owner", strconv.FormatBool(owner))
	}
	if after != FromNow {
		v.Set("after", strconv.FormatInt(after, 10))
	}
	path := "/api/v1/wait"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var ev wire.Event
	err := c.do(ctx, http.MethodGet, path, nil, &ev)
	return ev, err
}

// AddTask calls POST /api/v1/tasks.
func (c *Client) AddTask(ctx context.Context, req wire.TaskRequest) (wire.Task, error) {
	var t wire.Task
	err := c.Post(ctx, "/api/v1/tasks", req, &t)
	return t, err
}

// Tasks calls GET /api/v1/tasks for the open tasks of project, or of every project when it is empty.
func (c *Client) Tasks(ctx context.Context, project string) (wire.Tasks, error) {
	path := "/api/v1/tasks"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	var ts wire.Tasks
	err := c.do(ctx, http.MethodGet, path, nil, &ts)
	return ts, err
}
