// Package stops implements the per-instrument stop book described in
// docs/system_design/05-stop-orders.md.
//
// A StopBook holds armed stop / stop-limit orders in two btrees plus a
// byID map for O(1) cancel:
//
//   - buys  *btree.BTreeG[*domain.Order] — ascending by TriggerPrice so the
//     smallest-trigger buy stop is at the head and fires first as the last
//     trade price rises.
//   - sells *btree.BTreeG[*domain.Order] — descending by TriggerPrice so the
//     largest-trigger sell stop is at the head and fires first as the last
//     trade price falls.
//   - byID  map[string]*domain.Order — single source of truth for membership;
//     Len() == len(byID) == buys.Len() + sells.Len().
//
// The btree comparators are strict total orders: ties in TriggerPrice are
// broken by ascending seq so two distinct orders never compare equal (per
// ARCHITECT_PLAN.md §4 risk register on btree Less non-determinism).
//
// Concurrency: this package is NOT thread-safe on its own. The engine
// (T-010) holds a single mutex that serialises every call into StopBook,
// so all internal mutations happen in one critical section.
package stops

import (
	"sort"

	"github.com/google/btree"

	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// btreeDegree controls the branching factor of the underlying btree.
// Matches the order book's choice (see internal/engine/book) so the two
// containers behave consistently under load. The value itself is not
// observable through StopBook's public API.
const btreeDegree = 32

// StopBook is the per-instrument armed-stop container. Twin btrees keep
// each side ordered for next-to-fire peek; byID gives O(1) cancel and
// Get. See package doc for invariants.
type StopBook struct {
	buys  *btree.BTreeG[*domain.Order] // ascending by TriggerPrice, ties by seq
	sells *btree.BTreeG[*domain.Order] // descending by TriggerPrice, ties by seq
	byID  map[string]*domain.Order
}

// New returns an empty StopBook.
func New() *StopBook {
	return &StopBook{
		buys:  btree.NewG[*domain.Order](btreeDegree, lessBuy),
		sells: btree.NewG[*domain.Order](btreeDegree, lessSell),
		byID:  make(map[string]*domain.Order),
	}
}

// lessBuy orders buy stops ascending by TriggerPrice with ties broken by
// ascending seq. Strict total order: if a == b by both keys, the orders
// must be the same *Order pointer because seq is unique per engine.
func lessBuy(a, b *domain.Order) bool {
	if c := a.TriggerPrice.Cmp(b.TriggerPrice); c != 0 {
		return c < 0
	}
	return a.Seq() < b.Seq()
}

// lessSell orders sell stops descending by TriggerPrice with ties broken
// by ascending seq. Same strict-total-order property as lessBuy.
func lessSell(a, b *domain.Order) bool {
	if c := a.TriggerPrice.Cmp(b.TriggerPrice); c != 0 {
		return c > 0
	}
	return a.Seq() < b.Seq()
}

// Insert places an armed stop into the book. The caller (engine) must
// have already verified that o.Type ∈ {Stop, StopLimit} and that the
// trigger condition is not already satisfied — see T-010.
//
// The same order pointer is registered in byID and in the side's btree
// in one critical section (engine mutex held). Re-inserting an order
// with the same ID overwrites the byID entry.
func (s *StopBook) Insert(o *domain.Order) {
	if o.Side == domain.Buy {
		s.buys.ReplaceOrInsert(o)
	} else {
		s.sells.ReplaceOrInsert(o)
	}
	s.byID[o.ID] = o
}

// Cancel removes an armed stop by ID from BOTH byID and the relevant
// btree before returning. Returns (nil, false) if the ID is unknown.
//
// Because the engine mutex serialises every call, "both deletions
// happen before returning" implies callers never observe a half-removed
// stop.
func (s *StopBook) Cancel(orderID string) (*domain.Order, bool) {
	o, ok := s.byID[orderID]
	if !ok {
		return nil, false
	}
	delete(s.byID, orderID)
	if o.Side == domain.Buy {
		s.buys.Delete(o)
	} else {
		s.sells.Delete(o)
	}
	return o, true
}

// Get returns the armed stop with the given ID, or (nil, false) if no
// such stop is currently in the book. Used by the engine on the
// Cancel(orderID) path to distinguish "not found" from "found but
// resting in the order book".
func (s *StopBook) Get(orderID string) (*domain.Order, bool) {
	o, ok := s.byID[orderID]
	return o, ok
}

// Len returns the number of armed stops in the book. Equal to
// len(byID) and to buys.Len() + sells.Len() — these equalities are
// asserted in the package's tests and used by T-010's armedStops
// invariant check.
func (s *StopBook) Len() int { return len(s.byID) }


// DrainTriggered removes and returns every stop whose trigger condition
// is satisfied at the given lastTradePrice. Triggers are inclusive:
//
//   - buy fires when  TriggerPrice <= lastTradePrice
//   - sell fires when TriggerPrice >= lastTradePrice
//
// The algorithm peeks the head of each btree in a loop and pops while
// the head fires. After both heads no longer fire, the collected slice
// is sorted by ascending seq so that a same-price hit across both sides
// interleaves in placement order (see §05 trigger algorithm).
//
// Returned orders are removed from byID and their respective btree.
// Status / Type mutation is the caller's responsibility (T-010).
func (s *StopBook) DrainTriggered(lastTradePrice decimal.Decimal) []*domain.Order {
	var out []*domain.Order
	for {
		if head, ok := s.buys.Min(); ok && head.TriggerPrice.LessThanOrEqual(lastTradePrice) {
			s.buys.Delete(head)
			delete(s.byID, head.ID)
			out = append(out, head)
			continue
		}
		if head, ok := s.sells.Min(); ok && head.TriggerPrice.GreaterThanOrEqual(lastTradePrice) {
			s.sells.Delete(head)
			delete(s.byID, head.ID)
			out = append(out, head)
			continue
		}
		break
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq() < out[j].Seq() })
	return out
}
