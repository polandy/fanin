package review

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// The review comment's contract (SCENARIOS OC-5): a header line with CommentMark and a VerdictLabel verdict after it.
const (
	CommentMark  = "PR quality review"
	VerdictLabel = "Merge readiness"
)

// Verdict is the merge readiness a review gives.
type Verdict string

// The verdicts.
const (
	Ready    Verdict = "Ready"
	NotReady Verdict = "Not ready"
)

// unreadableError says why a review comment has no verdict; it matches ErrUnreadable.
type unreadableError struct{ reason string }

func (e unreadableError) Error() string { return "review: unreadable review: it " + e.reason }
func (e unreadableError) Unwrap() error { return ErrUnreadable }

var (
	errNoHeader  = unreadableError{reason: "does not start with ## " + CommentMark}
	errNoVerdict = unreadableError{reason: "has no merge readiness verdict after the header"}

	labelPattern    = regexp.MustCompile(`merge[- ]?readiness`)
	notPattern      = regexp.MustCompile(`(^|[^a-z0-9])not([^a-z0-9]|$)`)
	readyPattern    = regexp.MustCompile(`(^|[^a-z0-9])ready([^a-z0-9]|$)`)
	fencePattern    = regexp.MustCompile("^\\s*```")
	headerLeadChars = " \t>#*_`"
	headerTailChars = " \t*_`"
)

// Parse returns a comment's verdict and canonical text, or ErrUnreadable when it lacks the header or a verdict.
func Parse(body string) (Verdict, string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	header := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(bare(l)), strings.ToLower(CommentMark)) {
			header = i
		}
	}
	if header < 0 {
		return "", "", errNoHeader
	}
	verdictLine, verdict := -1, Verdict("")
	for i := header + 1; i < len(lines); i++ {
		lower := strings.ToLower(lines[i])
		loc := labelPattern.FindStringIndex(lower)
		if loc == nil {
			continue
		}
		v := verdictOf(lower[loc[1]:])
		for j := i + 1; v == "" && j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "" {
				v = verdictOf(strings.ToLower(lines[j]))
				break
			}
		}
		if v != "" {
			verdictLine, verdict = i, v
		}
	}
	if verdict == "" {
		return "", "", errNoVerdict
	}
	rest := strings.TrimRight(bare(lines[header])[len(CommentMark):], headerTailChars)
	out := []string{"## " + CommentMark + rest}
	last := verdictLine - 1
	for last > header && (strings.TrimSpace(lines[last]) == "" || fencePattern.MatchString(lines[last])) {
		last--
	}
	for _, l := range lines[header+1 : last+1] {
		if !fencePattern.MatchString(l) {
			out = append(out, l)
		}
	}
	out = append(out, "", VerdictLabel+": "+string(verdict))
	return verdict, strings.Join(out, "\n"), nil
}

func bare(s string) string { return strings.TrimLeft(s, headerLeadChars) }

func verdictOf(lower string) Verdict {
	switch {
	case notPattern.MatchString(lower):
		return NotReady
	case readyPattern.MatchString(lower):
		return Ready
	}
	return ""
}

// oneLine collapses whitespace and cuts s to limit characters.
// LatestText returns the canonical text, cut to TextLimit, of the newest comment that parses as a review.
func LatestText(comments []forge.Comment) (string, error) {
	for i := len(comments) - 1; i >= 0; i-- {
		if _, text, err := Parse(comments[i].Body); err == nil {
			return oneLine(text, TextLimit), nil
		}
	}
	return "", fmt.Errorf("%w: none of %d comments has the ## %s header and a verdict; the nudge uses the gate's reasons",
		ErrUnreadable, len(comments), CommentMark)
}

func oneLine(s string, limit int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}
