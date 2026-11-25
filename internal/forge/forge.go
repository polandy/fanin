// Package forge is the seam to a code forge: fanin opens PRs, sets the merge intent, posts statuses and watches;
// the forge merges (concept D4). An unreadable forge answer is an error, never a verdict.
package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The sentinels every forge client wraps; match them with errors.Is.
var (
	// ErrNetwork is a transport failure: the call may succeed when retried.
	ErrNetwork = errors.New("forge: network failure")
	// ErrNotFound is a repo, PR or branch the forge does not know.
	ErrNotFound = errors.New("forge: not found")
	// ErrUnreadable is an answer that cannot be parsed or misses a required field.
	ErrUnreadable = errors.New("forge: unreadable answer")
	// ErrChecksUnreadable is a readable PR whose checks or statuses cannot be read; a gate reads it as pending.
	ErrChecksUnreadable = errors.New("forge: checks unreadable")
	// ErrNotOpen is a merge of a PR that is closed without being merged.
	ErrNotOpen = errors.New("forge: PR is not open")
	// ErrInvalidRequest is a request fanin must not send, such as a merge without a head branch.
	ErrInvalidRequest = errors.New("forge: invalid request")
)

// PRState is the lifecycle state of a pull request.
type PRState string

// The PR states.
const (
	StateOpen   PRState = "OPEN"
	StateClosed PRState = "CLOSED"
	StateMerged PRState = "MERGED"
)

// Mergeable says whether the forge can merge the PR head into its base.
type Mergeable string

// The mergeable answers; MergeableUnknown means the forge has not computed it yet.
const (
	MergeableYes        Mergeable = "MERGEABLE"
	MergeableConflicted Mergeable = "CONFLICTING"
	MergeableUnknown    Mergeable = "UNKNOWN"
)

// CheckState is a check's state, normalised across check runs and commit statuses.
type CheckState string

// The check states.
const (
	CheckPending   CheckState = "pending"
	CheckSuccess   CheckState = "success"
	CheckFailure   CheckState = "failure"
	CheckCancelled CheckState = "cancelled"
	CheckSkipped   CheckState = "skipped"
)

// Check is one check run or commit status on the PR head.
type Check struct {
	Name  string
	State CheckState
	// UpdatedAt is when the check finished, or started while it is pending; zero only for a queued check run.
	UpdatedAt time.Time
}

// PR is a pull request as the forge reports it.
type PR struct {
	Repo       string
	Number     int
	Title      string
	URL        string
	State      PRState
	Mergeable  Mergeable
	HeadBranch string
	HeadSHA    string
	BaseBranch string
	// Checks is filled by Forge.PR only; ListOpen leaves it nil.
	Checks []Check
}

// StatusState is the state of a commit status fanin posts.
type StatusState string

// The commit status states.
const (
	StatusPending StatusState = "pending"
	StatusSuccess StatusState = "success"
	StatusFailure StatusState = "failure"
	StatusError   StatusState = "error"
)

// AllStatusStates lists every StatusState.
var AllStatusStates = []StatusState{StatusPending, StatusSuccess, StatusFailure, StatusError}

// Status is a commit status on one SHA, such as the review verdict (concept D5).
type Status struct {
	Repo        string
	SHA         string
	Context     string
	State       StatusState
	Description string
	TargetURL   string
}

// OpenRequest opens a pull request from Head into Base.
type OpenRequest struct {
	Repo  string
	Head  string
	Base  string
	Title string
	Body  string
}

// MergeRequest is a squash merge, or the auto-merge intent for one; build it with NewSquash.
type MergeRequest struct {
	Repo    string
	Number  int
	Subject string
	Body    string
	// HeadSHA is the head the merge is for; the forge refuses the merge when the head moved.
	HeadSHA string
}

// Trailer is one "Key: Value" line of a squash body.
type Trailer struct {
	Key   string
	Value string
}

// Forge is what fanin needs from a code forge. Repo is "owner/name".
type Forge interface {
	// PR reads one pull request with its head and checks.
	PR(ctx context.Context, repo string, number int) (PR, error)
	// Files lists the paths the pull request changes.
	Files(ctx context.Context, repo string, number int) ([]string, error)
	// ListOpen lists the open pull requests without their checks.
	ListOpen(ctx context.Context, repo string) ([]PR, error)
	// Open opens a pull request and returns its number.
	Open(ctx context.Context, req OpenRequest) (int, error)
	// EnableAutoMerge sets the merge intent: the forge squash-merges once its branch protection is green.
	EnableAutoMerge(ctx context.Context, req MergeRequest) error
	// PostStatus posts a commit status.
	PostStatus(ctx context.Context, st Status) error
	// SquashMerge merges now; a PR that is already merged is success (see SCENARIOS MG-6).
	SquashMerge(ctx context.Context, req MergeRequest) error
	// DeleteBranch deletes a branch of the repo.
	DeleteBranch(ctx context.Context, repo, branch string) error
}

// SquashSubject is the squash subject: the PR title followed by " (#n)" (see SCENARIOS MG-6).
func SquashSubject(title string, number int) string { return fmt.Sprintf("%s (#%d)", title, number) }

// NewSquash builds the squash merge of pr: subject from SquashSubject, body of trailers only. A PR without a
// title, head branch or head SHA is ErrInvalidRequest, so a bad view never reaches the merge.
func NewSquash(pr PR, trailers []Trailer) (MergeRequest, error) {
	switch {
	case pr.Repo == "" || pr.Number <= 0:
		return MergeRequest{}, fmt.Errorf("%w: PR repo %q number %d; read the PR from the forge first", ErrInvalidRequest, pr.Repo, pr.Number)
	case strings.TrimSpace(pr.Title) == "":
		return MergeRequest{}, fmt.Errorf("%w: PR %s#%d has an empty title; give the PR a title", ErrInvalidRequest, pr.Repo, pr.Number)
	case pr.HeadBranch == "" || pr.HeadSHA == "":
		return MergeRequest{}, fmt.Errorf("%w: PR %s#%d has head branch %q sha %q; re-read the PR", ErrInvalidRequest, pr.Repo, pr.Number, pr.HeadBranch, pr.HeadSHA)
	}
	lines := make([]string, 0, len(trailers))
	for _, t := range trailers {
		if t.Key == "" || strings.ContainsAny(t.Key, ": \n") || strings.Contains(t.Value, "\n") {
			return MergeRequest{}, fmt.Errorf("%w: trailer %q: %q; use one line \"Key: Value\" with a key without spaces or colons", ErrInvalidRequest, t.Key, t.Value)
		}
		lines = append(lines, t.Key+": "+t.Value)
	}
	return MergeRequest{
		Repo:    pr.Repo,
		Number:  pr.Number,
		Subject: SquashSubject(pr.Title, pr.Number),
		Body:    strings.Join(lines, "\n"),
		HeadSHA: pr.HeadSHA,
	}, nil
}
