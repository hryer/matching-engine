package clock

import (
	"time"

	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.Clock = (*Fake)(nil)

// Fake is a deterministic Clock for tests. The current instant is set
// explicitly via Set or advanced via Advance; Now() never auto-advances.
//
// Fake is NOT goroutine-safe. Engine tests run single-threaded under the
// engine mutex, which serialises all clock calls.
type Fake struct {
	now time.Time
}

// NewFake returns a *Fake initialised to the given instant. Pass a fixed,
// known instant (e.g. time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) so
// that replay tests produce byte-identical output.
func NewFake(initial time.Time) *Fake {
	return &Fake{now: initial}
}

// Now returns the clock's current instant.
func (f *Fake) Now() time.Time { return f.now }

// Advance adds d to the current instant.
func (f *Fake) Advance(d time.Duration) { f.now = f.now.Add(d) }

// Set overwrites the current instant.
func (f *Fake) Set(t time.Time) { f.now = t }
