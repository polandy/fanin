package version

import "testing"

func TestString(t *testing.T) {
	if got, want := String("fanind"), "fanind dev"; got != want {
		t.Fatalf("String(%q) = %q, want %q", "fanind", got, want)
	}
}
