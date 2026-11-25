// Package clock is the only place that touches wall-clock time; everything else takes a Clock.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock reads the time and schedules one-shot wake-ups.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives the time once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// Real is the wall clock.
type Real struct{}

// Now returns time.Now.
func (Real) Now() time.Time { return time.Now() }

// After wraps time.After.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

type waiter struct {
	at time.Time
	ch chan time.Time
}

// Manual is a clock that only moves when Advance is called.
type Manual struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

// NewManual returns a Manual clock standing at start.
func NewManual(start time.Time) *Manual { return &Manual{now: start} }

// Now returns the manual time.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// After returns a buffered channel that fires on the Advance that reaches the deadline; d <= 0 fires at once.
func (m *Manual) After(d time.Duration) <-chan time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- m.now
		return ch
	}
	m.waiters = append(m.waiters, waiter{at: m.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward by d and fires every due After in deadline order, each with its own deadline.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
	sort.SliceStable(m.waiters, func(i, j int) bool { return m.waiters[i].at.Before(m.waiters[j].at) })
	rest := m.waiters[:0]
	for _, w := range m.waiters {
		if w.at.After(m.now) {
			rest = append(rest, w)
			continue
		}
		w.ch <- w.at
	}
	m.waiters = rest
}

// Pending returns the number of After channels that have not fired yet.
func (m *Manual) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.waiters)
}
