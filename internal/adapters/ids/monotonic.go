package ids

import (
	"strconv"

	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.IDGenerator = (*Monotonic)(nil)

// Monotonic generates monotonically-increasing order and trade identifiers
// in the formats "o-<n>" and "t-<n>", where <n> is a uint64 starting at 1.
//
// Not safe for concurrent use; callers must serialise access
// (the engine mutex serves this purpose).
type Monotonic struct {
	orderN uint64
	tradeN uint64
}

// NewMonotonic returns a *Monotonic with both counters initialised to 0.
// The first call to NextOrderID returns "o-1"; the first call to
// NextTradeID returns "t-1".
func NewMonotonic() *Monotonic {
	return &Monotonic{}
}

// NextOrderID increments the order counter and returns the formatted ID.
func (m *Monotonic) NextOrderID() string {
	m.orderN++
	return "o-" + strconv.FormatUint(m.orderN, 10)
}

// NextTradeID increments the trade counter and returns the formatted ID.
func (m *Monotonic) NextTradeID() string {
	m.tradeN++
	return "t-" + strconv.FormatUint(m.tradeN, 10)
}
