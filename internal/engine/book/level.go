// Package book implements the per-instrument limit order book described in
// docs/system_design/03-order-book.md.
//
// Two sides each carry:
//   - a map[priceKey]*PriceLevel for O(1) "does this level exist" lookups, and
//   - a github.com/google/btree.BTreeG[*PriceLevel] for ordered traversal
//     (best price, top-N snapshot, in-order matching).
//
// Within each PriceLevel, container/list holds the FIFO of *domain.Order, so
// inserts at the back are O(1) and the matcher pops the front in O(1).
//
// The book never owns an "orders by ID" map — that lives in the engine layer
// (T-010) and feeds *domain.Order values into the book directly. This package
// works at the *Order pointer level and uses the order's elem / level back-
// pointers (set via Order.SetElem / Order.SetLevel) to make Cancel O(1).
package book

import (
	"container/list"

	"matching-engine/internal/domain/decimal"
)

// PriceLevel holds the FIFO of resting orders at one price plus an
// incrementally maintained running total of remaining quantity.
//
// Total invariant: Total == sum(o.RemainingQuantity for o in Orders).
// The book mutates Total on insert and on Cancel. The matcher (T-010) is
// expected to maintain Total directly when it produces fills — see
// docs/system_design/04-matching-algorithm.md — so RemoveFilledMaker on this
// type does NOT touch Total. (See OrderBook.RemoveFilledMaker.)
type PriceLevel struct {
	// Price is the level's canonical price. Multiple inputs that compare
	// equal as decimals (e.g. "500000000" and "500000000.0") collapse onto
	// one PriceLevel via priceKey canonicalisation.
	Price decimal.Decimal

	// Orders is the FIFO of *domain.Order at this price, ordered by arrival.
	// list.Element values returned by PushBack are stored back on the order
	// via Order.SetElem so list.Remove(elem) is O(1).
	Orders *list.List

	// Total is the running sum of RemainingQuantity across Orders.
	// Maintained on every insert / cancel / fill so that a depth-N book
	// snapshot is O(N), not O(N * level_size).
	Total decimal.Decimal
}

// LevelSnapshot is a flat (Price, Quantity) pair for a single level in a
// depth snapshot. Quantity is the level's running Total at snapshot time.
type LevelSnapshot struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}
