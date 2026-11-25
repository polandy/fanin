package clock_test

import (
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
)

var start = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestManualAfterFiresOnlyOnAdvance(t *testing.T) {
	c := clock.NewManual(start)
	ch := c.After(time.Minute)

	c.Advance(59 * time.Second)
	select {
	case <-ch:
		t.Fatal("After fired before its duration elapsed")
	default:
	}

	c.Advance(time.Second)
	select {
	case got := <-ch:
		if want := start.Add(time.Minute); !got.Equal(want) {
			t.Errorf("fired with %v, want %v", got, want)
		}
	default:
		t.Fatal("After did not fire once its duration elapsed")
	}
}

func TestManualAdvanceFiresInDeadlineOrder(t *testing.T) {
	c := clock.NewManual(start)
	late := c.After(2 * time.Second)
	early := c.After(time.Second)

	c.Advance(time.Hour)
	if got := <-early; !got.Equal(start.Add(time.Second)) {
		t.Errorf("early fired with %v", got)
	}
	if got := <-late; !got.Equal(start.Add(2 * time.Second)) {
		t.Errorf("late fired with %v", got)
	}
}

func TestManualNowAndNonPositiveAfter(t *testing.T) {
	c := clock.NewManual(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", c.Now(), start)
	}
	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) must fire immediately")
	}
	c.Advance(time.Second)
	if want := start.Add(time.Second); !c.Now().Equal(want) {
		t.Errorf("Now = %v, want %v", c.Now(), want)
	}
}

func TestManualPending(t *testing.T) {
	c := clock.NewManual(start)
	c.After(time.Second)
	if got := c.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	c.Advance(time.Second)
	if got := c.Pending(); got != 0 {
		t.Fatalf("Pending = %d, want 0", got)
	}
}

func TestRealAfterFires(t *testing.T) {
	var c clock.Clock = clock.Real{}
	if c.Now().IsZero() {
		t.Fatal("Real.Now is zero")
	}
	<-c.After(0)
}
