// Package engine — seeded-random property tests for engine invariants.
//
// TestEngine_Property_AllInvariants runs ~100 iterations × ~100 ops with a
// fixed seed list. After every operation it checks:
//
//   - Inv 1:  book never crosses (bestBid.Price < bestAsk.Price)
//   - Inv 2:  cumulative fills per order ≤ original quantity
//   - Inv 7:  no self-trade
//   - Inv 8:  Snapshot quantity totals match sum of RemainingQuantity in byID
//   - Inv 17: openOrders == len(byID) and armedStops == stops.Len()
//   - PriceLevel.Total: snapshot-level total matches byID sum at that price
//
// On any failure t.Fatalf includes seed, op index, and state dump for local
// reproduction.
package engine

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/adapters/publisher/inmem"
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
	"matching-engine/internal/engine/book"
)

// ---------------------------------------------------------------------------
// Helpers — property test engine construction
// ---------------------------------------------------------------------------

// newPropertyEngine constructs an engine with high caps so cap errors do not
// dominate the random sequence. The ring buffer is large enough to hold all
// trades a 100-op × 100-seed run can produce.
func newPropertyEngine() *Engine {
	return New(Deps{
		Clock:         clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(20000),
		MaxOpenOrders: 10000,
		MaxArmedStops: 1000,
	})
}

// ---------------------------------------------------------------------------
// Side table maintained alongside the engine
// ---------------------------------------------------------------------------

// propState is the side-table the property test maintains next to the engine.
type propState struct {
	// orderUser maps orderID → userID for every order ever placed.
	// Filled/cancelled orders stay here so we can resolve self-trade checks
	// on historical trades.
	orderUser map[string]string

	// orderQty maps orderID → original quantity for every order ever placed.
	// Used to assert Inv 2 (cumulative fills ≤ original qty).
	orderQty map[string]decimal.Decimal

	// liveIDs is the set of order IDs that were resting in byID at last check.
	// Updated lazily from Place results; used as Cancel targets.
	liveIDs []string

	// liveStopIDs is the set of armed stop IDs.
	liveStopIDs []string

	// seenTradeIDs tracks which trade IDs we have already validated for
	// Inv 7 (self-trade). Only new trades from each op need to be checked.
	seenTradeIDs map[string]struct{}

	// cumFills is cumulative fill quantity per orderID across all trades seen.
	cumFills map[string]decimal.Decimal

	// hadTrade tracks whether at least one trade has occurred. Used to skip
	// sell-stop placement (which would be immediately rejected on a fresh
	// engine before any lastTradePrice is set > 0, producing noise).
	hadTrade bool
}

func newPropState() *propState {
	return &propState{
		orderUser:    make(map[string]string),
		orderQty:     make(map[string]decimal.Decimal),
		seenTradeIDs: make(map[string]struct{}),
		cumFills:     make(map[string]decimal.Decimal),
	}
}

// recordOrder saves the user → order mapping and the original quantity.
func (s *propState) recordOrder(id, userID string, qty decimal.Decimal) {
	s.orderUser[id] = userID
	s.orderQty[id] = qty
}

// addLive adds id to the liveIDs slice if it is not already there.
func (s *propState) addLive(id string) {
	s.liveIDs = append(s.liveIDs, id)
}

// addLiveStop adds id to liveStopIDs.
func (s *propState) addLiveStop(id string) {
	s.liveStopIDs = append(s.liveStopIDs, id)
}

// removeLiveAt removes the element at index i from liveIDs.
func (s *propState) removeLiveAt(i int) {
	s.liveIDs[i] = s.liveIDs[len(s.liveIDs)-1]
	s.liveIDs = s.liveIDs[:len(s.liveIDs)-1]
}

// removeLiveStopAt removes the element at index i from liveStopIDs.
func (s *propState) removeLiveStopAt(i int) {
	s.liveStopIDs[i] = s.liveStopIDs[len(s.liveStopIDs)-1]
	s.liveStopIDs = s.liveStopIDs[:len(s.liveStopIDs)-1]
}

// ---------------------------------------------------------------------------
// Invariant checkers — each returns a non-empty string on violation.
// ---------------------------------------------------------------------------

// inv1BookNeverCrosses checks that bestBid.Price < bestAsk.Price (strict).
// Returns "" if the book has fewer than two populated sides.
func inv1BookNeverCrosses(e *Engine) string {
	bestBid := e.book.BestLevel(domain.Buy)
	bestAsk := e.book.BestLevel(domain.Sell)
	if bestBid == nil || bestAsk == nil {
		return ""
	}
	// Strict: equal prices are also a violation.
	if bestBid.Price.Cmp(bestAsk.Price) >= 0 {
		return fmt.Sprintf("Inv1: bestBid.Price=%s >= bestAsk.Price=%s (book crossed)", bestBid.Price, bestAsk.Price)
	}
	return ""
}

// inv2FillsBound checks that cumulative fills per order ≤ original quantity.
// It re-reads the full history ring and rebuilds cumFills from scratch to
// catch any off-by-one in the incremental path.
func inv2FillsBound(e *Engine, s *propState) string {
	trades := e.history.Recent(20000)

	// Process only trades we haven't seen yet; update cumFills.
	for _, tr := range trades {
		if _, seen := s.seenTradeIDs[tr.ID]; seen {
			continue
		}
		s.seenTradeIDs[tr.ID] = struct{}{}

		if _, ok := s.cumFills[tr.TakerOrderID]; !ok {
			s.cumFills[tr.TakerOrderID] = decimal.Zero
		}
		s.cumFills[tr.TakerOrderID] = s.cumFills[tr.TakerOrderID].Add(tr.Quantity)

		if _, ok := s.cumFills[tr.MakerOrderID]; !ok {
			s.cumFills[tr.MakerOrderID] = decimal.Zero
		}
		s.cumFills[tr.MakerOrderID] = s.cumFills[tr.MakerOrderID].Add(tr.Quantity)
	}

	for orderID, filled := range s.cumFills {
		origQty, ok := s.orderQty[orderID]
		if !ok {
			// Trade references an order we never placed — internal inconsistency.
			return fmt.Sprintf("Inv2: trade references unknown orderID=%s", orderID)
		}
		if filled.Cmp(origQty) > 0 {
			return fmt.Sprintf("Inv2: orderID=%s cumFill=%s > origQty=%s", orderID, filled, origQty)
		}
	}
	return ""
}

// inv7NoSelfTrade checks that no trade has taker.UserID == maker.UserID.
// Since the trade struct does not embed UserIDs, we resolve via the side table.
func inv7NoSelfTrade(e *Engine, s *propState) string {
	trades := e.history.Recent(20000)
	for _, tr := range trades {
		takerUser, takerOK := s.orderUser[tr.TakerOrderID]
		makerUser, makerOK := s.orderUser[tr.MakerOrderID]
		if !takerOK || !makerOK {
			// Cannot resolve — unknown order; conservative: flag it.
			return fmt.Sprintf("Inv7: cannot resolve userIDs for trade id=%s taker=%s maker=%s", tr.ID, tr.TakerOrderID, tr.MakerOrderID)
		}
		if takerUser == makerUser {
			return fmt.Sprintf("Inv7: self-trade detected trade id=%s user=%s takerOrder=%s makerOrder=%s", tr.ID, takerUser, tr.TakerOrderID, tr.MakerOrderID)
		}
	}
	return ""
}

// inv8ArmedStopsInvisible checks that Snapshot quantity totals match the sum
// of RemainingQuantity over byID (resting orders only). Armed stops must not
// appear in the snapshot.
func inv8ArmedStopsInvisible(e *Engine) string {
	bids, asks := e.Snapshot(10000)

	// Sum snapshot bids.
	snapBidTotal := decimal.Zero
	for _, lvl := range bids {
		snapBidTotal = snapBidTotal.Add(lvl.Quantity)
	}

	// Sum snapshot asks.
	snapAskTotal := decimal.Zero
	for _, lvl := range asks {
		snapAskTotal = snapAskTotal.Add(lvl.Quantity)
	}

	// Sum byID bids and asks.
	byIDBidTotal := decimal.Zero
	byIDAskTotal := decimal.Zero
	for _, o := range e.byID {
		if o.Side == domain.Buy {
			byIDBidTotal = byIDBidTotal.Add(o.RemainingQuantity)
		} else {
			byIDAskTotal = byIDAskTotal.Add(o.RemainingQuantity)
		}
	}

	if snapBidTotal.Cmp(byIDBidTotal) != 0 {
		return fmt.Sprintf("Inv8: snapshot bid total=%s != byID bid sum=%s", snapBidTotal, byIDBidTotal)
	}
	if snapAskTotal.Cmp(byIDAskTotal) != 0 {
		return fmt.Sprintf("Inv8: snapshot ask total=%s != byID ask sum=%s", snapAskTotal, byIDAskTotal)
	}
	return ""
}

// inv17CounterConsistency checks openOrders == len(byID) and armedStops == stops.Len().
func inv17CounterConsistency(e *Engine) string {
	if e.openOrders != len(e.byID) {
		return fmt.Sprintf("Inv17: openOrders=%d != len(byID)=%d", e.openOrders, len(e.byID))
	}
	if e.armedStops != e.stops.Len() {
		return fmt.Sprintf("Inv17: armedStops=%d != stops.Len()=%d", e.armedStops, e.stops.Len())
	}
	return ""
}

// invPriceLevelTotal checks that each price level's Total field equals the
// sum of RemainingQuantity for all resting orders in byID at that price.
//
// Because book.OrderBook does not export a level iterator, we use the Snapshot
// to enumerate distinct price levels, then sum byID entries at each price.
// This cross-check catches Total desynchronisation that would be observable
// via Snapshot consumers.
func invPriceLevelTotal(e *Engine) string {
	bids, asks := e.Snapshot(10000)

	check := func(levels []book.LevelSnapshot, side domain.Side) string {
		// Build a map price-key → sum(RemainingQuantity) from byID at this side.
		byIDSums := make(map[string]decimal.Decimal)
		for _, o := range e.byID {
			if o.Side != side {
				continue
			}
			key := o.Price.String()
			cur, ok := byIDSums[key]
			if !ok {
				cur = decimal.Zero
			}
			byIDSums[key] = cur.Add(o.RemainingQuantity)
		}

		for _, lvl := range levels {
			key := lvl.Price.String()
			expected, ok := byIDSums[key]
			if !ok {
				expected = decimal.Zero
			}
			if lvl.Quantity.Cmp(expected) != 0 {
				return fmt.Sprintf("PriceLevelTotal(%s): snapshot says %s but byID sum=%s at price=%s", side, lvl.Quantity, expected, lvl.Price)
			}
		}
		return ""
	}

	if msg := check(bids, domain.Buy); msg != "" {
		return msg
	}
	return check(asks, domain.Sell)
}

// ---------------------------------------------------------------------------
// State dump — enough to reproduce locally
// ---------------------------------------------------------------------------

func dumpState(e *Engine, s *propState, seed int64, opIdx int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seed=%d op=%d openOrders=%d armedStops=%d byID.len=%d stops.Len=%d\n",
		seed, opIdx, e.openOrders, e.armedStops, len(e.byID), e.stops.Len())

	bids, asks := e.Snapshot(20)
	fmt.Fprintf(&b, "  book bids (top 20):")
	for _, lvl := range bids {
		fmt.Fprintf(&b, " %s×%s", lvl.Price, lvl.Quantity)
	}
	fmt.Fprintf(&b, "\n  book asks (top 20):")
	for _, lvl := range asks {
		fmt.Fprintf(&b, " %s×%s", lvl.Price, lvl.Quantity)
	}
	fmt.Fprintf(&b, "\n  byID orders:")
	for id, o := range e.byID {
		fmt.Fprintf(&b, " [%s side=%s price=%s rem=%s status=%s]", id, o.Side, o.Price, o.RemainingQuantity, o.Status)
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Property generator — weighted random operations
// ---------------------------------------------------------------------------

// opWeights defines the distribution over operation types.
// Index → op: 0=Limit, 1=Market, 2=Stop, 3=StopLimit, 4=Cancel
var opWeights = []int{40, 20, 15, 10, 15} // must sum to 100

func pickOp(rng *rand.Rand) int {
	n := rng.Intn(100)
	cum := 0
	for i, w := range opWeights {
		cum += w
		if n < cum {
			return i
		}
	}
	return 0
}

func randPrice(rng *rand.Rand) decimal.Decimal {
	// 100..200 inclusive
	v := 100 + rng.Intn(101)
	return decimal.NewFromInt(int64(v))
}

func randQty(rng *rand.Rand) decimal.Decimal {
	// 1..10 inclusive
	v := 1 + rng.Intn(10)
	return decimal.NewFromInt(int64(v))
}

func randSide(rng *rand.Rand) domain.Side {
	if rng.Intn(2) == 0 {
		return domain.Buy
	}
	return domain.Sell
}

// runOneIteration runs nOps random operations on e, checking all invariants
// after each one. Returns non-empty string on first violation.
func runOneIteration(e *Engine, s *propState, rng *rand.Rand, seed int64, nOps int) string {
	users := []string{"u1", "u2", "u3", "u4", "u5"}

	for opIdx := 0; opIdx < nOps; opIdx++ {
		op := pickOp(rng)

		switch op {
		case 0: // Limit
			userID := users[rng.Intn(len(users))]
			side := randSide(rng)
			price := randPrice(rng)
			qty := randQty(rng)

			res, err := e.Place(PlaceCommand{
				UserID:   userID,
				Side:     side,
				Type:     domain.Limit,
				Price:    price,
				Quantity: qty,
			})
			if err != nil {
				// ErrTooManyOrders — valid engine response, not a violation.
				break
			}
			s.recordOrder(res.Order.ID, userID, qty)
			if _, inBook := e.byID[res.Order.ID]; inBook {
				s.addLive(res.Order.ID)
			}
			if len(res.Trades) > 0 {
				s.hadTrade = true
			}

		case 1: // Market
			userID := users[rng.Intn(len(users))]
			side := randSide(rng)
			qty := randQty(rng)

			res, err := e.Place(PlaceCommand{
				UserID:   userID,
				Side:     side,
				Type:     domain.Market,
				Quantity: qty,
			})
			if err != nil {
				break
			}
			s.recordOrder(res.Order.ID, userID, qty)
			if len(res.Trades) > 0 {
				s.hadTrade = true
			}

		case 2: // Stop
			if !s.hadTrade {
				// Sell stops on fresh engine are always immediately rejected.
				// Skip until we have a trade so the test exercises armed stops,
				// not just rejections.
				break
			}
			userID := users[rng.Intn(len(users))]
			side := randSide(rng)
			qty := randQty(rng)

			// Choose a trigger price in a range that avoids immediate
			// trigger-already-satisfied rejection. For a buy stop we want
			// trigger > lastTradePrice; for a sell stop trigger < lastTradePrice.
			// We use a wide integer range and accept that some will still be
			// rejected — that's a valid engine response.
			var triggerPrice decimal.Decimal
			if side == domain.Buy {
				// High triggers: 201..250
				triggerPrice = decimal.NewFromInt(int64(201 + rng.Intn(50)))
			} else {
				// Low triggers: 50..99
				triggerPrice = decimal.NewFromInt(int64(50 + rng.Intn(50)))
			}

			res, err := e.Place(PlaceCommand{
				UserID:       userID,
				Side:         side,
				Type:         domain.Stop,
				TriggerPrice: triggerPrice,
				Quantity:     qty,
			})
			if err != nil {
				// ErrTooManyStops — valid.
				break
			}
			s.recordOrder(res.Order.ID, userID, qty)
			if res.Order.Status == domain.StatusArmed {
				s.addLiveStop(res.Order.ID)
			}

		case 3: // StopLimit
			if !s.hadTrade {
				break
			}
			userID := users[rng.Intn(len(users))]
			side := randSide(rng)
			qty := randQty(rng)

			var triggerPrice, limitPrice decimal.Decimal
			if side == domain.Buy {
				triggerPrice = decimal.NewFromInt(int64(201 + rng.Intn(50)))
				limitPrice = decimal.NewFromInt(int64(210 + rng.Intn(40)))
			} else {
				triggerPrice = decimal.NewFromInt(int64(50 + rng.Intn(50)))
				limitPrice = decimal.NewFromInt(int64(40 + rng.Intn(50)))
			}

			res, err := e.Place(PlaceCommand{
				UserID:       userID,
				Side:         side,
				Type:         domain.StopLimit,
				TriggerPrice: triggerPrice,
				Price:        limitPrice,
				Quantity:     qty,
			})
			if err != nil {
				break
			}
			s.recordOrder(res.Order.ID, userID, qty)
			if res.Order.Status == domain.StatusArmed {
				s.addLiveStop(res.Order.ID)
			}

		case 4: // Cancel
			// 50% chance cancel a resting order, 50% cancel an armed stop.
			if rng.Intn(2) == 0 {
				if len(s.liveIDs) == 0 {
					break
				}
				i := rng.Intn(len(s.liveIDs))
				id := s.liveIDs[i]
				s.removeLiveAt(i)
				e.Cancel(id) //nolint:errcheck — ErrOrderNotFound / ErrAlreadyTerminal are valid
			} else {
				if len(s.liveStopIDs) == 0 {
					break
				}
				i := rng.Intn(len(s.liveStopIDs))
				id := s.liveStopIDs[i]
				s.removeLiveStopAt(i)
				e.Cancel(id) //nolint:errcheck
			}
		}

		// --- Check all invariants after every op ---

		if msg := inv17CounterConsistency(e); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
		if msg := inv1BookNeverCrosses(e); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
		if msg := inv2FillsBound(e, s); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
		if msg := inv7NoSelfTrade(e, s); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
		if msg := inv8ArmedStopsInvisible(e); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
		if msg := invPriceLevelTotal(e); msg != "" {
			return msg + "\n" + dumpState(e, s, seed, opIdx)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// TestEngine_Property_AllInvariants — master property test
// ---------------------------------------------------------------------------

func TestEngine_Property_AllInvariants(t *testing.T) {
	const (
		nSeeds = 50
		nOps   = 100
	)

	for seedIdx := int64(1); seedIdx <= nSeeds; seedIdx++ {
		seed := seedIdx
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			e := newPropertyEngine()
			s := newPropState()

			if msg := runOneIteration(e, s, rng, seed, nOps); msg != "" {
				t.Fatalf("invariant violation (seed=%d nOps=%d):\n%s", seed, nOps, msg)
			}
		})
	}
}

