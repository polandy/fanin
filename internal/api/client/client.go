// Package client is the Go client of the fanin HTTP API, used by the fanin CLI.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/api/wire"
)

// Error is a failed call answered with the API's JSON error format.
type Error struct {
	Status  int
	Code    string
	Message string
}

// Error describes the failed call.
func (e *Error) Error() string {
	return fmt.Sprintf("api: %d %s: %s", e.Status, e.Code, e.Message)
}

// Client talks to one fanind.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New returns a client for the API at baseURL. token is the owner token for command calls; hc may be nil.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), token: token, hc: hc}
}

// Health calls GET /api/v1/health.
func (c *Client) Health(ctx context.Context) (wire.Health, error) {
	var h wire.Health
	err := c.do(ctx, http.MethodGet, "/api/v1/health", nil, &h)
	return h, err
}

// Snapshot calls GET /api/v1/snapshot.
func (c *Client) Snapshot(ctx context.Context) (wire.Snapshot, error) {
	var s wire.Snapshot
	err := c.do(ctx, http.MethodGet, "/api/v1/snapshot", nil, &s)
	return s, err
}

// Post sends a command: body (nil for none) as JSON with the owner token, and decodes a 2xx answer into out
// (nil to ignore it). Strands build their typed calls on it.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("api: encode body of %s %s: %w", method, path, err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := c.request(ctx, method, path, rd)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("api: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("api: decode answer of %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("api: build %s %s: %w", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func readError(resp *http.Response) error {
	var b wire.ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil || b.Error.Code == "" {
		return &Error{Status: resp.StatusCode, Code: "unreadable",
			Message: "the answer has no JSON error body; check the URL points at fanind"}
	}
	return &Error{Status: resp.StatusCode, Code: b.Error.Code, Message: b.Error.Message}
}

// Events streams GET /api/v1/events after seq and calls fn for each event until ctx ends or fn fails. ready, when
// not nil, is called once the stream is open. A cancelled ctx returns its error.
func (c *Client) Events(ctx context.Context, after int64, ready func(), fn func(wire.Event) error) error {
	path := "/api/v1/events?after=" + url.QueryEscape(strconv.FormatInt(after, 10))
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("api: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}
	if ready != nil {
		ready()
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev wire.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("api: decode event %q: %w", data, err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			return ctxErr
		}
		return fmt.Errorf("api: read event stream: %w", err)
	}
	return ctx.Err()
}
