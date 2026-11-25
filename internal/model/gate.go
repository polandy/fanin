package model

import "time"

// Verdict is a gate's answer for one PR head. An unreadable gate is an error, never a Verdict.
type Verdict string

// The gate verdicts.
const (
	VerdictGreen   Verdict = "green"
	VerdictPending Verdict = "pending"
	VerdictRed     Verdict = "red"
)

// AllVerdicts lists every Verdict.
var AllVerdicts = []Verdict{VerdictGreen, VerdictPending, VerdictRed}

// ParseVerdict reads a stored verdict; an unknown value is ErrUnknownValue.
func ParseVerdict(s string) (Verdict, error) { return parse("verdict", s, AllVerdicts) }

// HoldKind says what a project hold stops.
type HoldKind string

// The hold kinds.
const (
	// HoldMerge stops every merge of the project after a failed post-merge hook (see SCENARIOS MG-4).
	HoldMerge HoldKind = "merge"
	// HoldCI stops fresh workers and merges while CI on main is red, until FixTask merges (see SCENARIOS MG-8).
	HoldCI HoldKind = "ci"
)

// AllHoldKinds lists every HoldKind.
var AllHoldKinds = []HoldKind{HoldMerge, HoldCI}

// ParseHoldKind reads a stored hold kind; an unknown value is ErrUnknownValue.
func ParseHoldKind(s string) (HoldKind, error) { return parse("hold kind", s, AllHoldKinds) }

// Hold stops further merges (and, for HoldCI, fresh workers) of one project until it is lifted.
type Hold struct {
	ID      string
	Project string
	Kind    HoldKind
	Reason  string
	// FixTask is the task whose merge lifts a HoldCI; empty for HoldMerge.
	FixTask   TaskID
	CreatedAt time.Time
	LiftedAt  time.Time
}

// Lock is one project lock row: the holder and its lease (concept C-LOCK).
type Lock struct {
	Project string
	// Holder is the task or run ID that took the lock.
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// Expired reports whether the lease has run out at now; an expired lock is free to take.
func (l Lock) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }
