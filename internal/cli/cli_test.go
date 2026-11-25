package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain", errors.New("boom"), ExitError},
		{"usage", Usagef("bad flag %q", "-x"), ExitUsage},
		{"wrapped usage", fmt.Errorf("outer: %w", Usagef("x")), ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestOutput(t *testing.T) {
	var out, errb bytes.Buffer
	o := Output{Out: &out, Err: &errb}
	o.Println("a", "b")
	o.Printf("%d\n", 3)
	o.Errorf("bad %s", "x")
	if got, want := out.String(), "a b\n3\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := errb.String(), "fanin: bad x\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
