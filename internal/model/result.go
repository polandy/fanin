package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// FieldCap is the most characters a free-text result field keeps (see SCENARIOS AW-2).
const FieldCap = 400

// ErrInvalidResult is returned for a worker result that lacks the field its kind needs.
var ErrInvalidResult = errors.New("model: invalid worker result")

// ResultKind is the outcome a worker reports through fanin_report.
type ResultKind string

// The result kinds; the bash statuses DONE PR, PUSHED, COMMITTED, ISSUE, NOTHING and BLOCKED.
const (
	ResultDonePR    ResultKind = "done_pr"
	ResultPushed    ResultKind = "pushed"
	ResultCommitted ResultKind = "committed"
	ResultIssue     ResultKind = "issue"
	ResultNothing   ResultKind = "nothing"
	ResultBlocked   ResultKind = "blocked"
)

// AllResultKinds lists every ResultKind.
var AllResultKinds = []ResultKind{
	ResultDonePR, ResultPushed, ResultCommitted, ResultIssue, ResultNothing, ResultBlocked,
}

// ParseResultKind reads a reported result kind; an unknown value is ErrUnknownValue.
func ParseResultKind(s string) (ResultKind, error) { return parse("result kind", s, AllResultKinds) }

// Result is a worker's structured report.
type Result struct {
	Kind ResultKind
	// PR is the pull request number of done_pr.
	PR int
	// Branch is the branch of pushed and committed.
	Branch string
	// Issue is the issue number of issue.
	Issue int
	// IssueURL is the optional web address of Issue.
	IssueURL string
	// Text is the reason of nothing, the question of blocked, or a note on any kind.
	Text string
}

// Normalize returns r with Branch and Text cut to one line of at most FieldCap characters.
func (r Result) Normalize() Result {
	r.Branch = CapField(r.Branch)
	r.Text = CapField(r.Text)
	r.IssueURL = strings.TrimSpace(r.IssueURL)
	return r
}

// Validate checks that r carries the field its kind needs.
func (r Result) Validate() error {
	var missing string
	switch r.Kind {
	case ResultDonePR:
		if r.PR <= 0 {
			missing = "a PR number above 0"
		}
	case ResultPushed, ResultCommitted:
		if strings.TrimSpace(r.Branch) == "" {
			missing = "a branch"
		}
	case ResultIssue:
		if r.Issue <= 0 {
			missing = "an issue number above 0"
		}
	case ResultNothing, ResultBlocked:
		if strings.TrimSpace(r.Text) == "" {
			missing = "a text"
		}
	default:
		return fmt.Errorf("%w: kind %q; use one of %v", ErrInvalidResult, r.Kind, AllResultKinds)
	}
	if missing != "" {
		return fmt.Errorf("%w: kind %q needs %s", ErrInvalidResult, r.Kind, missing)
	}
	return validIssueURL(r.IssueURL)
}

// validIssueURL accepts an empty URL or an absolute http(s) URL of at most FieldCap characters; a URL is never cut.
func validIssueURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len([]rune(s)) > FieldCap ||
		strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("%w: issue URL %q; send an absolute http(s) URL of at most %d characters or none",
			ErrInvalidResult, s, FieldCap)
	}
	return nil
}

// CapField joins every whitespace run of s into one space, trims it and cuts it to FieldCap characters.
func CapField(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > FieldCap {
		s = string(r[:FieldCap])
	}
	return s
}
