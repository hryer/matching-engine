package book

import (
	"container/list"
	"math/big"
	"strconv"

	"github.com/google/btree"

	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// btreeDegree is the branching factor passed to btree.NewG. The library
// recommends a value in the 2..32 range; 32 keeps node sizes cache-friendly
// without producing tall trees for the depths we expect (well below 1e5
// price levels per side in v1).
const btreeDegree = 32

// maxSnapshotDepth caps the depth a caller may request from Snapshot.
// See ARCHITECT_PLAN.md §7. Requests above this are silently capped.
const maxSnapshotDepth = 1000

// side is one half of the book — bids or asks. The map and btree both
// reference the same *PriceLevel pointers, so a level can be looked up in
// O(1) by canonical price and walked in price order in O(log n) per step.
type side struct {
	levels map[string]*PriceLevel
	index  *btree.BTreeG[*PriceLevel]
	isBid  bool
}

// newSide constructs an empty side. The btree comparator orders bids
// descending and asks ascending so that, in both cases, the "best" price
// is the tree's minimum. Snapshot and BestLevel can therefore use the
// same Ascend traversal regardless of side.
func newSide(isBid bool) *side {
	return &side{
		levels: make(map[string]*PriceLevel),
		index:  btree.NewG[*PriceLevel](btreeDegree, lessByPrice(isBid)),
		isBid:  isBid,
	}
}

// lessByPrice returns the comparator the btree uses to order PriceLevels.
//
// Bids are stored descending (a < b iff a.Price > b.Price) so that the
// best bid (highest price) is the tree's Min(). Asks are stored ascending
// so that the best ask (lowest price) is also Min(). This symmetry lets
// BestLevel and Snapshot use a single Ascend walk per side.
func lessByPrice(isBid bool) btree.LessFunc[*PriceLevel] {
	return func(a, b *PriceLevel) bool {
		if isBid {
			return a.Price.GreaterThan(b.Price)
		}
		return a.Price.LessThan(b.Price)
	}
}

// OrderBook is a single-instrument limit order book. It is not safe for
// concurrent use: the engine (T-010) serialises all access through a single
// goroutine. See docs/system_design/03-order-book.md.
type OrderBook struct {
	bids *side
	asks *side
}

// New returns an empty OrderBook with both sides initialised.
func New() *OrderBook {
	return &OrderBook{
		bids: newSide(true),
		asks: newSide(false),
	}
}

// sideFor returns the side this Side enum refers to: Buy → bids, Sell → asks.
func (b *OrderBook) sideFor(s domain.Side) *side {
	if s == domain.Buy {
		return b.bids
	}
	return b.asks
}

// Insert places o at the back of the FIFO at its price level. If no level
// exists at that price one is created and added to both the map and the
// btree. The order's elem and level back-pointers are set so Cancel can be
// O(1).
//
// Insert mutates level.Total by += o.RemainingQuantity. Callers that wish
// to insert a partially-filled order should set RemainingQuantity before
// the call.
//
// Complexity: O(1) at an existing level, O(log n) at a new level.
func (b *OrderBook) Insert(o *domain.Order) {
	s := b.sideFor(o.Side)
	key := priceKey(o.Price)

	lvl, exists := s.levels[key]
	if !exists {
		lvl = &PriceLevel{
			Price:  o.Price,
			Orders: list.New(),
			Total:  decimal.Zero,
		}
		s.levels[key] = lvl
		s.index.ReplaceOrInsert(lvl)
	}

	elem := lvl.Orders.PushBack(o)
	lvl.Total = lvl.Total.Add(o.RemainingQuantity)
	o.SetElem(elem)
	o.SetLevel(lvl)
}

// Cancel removes a resting order from its level, decrements level.Total by
// the order's current RemainingQuantity, removes the level if it becomes
// empty, and clears the order's elem and level back-pointers.
//
// The caller must guarantee the order is currently resting on this book
// (i.e. Order.Elem() != nil and Order.Level() != nil). Cancelling a non-
// resting order is a no-op.
//
// Complexity: O(1) when the level remains non-empty, O(log n) when the
// level is removed.
func (b *OrderBook) Cancel(o *domain.Order) {
	lvl, ok := o.Level().(*PriceLevel)
	if !ok || lvl == nil || o.Elem() == nil {
		return
	}

	lvl.Orders.Remove(o.Elem())
	lvl.Total = lvl.Total.Sub(o.RemainingQuantity)

	if lvl.Orders.Len() == 0 {
		s := b.sideFor(o.Side)
		delete(s.levels, priceKey(lvl.Price))
		s.index.Delete(lvl)
	}

	o.SetElem(nil)
	o.SetLevel(nil)
}

// RemoveFilledMaker removes a fully-filled maker from the head of its
// level's FIFO and removes the level if empty. It does NOT modify
// level.Total — the matcher decrements Total per fill as it produces
// trades (see docs/system_design/04-matching-algorithm.md), so by the
// time the maker's RemainingQuantity is zero the running Total has
// already been brought down to match. RemoveFilledMaker also clears the
// order's elem and level back-pointers.
//
// The caller is expected to pass the level argument that the maker
// currently rests on. Passing a stale level is a programmer error and
// will desynchronise the level/order back-pointers from the book.
//
// Complexity: O(1) for the maker, O(log n) when the level is removed.
func (b *OrderBook) RemoveFilledMaker(level *PriceLevel, o *domain.Order) {
	if level == nil || o.Elem() == nil {
		return
	}

	level.Orders.Remove(o.Elem())

	if level.Orders.Len() == 0 {
		s := b.sideFor(o.Side)
		delete(s.levels, priceKey(level.Price))
		s.index.Delete(level)
	}

	o.SetElem(nil)
	o.SetLevel(nil)
}

// BestLevel returns the best PriceLevel resting on the requested side, or
// nil if that side is empty.
//
// Semantics: the side argument refers to the side the level lives on, NOT
// the side of an incoming taker. BestLevel(Buy) returns the highest bid;
// BestLevel(Sell) returns the lowest ask. The matcher (T-010) walks the
// opposite side to the incoming order, e.g. for a Buy taker it calls
// BestLevel(Sell) to find the cheapest ask.
//
// Complexity: O(log n) (single tree min lookup via Ascend, stops after one).
func (b *OrderBook) BestLevel(s domain.Side) *PriceLevel {
	sd := b.sideFor(s)
	var best *PriceLevel
	sd.index.Ascend(func(lvl *PriceLevel) bool {
		best = lvl
		return false
	})
	return best
}

// Snapshot returns the top depth levels per side, ordered best to worst.
// Bids are returned in descending price order; asks in ascending price
// order. Both slices have len <= depth. depth is clamped to [0, 1000];
// negative requests return empty slices, and requests above 1000 are
// silently capped.
//
// Complexity: O(depth) per side.
func (b *OrderBook) Snapshot(depth int) (bids, asks []LevelSnapshot) {
	if depth < 0 {
		depth = 0
	}
	if depth > maxSnapshotDepth {
		depth = maxSnapshotDepth
	}
	return collectFromSide(b.bids, depth), collectFromSide(b.asks, depth)
}

// collectFromSide walks side s in best-to-worst order (Min first) and
// returns up to depth LevelSnapshots.
func collectFromSide(s *side, depth int) []LevelSnapshot {
	if depth == 0 {
		return []LevelSnapshot{}
	}
	out := make([]LevelSnapshot, 0, depth)
	s.index.Ascend(func(lvl *PriceLevel) bool {
		out = append(out, LevelSnapshot{Price: lvl.Price, Quantity: lvl.Total})
		return len(out) < depth
	})
	return out
}

// priceKey returns a canonical string for a decimal price suitable as a
// map key. shopspring/decimal preserves trailing zeros in String(), so
// NewFromString("500000000") and NewFromString("500000000.0") render
// differently despite comparing equal. We strip trailing zeros from the
// coefficient — incrementing the exponent for each — and emit
// "<coef>e<exp>". For decimal.Zero we emit a fixed canonical form so
// any-exponent representations of zero collide.
//
// Complexity: O(d) where d is the number of decimal digits in the
// coefficient. For human-scale prices d is small (≤ 20).
func priceKey(d decimal.Decimal) string {
	coef := new(big.Int).Set(d.Coefficient())
	exp := int64(d.Exponent())

	if coef.Sign() == 0 {
		return "0e0"
	}

	ten := big.NewInt(10)
	zero := big.NewInt(0)
	q := new(big.Int)
	r := new(big.Int)
	for {
		q.QuoRem(coef, ten, r)
		if r.Cmp(zero) != 0 {
			break
		}
		coef.Set(q)
		exp++
		if coef.Cmp(zero) == 0 {
			// Shouldn't happen — the Sign() == 0 check above handled zero,
			// and dividing a non-zero integer by 10 cannot reach zero
			// without first producing a non-zero remainder. Belt and braces.
			return "0e0"
		}
	}

	return coef.String() + "e" + strconv.FormatInt(exp, 10)
}
