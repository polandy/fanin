// Package faninmcp is fanin's worker contract: report types, result.json, client and MCP tools (worker-contract.md).
package faninmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Kind is the outcome a worker reports.
type Kind string

// The report kinds.
const (
	KindDonePR    Kind = "done_pr"
	KindPushed    Kind = "pushed"
	KindCommitted Kind = "committed"
	KindIssue     Kind = "issue"
	KindNothing   Kind = "nothing"
	KindBlocked   Kind = "blocked"
)

// Kinds lists every Kind.
var Kinds = []Kind{KindDonePR, KindPushed, KindCommitted, KindIssue, KindNothing, KindBlocked}

// TextCap is how many characters of Branch and Text the daemon keeps, joined into one line (see SCENARIOS AW-2).
const TextCap = 400

// Environment the daemon sets for a worker's MCP server.
const (
	EnvURL      = "FANIN_URL"
	EnvRunID    = "FANIN_RUN_ID"
	EnvRunToken = "FANIN_RUN_TOKEN"
)

// ResultFileName is the fallback report in the run directory, for workers without MCP.
const ResultFileName = "result.json"

// ErrUnreadable is a result.json that is not one JSON object of the ResultFile shape.
var ErrUnreadable = errors.New("faninmcp: unreadable result file")

// Report is a worker's result; each kind needs its one field (see the Kind constants and NewServer).
type Report struct {
	Kind     Kind   `json:"kind" jsonschema:"the outcome: done_pr, pushed, committed, issue, nothing or blocked"`
	PR       int    `json:"pr,omitempty" jsonschema:"pull request number; required for done_pr"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch name; required for pushed and committed"`
	Issue    int    `json:"issue,omitempty" jsonschema:"issue number; required for issue"`
	IssueURL string `json:"issue_url,omitempty" jsonschema:"web address of the issue, absolute http(s)"`
	Text     string `json:"text,omitempty" jsonschema:"reason for nothing, the question for blocked, or a note; one line, at most 400 characters are kept"`
}

// Progress is a worker's heartbeat with an optional note.
type Progress struct {
	Text string `json:"text,omitempty" jsonschema:"what the worker is doing now; one line, at most 400 characters are kept"`
}

// ResultFile is the content of result.json: the report plus the run token, since no request carries it.
type ResultFile struct {
	Token string `json:"token" jsonschema:"the run token from $FANIN_RUN_TOKEN"`
	Report
}

// ParseResultFile reads one result.json; anything but one known-field object with token and kind is ErrUnreadable.
func ParseResultFile(data []byte) (ResultFile, error) {
	var f ResultFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return ResultFile{}, fmt.Errorf("%w: %w; write one JSON object with token, kind and the kind's field",
			ErrUnreadable, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ResultFile{}, fmt.Errorf("%w: data after the JSON object; write exactly one object", ErrUnreadable)
	}
	if f.Token == "" {
		return ResultFile{}, fmt.Errorf("%w: no token; copy $%s into the token field", ErrUnreadable, EnvRunToken)
	}
	if f.Kind == "" {
		return ResultFile{}, fmt.Errorf("%w: no kind; set kind to one of %v", ErrUnreadable, Kinds)
	}
	return f, nil
}
