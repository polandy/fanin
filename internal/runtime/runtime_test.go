package runtime_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/runtime"
)

func TestExitErrorMatchedWithErrorsAs(t *testing.T) {
	var err error = fmt.Errorf("run gh: %w", &runtime.ExitError{Argv: []string{"gh", "pr"}, Code: 3, StderrTail: "nope"})
	var ee *runtime.ExitError
	if !errors.As(err, &ee) {
		t.Fatal("errors.As did not match *ExitError")
	}
	if ee.Code != 3 || ee.StderrTail != "nope" {
		t.Errorf("got code=%d tail=%q", ee.Code, ee.StderrTail)
	}
	for _, want := range []string{"gh pr", "3", "nope"} {
		if !strings.Contains(ee.Error(), want) {
			t.Errorf("Error() = %q, missing %q", ee.Error(), want)
		}
	}
}

func TestTail(t *testing.T) {
	long := strings.Repeat("x", runtime.StderrTailLimit+10)
	if got := runtime.Tail([]byte(long)); len(got) != runtime.StderrTailLimit {
		t.Errorf("len = %d, want %d", len(got), runtime.StderrTailLimit)
	}
	if got := runtime.Tail([]byte("short\n")); got != "short" {
		t.Errorf("got %q", got)
	}
}
