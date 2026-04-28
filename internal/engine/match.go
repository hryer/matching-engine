// Package engine — match.go owns the price-time-FIFO matching kernel and the
// stop-cascade machinery.
//
// DEVIATION FROM §04 PSEUDOCODE (T-010-PLAN.md §3, "Key contract subtlety").
//
// The §04 pseudocode places the rest step (book.Insert + status assignment for
// Resting / PartiallyFilled) inside match. This file deliberately does NOT do
// that for the Limit case. Rationale:
//
//  1. match is called from two sites: Place and drainTriggeredStops.
//  2. Place must gate book.Insert on the openOrders cap (ErrTooManyOrders) and
//     choose between StatusResting, StatusPartiallyFilled-and-rest, and
//     StatusPartiallyFilled-truncated depending on the cap. That policy is a
//     user-call-boundary concern, not a matching-kernel concern.
//  3. drainTriggeredStops re-enters match for triggered stop-limits, but there
//     the cap is intentionally not applied (cascade overshoots are accepted —
//     see T-010-PLAN.md §4 "cascade-trigger-stoplimit-becomes-limit-rests").
//
// Therefore match sets terminal status ONLY for:
//   - StatusFilled:          incoming.RemainingQuantity.IsZero() after the loop.
//   - StatusCancelled:       STP (self-match) detected at head of FIFO.
//   - StatusRejected:        Market with zero trades (no liquidity).
//   - StatusPartiallyFilled: Market with trades but remaining > 0.
//
// For Limit orders that exit the loop with remaining > 0 (would rest), match
// leaves Status unset. The caller (Place or drainTriggeredStops) inspects
// (incoming.Status, len(trades), incoming.RemainingQuantity) and assigns the
// correct terminal status together with the optional book.Insert.
//
// Pre-condition for all helpers in this file: e.mu is held by the caller.
// None of these functions acquire or release the lock.
package engine

import (
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// match runs the price-time-FIFO matching loop against the opposite side of
// the book. It returns the trades produced. It mutates incoming.RemainingQuantity
// and sets incoming.Status terminally for the Filled, Cancelled (STP), Rejected
// (Market-no-liquidity), and Market-PartiallyFilled cases. It does NOT set
// terminal status for Limit orders that exit with remaining > 0 — see the
// package-level comment above and T-010-PLAN.md §3.
//
// match performs all maker-side mutations inline: it decrements
// maker.RemainingQuantity and level.Total per fill, calls
// book.RemoveFilledMaker on a fully-consumed maker (which also deletes the
// level when empty), sets maker.Status = StatusFilled, removes the maker from
// e.byID, and decrements e.openOrders.
//
// appendTrade is called per trade, which drives updateLastTradePrice and the
// stop cascade (drainTriggeredStops). No allocations occur beyond the trades
// slice itself (grown at most once per fill iteration via append).
//
// Pre: e.mu held. No goroutines spawned. No calls to public Engine methods.
func (e *Engine) match(incoming *domain.Order) []*domain.Trade {
	var trades []*domain.Trade

	// Determine which side of the book the opposing (maker) orders rest on.
	// For a Buy taker, makers are on the Sell (ask) side; for a Sell taker,
	// makers are on the Buy (bid) side.
	// BestLevel(side) returns the best level on the named side, so we pass
	// the maker side directly (see book.go "BestLevel" comment).
	makerSide := domain.Sell
	if incoming.Side == domain.Sell {
		makerSide = domain.Buy
	}

	for !incoming.RemainingQuantity.IsZero() {
		bestLevel := e.book.BestLevel(makerSide)
		if bestLevel == nil {
			// No liquidity on the opposite side.
			break
		}

		// Limit price gate — Market orders skip this block and always cross.
		if incoming.Type == domain.Limit {
			if incoming.Side == domain.Buy && incoming.Price.LessThan(bestLevel.Price) {
				// Taker's limit is below the best ask — will not cross.
				break
			}
			if incoming.Side == domain.Sell && incoming.Price.GreaterThan(bestLevel.Price) {
				// Taker's limit is above the best bid — will not cross.
				break
			}
		}

		// Recover the head maker from the FIFO at this level.
		maker := bestLevel.Orders.Front().Value.(*domain.Order)

		// Self-match prevention: cancel-newest.
		// Detect BEFORE computing fillQty so no trade is ever emitted against
		// an own maker. Prior trades against other makers in this call are
		// kept. The maker is untouched — it remains at the head of the FIFO.
		// Per plan §6: do NOT skip this maker to find a non-self maker below it.
		if maker.UserID == incoming.UserID {
			incoming.Status = domain.StatusCancelled
			return trades
		}

		// Compute fill quantity: limited by whichever side is smaller.
		fillQty := incoming.RemainingQuantity
		if maker.RemainingQuantity.LessThan(fillQty) {
			fillQty = maker.RemainingQuantity
		}

		// Construct the trade. Price is always the maker's resting price
		// (standard exchange convention — see §04 "Trade price decision").
		t := &domain.Trade{
			ID:           e.ids.NextTradeID(),
			TakerOrderID: incoming.ID,
			MakerOrderID: maker.ID,
			Price:        maker.Price,
			Quantity:     fillQty,
			TakerSide:    incoming.Side,
			CreatedAt:    e.clock.Now(),
		}

		// Update remaining quantities and the level running total.
		// NOTE: level.Total is decremented HERE by the matcher — NOT inside
		// book.RemoveFilledMaker. Read the RemoveFilledMaker comment in book.go
		// carefully before changing this ordering.
		incoming.RemainingQuantity = incoming.RemainingQuantity.Sub(fillQty)
		maker.RemainingQuantity = maker.RemainingQuantity.Sub(fillQty)
		bestLevel.Total = bestLevel.Total.Sub(fillQty)

		// Maker fully consumed: remove from book, e.byID, and decrement
		// openOrders. RemoveFilledMaker handles FIFO removal and level pruning;
		// it does NOT touch level.Total (already done above).
		// Maker partially consumed: status update only — it stays in the FIFO.
		if maker.RemainingQuantity.IsZero() {
			maker.Status = domain.StatusFilled
			e.book.RemoveFilledMaker(bestLevel, maker)
			delete(e.byID, maker.ID)
			e.openOrders--
		} else {
			maker.Status = domain.StatusPartiallyFilled
		}

		// Publish the trade and drive the stop cascade. appendTrade calls
		// updateLastTradePrice which calls drainTriggeredStops.
		e.appendTrade(t)
		trades = append(trades, t)
	}

	// Set terminal status for the incoming order. Only the cases enumerated in
	// the package-level comment above are handled here. The Limit-with-remaining
	// case is left to the caller (Place or drainTriggeredStops) — see
	// T-010-PLAN.md §3 "Key contract subtlety".
	switch {
	case incoming.RemainingQuantity.IsZero():
		// Fully filled — applies to both Limit and Market.
		incoming.Status = domain.StatusFilled
	case incoming.Type == domain.Market && len(trades) == 0:
		// Market with no liquidity at all.
		incoming.Status = domain.StatusRejected
	case incoming.Type == domain.Market:
		// Market, partial fill — remainder is dropped, NOT rested.
		incoming.Status = domain.StatusPartiallyFilled
	}
	// Limit with remaining > 0: status intentionally left unset for caller.

	return trades
}

// appendTrade publishes the trade to the history ring and drives the last-trade
// price update (which may cascade-trigger armed stops).
//
// Exactly two operations, in this order:
//  1. e.history.Publish(t)
//  2. e.updateLastTradePrice(t.Price)
//
// Pre: e.mu held.
func (e *Engine) appendTrade(t *domain.Trade) {
	e.history.Publish(t)
	e.updateLastTradePrice(t.Price)
}

// updateLastTradePrice sets the engine's last-trade-price reference and
// immediately drains any stops whose trigger condition is now satisfied.
//
// Pre: e.mu held.
func (e *Engine) updateLastTradePrice(p decimal.Decimal) {
	e.lastTradePrice = p
	e.drainTriggeredStops()
}

// drainTriggeredStops pops all stops whose trigger is satisfied at the current
// lastTradePrice, rewrites their Type (Stop → Market, StopLimit → Limit), and
// re-enters match for each. The cascade terminates naturally because
// stops.DrainTriggered removes each order from stops.byID before this function
// calls match — so no stop can fire itself again in the same cascade sweep.
//
// Counter accounting (per T-010-PLAN.md §4):
//   - armedStops decremented by 1 per drained order (before match).
//   - openOrders incremented by 1 when a triggered stop-limit ends up resting
//     (Limit with no fill, OR Limit with partial fill). No cap-check in cascade.
//   - openOrders unchanged for Stop→Market outcomes (Market never rests).
//
// e.byID is written only in the "triggered stop-limit rests" path, after
// e.book.Insert. This is the ONLY place in match.go that writes to e.byID.
//
// Pre: e.mu held. No goroutines spawned.
func (e *Engine) drainTriggeredStops() {
	triggered := e.stops.DrainTriggered(e.lastTradePrice)
	// triggered is already sorted by ascending seq — do not re-sort.

	for _, o := range triggered {
		e.armedStops--

		// Rewrite Type: Stop → Market, StopLimit → Limit. The order now
		// participates in match as a plain Market or Limit order.
		switch o.Type {
		case domain.Stop:
			o.Type = domain.Market
		case domain.StopLimit:
			o.Type = domain.Limit
		}

		// Re-enter match. match may itself call appendTrade → updateLastTradePrice
		// → drainTriggeredStops recursively, but each recursive DrainTriggered
		// call operates on stops not yet removed — so no stop can be drained
		// twice. The cascade terminates by induction on the number of armed stops.
		//
		// No goroutine. All work runs on the caller's goroutine under e.mu.
		// This is mandated by T-010-PLAN.md §5 (concurrency contract) and §8
		// (determinism: "go func() is banned in this package").
		trades := e.match(o)

		// Post-match: decide whether the triggered order rests.
		//
		// For Stop→Market outcomes (Filled, PartiallyFilled, Rejected), and for
		// StopLimit→Limit that fully filled (StatusFilled) or self-matched
		// (StatusCancelled): terminal status was already set by match; nothing
		// more to do.
		//
		// For StopLimit→Limit that did not fully fill: match leaves status
		// unset (plan §3). drainTriggeredStops resolves it here, without a
		// cap-check (plan §4, cascade row).
		switch {
		case o.Status == domain.StatusFilled ||
			o.Status == domain.StatusCancelled ||
			o.Status == domain.StatusRejected:
			// Terminal status set by match. No book insert.

		case o.Type == domain.Limit && len(trades) == 0:
			// Stop-limit triggered but no opposing liquidity — rests at full
			// original size. No cap-check in cascade (T-010-PLAN.md §4).
			o.Status = domain.StatusResting
			e.book.Insert(o)
			e.byID[o.ID] = o
			e.openOrders++

		case o.Type == domain.Limit:
			// Stop-limit triggered and partial-filled — remainder rests.
			// No cap-check in cascade (T-010-PLAN.md §4).
			o.Status = domain.StatusPartiallyFilled
			e.book.Insert(o)
			e.byID[o.ID] = o
			e.openOrders++

		case o.Type == domain.Market:
			// Stop→Market that partial-filled: match already set
			// StatusPartiallyFilled. Remainder is dropped; Market never rests.
			// This case is reachable only if match returned trades but
			// RemainingQuantity > 0 on a Market — which match handles inline.
			// No action needed; status was set by match.
		}
	}
}
