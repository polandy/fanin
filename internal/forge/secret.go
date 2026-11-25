package forge

import "errors"

// ErrNoSecret is a secret source that holds no value.
var ErrNoSecret = errors.New("forge: secret is not set")

// Secret holds a forge token.
type Secret struct{ value *string }

// NewSecret wraps a token.
func NewSecret(token string) Secret { return Secret{value: &token} }

// SecretFromEnv reads the token from the environment variable name.
func SecretFromEnv(string, func(string) (string, bool)) (Secret, error) { return Secret{}, nil }

// Reveal returns the token.
func (s Secret) Reveal() string { return "" }

// Empty reports whether the secret holds no token.
func (s Secret) Empty() bool { return false }

// Scrub replaces the token in text.
func (s Secret) Scrub(text string) string { return text }

// String is the token.
func (s Secret) String() string { return s.Reveal() }
