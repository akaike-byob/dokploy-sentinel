// Package clock provides an injectable time source so that flap damping,
// cooldowns, rate math, and the digest window can be tested deterministically
// (see docs/plan/09-testing.md §9.1). Nothing in internal/checks, internal/alert,
// or internal/state may call time.Now() directly; they take a Clock instead.
package clock

import "time"

// Clock is the single time source threaded through a run.
type Clock interface {
	Now() time.Time
}

// Real is the production clock backed by time.Now().
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// Fake is a hand-advanced clock for tests.
type Fake struct {
	T time.Time
}

// NewFake returns a Fake pinned to t.
func NewFake(t time.Time) *Fake { return &Fake{T: t} }

// Now returns the fake's current time.
func (f *Fake) Now() time.Time { return f.T }

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.T = f.T.Add(d) }
