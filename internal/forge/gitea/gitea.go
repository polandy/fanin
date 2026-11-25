// Package gitea is the forge.Forge for Gitea and Forgejo over their REST API (/api/v1). The token comes from a
// named secret source as a forge.Secret and goes only into the Authorization header (see SCENARIOS GT-10).
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

const (
	// APIPath is the REST root below the forge URL.
	APIPath = "/api/v1"
	// PageLimit is the page size of every list call; Gitea caps it at 50 by default.
	PageLimit = 50
	// MaxPages bounds every paginated list; a longer list is an error, never a truncated answer.
	MaxPages = 20
	// MaxBody is the most bytes read from one answer.
	MaxBody = 8 << 20

	errTailBytes = 512
	contentJSON  = "application/json"
	authScheme   = "token "
)

// Client talks to one Gitea or Forgejo instance.
type Client struct {
	http  *http.Client
	base  string
	token forge.Secret
	log   *slog.Logger
}

var _ forge.Forge = (*Client)(nil)

// New returns a Client for the forge at baseURL (such as https://git.example.com); a nil logger logs nowhere.
func New(httpClient *http.Client, baseURL string, token forge.Secret, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: gitea url %q; set url to http(s)://host[/path] without credentials",
			forge.ErrInvalidRequest, forge.Redact(baseURL))
	}
	if token.Empty() {
		return nil, fmt.Errorf("%w: gitea %s has an empty token; set token_env to the variable that holds it",
			forge.ErrNoSecret, u.Host)
	}
	if httpClient == nil {
		return nil, fmt.Errorf("%w: gitea %s: no http client", forge.ErrInvalidRequest, u.Host)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: httpClient, base: strings.TrimRight(u.String(), "/") + APIPath, token: token, log: log}, nil
}

// call is one REST call: path is already escaped; body is JSON-encoded when set, and out is decoded when set.
type call struct {
	method string
	path   string
	query  url.Values
	body   any
	out    any
}

// do runs c and turns a failure into an error wrapping a forge sentinel where one fits, with the token scrubbed.
func (c *Client) do(ctx context.Context, cl call) error {
	op := cl.method + " " + cl.path
	err := c.send(ctx, op, cl)
	if err != nil && ctx.Err() == nil {
		c.log.DebugContext(ctx, "forge: gitea call failed", "op", op, "err", err)
	}
	return err
}

func (c *Client) send(ctx context.Context, op string, cl call) error {
	var body io.Reader
	if cl.body != nil {
		b, err := json.Marshal(cl.body)
		if err != nil {
			return fmt.Errorf("%w: %s: encode body: %w", forge.ErrInvalidRequest, op, err)
		}
		body = bytes.NewReader(b)
	}
	target := c.base + cl.path
	if len(cl.query) > 0 {
		target += "?" + cl.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, target, body)
	if err != nil {
		return fmt.Errorf("%w: %s: %s", forge.ErrInvalidRequest, op, c.token.Scrub(err.Error()))
	}
	req.Header.Set("Authorization", authScheme+c.token.Reveal())
	req.Header.Set("Accept", contentJSON)
	if cl.body != nil {
		req.Header.Set("Content-Type", contentJSON)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("forge: %s: %w", op, ctxErr)
		}
		// The transport error is not wrapped: it may quote the request.
		return fmt.Errorf("%w: %s: %s", forge.ErrNetwork, op, c.token.Scrub(err.Error()))
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.log.DebugContext(ctx, "forge: gitea close answer", "op", op, "err", c.token.Scrub(err.Error()))
		}
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return fmt.Errorf("%w: %s: read answer: %s", forge.ErrNetwork, op, c.token.Scrub(err.Error()))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(op, resp.StatusCode, data)
	}
	if len(data) > MaxBody {
		return fmt.Errorf("%w: %s: answer exceeds %d bytes", forge.ErrUnreadable, op, MaxBody)
	}
	if cl.out == nil {
		return nil
	}
	if err := decodeStrict(data, cl.out); err != nil {
		return fmt.Errorf("%w: %s: %s", forge.ErrUnreadable, op, c.token.Scrub(err.Error()))
	}
	return nil
}

func (c *Client) statusError(op string, code int, data []byte) error {
	tail := c.token.Scrub(strings.TrimSpace(string(data[:min(len(data), errTailBytes)])))
	switch code {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s: HTTP %d: %s", forge.ErrNotFound, op, code, tail)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s: HTTP %d: %s", forge.ErrNetwork, op, code, tail)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("forge: %s: HTTP %d: %s; check the token in the variable token_env names and its scopes",
			op, code, tail)
	default:
		return fmt.Errorf("forge: %s: HTTP %d: %s", op, code, tail)
	}
}

// decodeStrict decodes one JSON value and rejects trailing data; unknown fields are fine, Gitea sends many.
func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %q: %w", data[:min(len(data), errTailBytes)], err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// repoPath is /repos/{owner}/{name}; repo must be exactly "owner/name".
func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%w: repo %q; use owner/name", forge.ErrInvalidRequest, repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// refPath escapes each segment of a branch name and keeps its slashes, as Gitea's wildcard routes expect.
func refPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func pageQuery(page int, extra url.Values) url.Values {
	q := url.Values{}
	for k, v := range extra {
		q[k] = v
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("limit", strconv.Itoa(PageLimit))
	return q
}

// pages fetches path page by page until a page is short; more than MaxPages is an error.
func pages[T any](ctx context.Context, c *Client, path string, extra url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= MaxPages; page++ {
		var items []T
		if err := c.do(ctx, call{method: http.MethodGet, path: path, query: pageQuery(page, extra), out: &items}); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < PageLimit {
			return all, nil
		}
	}
	return nil, fmt.Errorf("%w: GET %s: more than %d pages of %d", forge.ErrUnreadable, path, MaxPages, PageLimit)
}
