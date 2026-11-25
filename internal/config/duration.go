package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a config duration written as a Go duration string such as 15m.
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w; write a duration such as 15m", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML writes the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return d.Std().String(), nil }

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
