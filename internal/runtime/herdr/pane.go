package herdr

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Pane text is read only to spot dialogs, stalls and live background work, never for a result (concept C-REPORT).

// The pane regions the observer looks at.
const (
	// DialogLines is how many bottom lines are searched for a dialog.
	DialogLines = 30
	// FooterLines is how many bottom lines hold claude's live footer (see SCENARIOS AW-3).
	FooterLines = 4
	// LastWordsLines is how many lines above the input box are a worker's last words (see SCENARIOS AW-12).
	LastWordsLines = 5
	// DialogLimit caps a dialog's text in runes (see SCENARIOS AW-2).
	DialogLimit = 400
)

// The claude screen marks the observer reads.
const (
	inputMark     = "❯"
	inputHint     = `Try "`
	statusLine    = "esc to interrupt"
	spinnerGlyphs = "✻✽✶✳✢·*⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	frameChars    = " │┃"
	shellWord     = "shell"
	optionSep     = " / "
	ellipsis      = "…"
)

var (
	// optionLine is a numbered option; group 1 is the cursor, 2 the number, 3 the text.
	optionLine = regexp.MustCompile(`^(❯\s*)?(\d+)\.\s+(\S.*)$`)
	// bgFooter is claude's count of live background work below the input box.
	bgFooter = regexp.MustCompile(`\b\d+ (?:background (?:tasks?|agents?|subagents?|shells?)|shells?)\b`)
	// volatile is the timers and token counts that change while nothing else does.
	volatile = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:[smh]|k? tokens)\b`)
)

// footerKind says what live background work the footer shows.
type footerKind int

const (
	footerNone footerKind = iota
	footerAgent
	footerShell
)

// lines splits text into lines without trailing blanks or trailing blank lines.
func lines(text string) []string {
	ls := strings.Split(text, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	for len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

func bottom(ls []string, n int) []string { return ls[max(0, len(ls)-n):] }

// unframe drops the box frame and the indent around a line.
func unframe(l string) string { return strings.Trim(l, frameChars) }

func isBorder(l string) bool {
	l = strings.TrimSpace(l)
	if l == "" {
		return false
	}
	for _, r := range l {
		if r < '─' || r > '╿' {
			return false
		}
	}
	return true
}

// inputBox returns the index of claude's input line (a ❯ line right below a border), or -1.
func inputBox(ls []string) int {
	for i := len(ls) - 1; i > 0; i-- {
		if strings.HasPrefix(unframe(ls[i]), inputMark) && isBorder(ls[i-1]) {
			return i
		}
	}
	return -1
}

// parseDialog returns a dialog's question line and its option labels; a numbered list without claude's ❯ cursor is
// no dialog.
func parseDialog(text string) (string, []string, bool) {
	ls := bottom(lines(text), DialogLines)
	var opts, labels []string
	first, cursor := -1, false
	for i, l := range ls {
		m := optionLine.FindStringSubmatch(unframe(l))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		switch {
		case n == 1:
			opts, labels, first, cursor = nil, nil, i, false
		case n != len(opts)+1:
			continue
		}
		cursor = cursor || m[1] != ""
		opts, labels = append(opts, m[2]+". "+m[3]), append(labels, m[3])
	}
	if !cursor || len(opts) < 2 {
		return "", nil, false
	}
	question := ""
	for i := first - 1; i >= 0; i-- {
		if q := unframe(ls[i]); q != "" && !isBorder(q) {
			question = q
			break
		}
	}
	d := strings.Join(opts, optionSep)
	if question != "" {
		d = question + " " + d
	}
	if r := []rune(d); len(r) > DialogLimit {
		d = string(r[:DialogLimit])
	}
	return d, labels, true
}

// liveFooter reports background work named in the bottom lines only; an old footer up the scrollback does not count.
func liveFooter(text string) footerKind {
	kind := footerNone
	for _, l := range bottom(lines(text), FooterLines) {
		m := bgFooter.FindString(l)
		switch {
		case m == "":
		case strings.Contains(m, shellWord):
			return footerShell
		default:
			kind = footerAgent
		}
	}
	return kind
}

// fingerprint is the pane without its spinner line and timers, so only real output changes it (see SCENARIOS AW-4).
func fingerprint(text string) string {
	var keep []string
	for _, l := range lines(text) {
		t := strings.TrimSpace(l)
		if strings.Contains(t, statusLine) || (t != "" && strings.ContainsRune(spinnerGlyphs, []rune(t)[0]) && strings.Contains(t, ellipsis)) {
			continue
		}
		keep = append(keep, volatile.ReplaceAllString(l, ""))
	}
	return strings.Join(keep, "\n")
}

// lastWords is the last non-empty lines above the input box, so the box and the footer are dropped (see SCENARIOS AW-12).
func lastWords(text string) string {
	ls := lines(text)
	if i := inputBox(ls); i > 0 {
		ls = ls[:i-1]
	}
	var words []string
	for j := len(ls) - 1; j >= 0 && len(words) < LastWordsLines; j-- {
		if strings.TrimSpace(ls[j]) != "" {
			words = append(words, ls[j])
		}
	}
	slices.Reverse(words)
	return strings.Join(words, "\n")
}

// unsentInput reports text typed into claude's input box but not sent (see SCENARIOS AW-8).
func unsentInput(text string) bool {
	ls := lines(text)
	i := inputBox(ls)
	if i < 0 {
		return false
	}
	typed := strings.TrimSpace(strings.TrimPrefix(unframe(ls[i]), inputMark))
	return typed != "" && !strings.HasPrefix(typed, inputHint)
}
