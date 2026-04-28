package inmem

import (
	"matching-engine/internal/domain"
	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.EventPublisher = (*Ring)(nil)

// Ring is a bounded circular buffer that retains the last `cap` published
// trades. On overflow the oldest trade is silently overwritten in O(1).
//
// Not safe for concurrent use; callers must serialise access.
// The engine mutex guarantees this for both Publish (called from engine.Place)
// and Recent (called from engine.Trades).
type Ring struct {
	buf   []*domain.Trade
	next  int // write index for the next Publish
	count int // number of trades currently stored (0 ≤ count ≤ cap)
	cap   int
}

// NewRing constructs a Ring with the given capacity. Panics if capacity <= 0.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("inmem.NewRing: capacity must be > 0")
	}
	return &Ring{
		buf: make([]*domain.Trade, capacity),
		cap: capacity,
	}
}

// Publish appends a trade to the ring. If the ring is full, the oldest
// trade is overwritten. This is O(1) with no allocation.
func (r *Ring) Publish(trade *domain.Trade) {
	r.buf[r.next] = trade
	r.next = (r.next + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
}

// Recent returns up to limit of the most recently published trades,
// ordered newest first (index 0 is the most recent trade).
//
// If limit <= 0, an empty slice is returned.
// If limit > count, all stored trades are returned (limit is clamped to count).
func (r *Ring) Recent(limit int) []*domain.Trade {
	if limit <= 0 {
		return []*domain.Trade{}
	}
	if limit > r.count {
		limit = r.count
	}
	if limit == 0 {
		return []*domain.Trade{}
	}

	out := make([]*domain.Trade, limit)
	// The most recently written slot is at index (r.next - 1 + r.cap) % r.cap.
	// Walk backwards from there for `limit` steps.
	for i := 0; i < limit; i++ {
		idx := (r.next - 1 - i + r.cap*2) % r.cap // *2 guards against r.next==0
		out[i] = r.buf[idx]
	}
	return out
}
