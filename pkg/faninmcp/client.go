package faninmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody caps how much of an error answer the client reads.
const maxErrorBody = 64 << 10

// ErrConfig is a client built without the daemon URL, run ID or run token.
var ErrConfig = errors.New("faninmcp: incomplete client config")

// ReportPath is the daemon route that takes a run's Report.
func ReportPath(run string) string { return "/api/v1/runs/" + url.PathEscape(run) + "/report" }

// ProgressPath is the daemon route that takes a run's Progress.
func ProgressPath(run string) string { return "/api/v1/runs/" + url.PathEscape(run) + "/progress" }

// Error is a non-2xx answer of the daemon.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("fanind answered %d %s: %s", e.Status, e.Code, e.Message)
}

// Client sends one run's reports to the daemon with the run token.
type Client struct {
	base  string
	run   string
	token string
	http  *http.Client
}

// NewClient returns a client for run at baseURL; a nil hc uses http.DefaultClient. An empty argument is
// ErrConfig, so a worker never reports without its token.
func NewClient(baseURL, run, token string, hc *http.Client) (*Client, error) {
	for _, f := range []struct{ env, v string }{{EnvURL, baseURL}, {EnvRunID, run}, {EnvRunToken, token}} {
		if f.v == "" {
			return nil, fmt.Errorf("%w: %s is empty; fanind sets it when it starts the worker", ErrConfig, f.env)
		}
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), run: run, token: token, http: hc}, nil
}

// BaseURL returns the daemon URL the client calls.
func (c *Client) BaseURL() string { return c.base }

// Report sends the run's result.
func (c *Client) Report(ctx context.Context, r Report) error {
	return c.post(ctx, ReportPath(c.run), r)
}

// Progress sends a heartbeat.
func (c *Client) Progress(ctx context.Context, p Progress) error {
	return c.post(ctx, ProgressPath(c.run), p)
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("faninmcp: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("faninmcp: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("faninmcp: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		_, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			return fmt.Errorf("faninmcp: read the answer of POST %s: %w", path, err)
		}
		return nil
	}
	apiErr := &Error{Status: resp.StatusCode}
	var eb struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if readErr != nil || json.Unmarshal(raw, &eb) != nil {
		apiErr.Message = strings.TrimSpace(string(raw))
		return apiErr
	}
	apiErr.Code, apiErr.Message = eb.Error.Code, eb.Error.Message
	return apiErr
}
