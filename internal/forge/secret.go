package forge

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ErrNoSecret is a secret source that holds no value, such as an unset environment variable.
var ErrNoSecret = errors.New("forge: secret is not set")

// Secret holds a forge token. Every printed, logged or marshalled form is Redacted, and an unexported Secret
// field prints as a pointer, so the token reaches neither logs nor config dumps (see SCENARIOS GT-10).
type Secret struct{ value *string }

// NewSecret wraps a token.
func NewSecret(token string) Secret { return Secret{value: &token} }

// SecretFromEnv reads the token from the environment variable name through lookup (os.LookupEnv in fanind); an
// unset or empty variable is ErrNoSecret.
func SecretFromEnv(name string, lookup func(string) (string, bool)) (Secret, error) {
	v, ok := lookup(name)
	if !ok || strings.TrimSpace(v) == "" {
		return Secret{}, fmt.Errorf("%w: environment variable %s is empty; export the forge token in %s", ErrNoSecret, name, name)
	}
	return NewSecret(v), nil
}

// Reveal returns the token, for the Authorization header only.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// Empty reports whether the secret holds no token.
func (s Secret) Empty() bool { return s.Reveal() == "" }

// Scrub replaces the token in text with Redacted and then applies Redact.
func (s Secret) Scrub(text string) string {
	if t := s.Reveal(); t != "" {
		text = strings.ReplaceAll(text, t, Redacted)
	}
	return Redact(text)
}

// String is Redacted.
func (Secret) String() string { return Redacted }

// GoString is Redacted.
func (Secret) GoString() string { return Redacted }

// LogValue is Redacted.
func (Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalText is Redacted; JSON and YAML encoders use it.
func (Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalYAML is Redacted.
func (Secret) MarshalYAML() (any, error) { return Redacted, nil }
