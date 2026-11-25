// Package gitea is the forge.Forge for Gitea and Forgejo over their REST API.
package gitea

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/polandy/fanin/internal/forge"
)

// Footer ends the body of every PR OpenBranch opens.
const Footer = "Opened by fanin."

var errTODO = errors.New("not implemented")

// Client talks to one Gitea or Forgejo instance.
type Client struct{}

// New returns a Client.
func New(*http.Client, string, forge.Secret, *slog.Logger) (*Client, error) { return &Client{}, nil }

// PR reads one pull request.
func (c *Client) PR(context.Context, string, int) (forge.PR, error) { return forge.PR{}, errTODO }

// Files lists the paths the pull request changes.
func (c *Client) Files(context.Context, string, int) ([]string, error) { return nil, errTODO }

// ListOpen lists the open pull requests.
func (c *Client) ListOpen(context.Context, string) ([]forge.PR, error) { return nil, errTODO }

// Open opens a pull request.
func (c *Client) Open(context.Context, forge.OpenRequest) (int, error) { return 0, errTODO }

// OpenBranch opens or reuses the PR for a pushed branch.
func (c *Client) OpenBranch(context.Context, string, string, string) (int, bool, error) {
	return 0, false, errTODO
}

// EnableAutoMerge sets the merge intent.
func (c *Client) EnableAutoMerge(context.Context, forge.MergeRequest) error { return errTODO }

// PostStatus posts a commit status.
func (c *Client) PostStatus(context.Context, forge.Status) error { return errTODO }

// SquashMerge merges now.
func (c *Client) SquashMerge(context.Context, forge.MergeRequest) error { return errTODO }

// DeleteBranch deletes a branch.
func (c *Client) DeleteBranch(context.Context, string, string) error { return errTODO }
