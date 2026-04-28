package clock

import (
	"time"

	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.Clock = (*Real)(nil)

// Real is the production Clock adapter. It delegates to time.Now().
type Real struct{}

// NewReal returns a Real clock.
func NewReal() Real { return Real{} }

// Now returns the current wall-clock time.
func (Real) Now() time.Time { return time.Now() }
