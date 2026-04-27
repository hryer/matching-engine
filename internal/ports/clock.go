package ports

import "time"

// Clock returns the current time. The engine uses this instead of time.Now()
// so tests can drive a deterministic clock.
type Clock interface {
	// Now returns the current time as observed by this clock.
	Now() time.Time
}
