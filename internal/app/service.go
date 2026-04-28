// Package app is the transport-agnostic application layer that sits between
// HTTP handlers and the engine. Its only responsibility in v1 is idempotency
// dedup keyed by (user_id, client_order_id).
//
// LOCK ORDERING INVARIANT (§06, invariant 13 of ARCHITECT_PLAN.md §3):
//
//	dedupMu → engine.mu   (always in this order, never reversed)
//
// Only the Place method takes both mutexes. dedupMu is acquired first, then
// engine.mu is acquired inside engine.Place. No other path takes dedupMu.
// No path takes the mutexes in the reverse order. Two-mutex deadlock is
// therefore structurally impossible.
package app

import (
	"sync"

	"matching-engine/internal/domain"
	"matching-engine/internal/engine"
	"matching-engine/internal/engine/book"
)

// PlaceCommand is the service-layer placement command. It mirrors
// engine.PlaceCommand and adds ClientOrderID, which the engine does not
// know about. The HTTP handler (T-013/T-014) populates all fields after
// request validation.
type PlaceCommand struct {
	engine.PlaceCommand                // embedded: UserID, Side, Type, Price, TriggerPrice, Quantity
	ClientOrderID       string         // required; 1..64 ASCII printable (validated upstream)
}

// Service is the application service. It wraps *engine.Engine with an
// idempotency dedup layer. Safe for concurrent use.
type Service struct {
	engine  *engine.Engine
	dedupMu sync.Mutex
	dedup   map[string]engine.PlaceResult
}

// NewService constructs a Service backed by eng with an empty dedup map.
func NewService(eng *engine.Engine) *Service {
	return &Service{
		engine: eng,
		dedup:  make(map[string]engine.PlaceResult),
	}
}

// Place places an order with idempotency dedup. On the first call for a
// given (UserID, ClientOrderID) pair the command is forwarded to the engine
// and the result is cached (including business-rejected results). On
// subsequent calls the cached result is returned without re-invoking the
// engine. Engine sentinel errors (ErrTooManyOrders, ErrTooManyStops) are
// NOT cached — the caller may retry safely with the same ClientOrderID.
//
// dedupMu is held for the entire duration of Place, including the engine
// call, so concurrent requests with the same key serialise here and only
// one reaches the engine.
func (s *Service) Place(cmd PlaceCommand) (engine.PlaceResult, error) {
	key := cmd.UserID + "\x00" + cmd.ClientOrderID

	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()

	if cached, ok := s.dedup[key]; ok {
		return cached, nil
	}

	result, err := s.engine.Place(cmd.PlaceCommand)
	if err != nil {
		// Sentinel errors (cap exhaustion) are not cached. The caller may
		// retry with the same ClientOrderID once capacity is available.
		return engine.PlaceResult{}, err
	}

	// Cache successful results, including business rejections (Status=Rejected).
	// The cached *Order and []*Trade pointers are the same pointers held by the
	// engine. HTTP marshalling (T-014) is read-only, so sharing them is safe.
	s.dedup[key] = result
	return result, nil
}

// Cancel forwards to the engine. Takes only engine.mu; dedupMu is not acquired.
func (s *Service) Cancel(orderID string) (*domain.Order, error) {
	return s.engine.Cancel(orderID)
}

// Snapshot forwards to the engine. Takes only engine.mu; dedupMu is not acquired.
func (s *Service) Snapshot(depth int) (bids, asks []book.LevelSnapshot) {
	return s.engine.Snapshot(depth)
}

// Trades forwards to the engine. Takes only engine.mu; dedupMu is not acquired.
func (s *Service) Trades(limit int) []*domain.Trade {
	return s.engine.Trades(limit)
}
