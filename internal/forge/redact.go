package forge

import "regexp"

// Redacted replaces every secret Redact finds.
const Redacted = "[REDACTED]"

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), Redacted},
	{regexp.MustCompile(`(?i)\b(authorization:\s*(?:token|bearer|basic)\s+)\S+`), "${1}" + Redacted},
	{regexp.MustCompile(`(?i)\b((?:token|access_token|password)[=:]\s*)[^\s&"']+`), "${1}" + Redacted},
	{regexp.MustCompile(`(https?://)[^/\s@]+@`), "${1}" + Redacted + "@"},
}

// Redact removes forge tokens, authorization headers and URL credentials from s; every forge error and log line
// that carries forge output goes through it (see SCENARIOS GT-10).
func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return Redact(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// RedactError wraps err so its text is redacted; errors.Is and errors.As still see err. Nil stays nil.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{err: err}
}
