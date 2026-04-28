// Package engine — resource cap and adversarial cap tests.
package engine

import (
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/adapters/publisher/inmem"
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// ---------------------------------------------------------------------------
// Cap — ErrTooManyOrders on 3rd Limit with MaxOpenOrders=2
// ---------------------------------------------------------------------------

func TestEngine_Cap_TooManyOrders_ThirdLimitRejected(t *testing.T) {
	e := newTestEngine(t, 2, 100)
	placeLimit(t, e, "alice", domain.Buy, "99", "1")
	placeLimit(t, e, "alice", domain.Buy, "98", "1")
	checkInvariants(t, e)
	if e.openOrders != 2 {
		t.Fatalf("openOrders should be 2, got %d", e.openOrders)
	}

	_, err := e.Place(PlaceCommand{
		UserID:   "alice",
		Side:     domain.Buy,
		Type:     domain.Limit,
		Price:    mustDec("97"),
		Quantity: mustDec("1"),
	})
	if err != ErrTooManyOrders {
		t.Fatalf("want ErrTooManyOrders, got %v", err)
	}
	// State must be unchanged.
	checkInvariants(t, e)
	if e.openOrders != 2 {
		t.Fatalf("openOrders must remain 2 after cap-hit, got %d", e.openOrders)
	}
	// No trade emitted.
	if len(e.Trades(10)) != 0 {
		t.Fatalf("no trades should have been emitted")
	}
}

// ---------------------------------------------------------------------------
// Cap — ErrTooManyStops on 2nd Stop with MaxArmedStops=1
// ---------------------------------------------------------------------------

func TestEngine_Cap_TooManyStops_SecondStopRejected(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 1)
	placeStop(t, e, "alice", domain.Buy, "110", "5")
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}

	_, err := e.Place(PlaceCommand{
		UserID:       "bob",
		Side:         domain.Buy,
		Type:         domain.Stop,
		TriggerPrice: mustDec("115"),
		Quantity:     mustDec("5"),
	})
	if err != ErrTooManyStops {
		t.Fatalf("want ErrTooManyStops, got %v", err)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops must remain 1 after cap-hit, got %d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// Decision (b): partial-fill-then-cap-hit (T-010-PLAN.md §1 and §9).
//
// The cap must fire on the REMAINDER after a partial fill. This requires
// openOrders == maxOpenOrders at the time of the remainder check — i.e., the
// partial fill did NOT consume all resting makers, so openOrders never dropped
// to zero.
//
// Scenario:
//   MaxOpenOrders=1.
//   Step 1: Arm a sell stop-limit (trigger=40 < lastTradePrice=50 at time of fire).
//           Actually: arm a buy stop-limit(trigger=80, limit=200) and a sell
//           stop-limit(trigger=30, limit=5) — too complex. Simplify:
//
//   Use a Market taker to drive lastTradePrice to produce a resting survivor.
//   Market orders never rest, so they can't fill the cap. But sell orders placed
//   to match a market also never rest (they're consumed by the market). Hmm.
//
//   Direct scenario that actually works:
//     MaxOpenOrders=2.
//     Place sell A at 100 qty=3 (openOrders=1).
//     Place sell B at 105 qty=10 (openOrders=2).  ← uses second slot.
//     Taker: buy limit 6 at 104 (crosses A; price 104 < 105 so does NOT cross B).
//       A fully consumed (openOrders→1). Taker remaining=3.
//       Cap: openOrders(1)+1=2 > maxOpenOrders(2) → false. Remainder rests. Not cap-hit.
//
//     Still not cap-hit. We need maxOpenOrders=1 with openOrders=1 after fills.
//
//   The only correct path: maker is PARTIALLY consumed (stays resting), so openOrders=1.
//     MaxOpenOrders=1. Maker sell 10 at 100. openOrders=1.
//     Taker: buy market qty=3. Fills 3 from maker. Maker remaining=7, PartiallyFilled,
//     still resting (openOrders=1). Taker fully consumed (Filled). No remainder on taker.
//   A Market taker has no remainder to rest.
//
//   For a LIMIT taker with both a partial fill AND a remainder:
//     Maker sell 10 at 100. Taker buy limit 103 qty=5.
//       Wait — taker crosses all of maker's 10? No: taker qty=5, maker qty=10.
//       fillQty=min(5,10)=5. Maker remaining=5. Taker fully consumed. Filled.
//     Taker qty=15 at 103:
//       fillQty=min(15,10)=10. Maker fully consumed (openOrders→0). Taker remaining=5.
//       Cap: 0+1=1 <= 1. Rests. NOT cap-hit.
//
//   THE KEY INSIGHT: for cap-hit on a partial-fill remainder, we need the maker to
//   survive AND taker to have remaining AND cap to block the rest. This requires
//   the taker to exhaust some makers (possibly partially) and STILL have remaining,
//   AND openOrders to be at max. The only way openOrders stays at max after fills
//   is if another resting order (not the consumed maker) holds a slot.
//
//   Use CASCADE to plant a second resting order without burning a cap slot:
//     MaxOpenOrders=1. lastTradePrice=100 (from setup trade, openOrders=0).
//     Arm sell stop-limit: trigger<lastTradePrice to arm.
//       Sell stop arms when trigger < lastTradePrice. trigger=40 < 100. Arms.
//     Drive trade at 30: price drops to 30. Sell stop fires (40 >= 30).
//       Becomes Limit(105 sell). No bids at 105. Rests. openOrders→1 (cascade bypass).
//     Now plant a sell at 100 (would exceed cap, but... can't).
//     This gets circular.
//
//   FINAL WORKING APPROACH: use two sells at different prices, with the taker
//   crossing only the cheaper one but being price-limited so it can't reach the second.
//   The SECOND sell provides the openOrders counter. The first sell is partially filled.
//
//   MaxOpenOrders=2.
//   Sell A at 100 qty=5 (openOrders=1).
//   Sell B at 102 qty=10 (openOrders=2). ← this is the slot-holder.
//   Taker: buy limit 101 qty=8.
//     Crosses A (100 <= 101). fillQty=min(8,5)=5. A fully consumed (openOrders→1).
//     Taker remaining=3. Next: B at 102. 102 <= 101? NO (taker limit=101 < 102). Stop.
//     Cap: openOrders(1)+1=2 > maxOpenOrders(2) → false. Still rests. Not cap-hit.
//
//   MaxOpenOrders=1 with cascade planting B:
//     Setup trade at 50. openOrders=0.
//     Arm sell stop-limit: trigger=40 (40 < 50, so arms). limit=102.
//     Sell A at 100 (openOrders=1). ← uses the slot.
//     Drive trade at 30: lastTradePrice=30. Sell stop fires (40 >= 30).
//       B becomes Limit(102 sell). No bids at 102. Cascade rests. openOrders=2.
//     Now: openOrders=2 (A@100 + B@102). maxOpenOrders=1.
//     Taker: buy limit 101 qty=8.
//       Crosses A (100<=101). fillQty=min(8,1)=1. A fully consumed (openOrders→1).
//       Wait A has qty=1? I said "Sell A at 100" but need qty properly.
//       A qty=1: A fully consumed, openOrders→1. Taker remaining=7. B at 102>101. Stop.
//       Cap: 1+1=2 > 1 → HIT. Decision (b)!
//     A qty=5: crossing fills 5, A consumed, openOrders→1. Taker remaining=3.
//       B at 102>101. Cap: 1+1=2>1 → HIT. Decision (b)!
//
//   This is the working scenario. We need to drive lastTradePrice to 30 to fire
//   the sell stop, but to do that we need a sell at 30 and a buy at 30. Placing
//   the sell at 30 will hit the cap (openOrders=1 already). Use Market buy to
//   consume the sell at 30 without resting: place Market sell at 30 — but Market
//   never rests, so Market Sell won't add to openOrders. Wait: place Market sell
//   at 30, but there are no bids at 30. Market sell → Rejected. That doesn't work.
//
//   To drive lastTradePrice to 30: we need a bid at 30 AND an ask at 30. The ask
//   can be a Market sell (no resting needed). The bid must be Limit(30) which rests.
//   But Limit(30) would hit the cap (openOrders=1). Unless we cancel A first.
//
//   Cancel A, drive trade at 30, then try to re-place A... but then cascade fires
//   and B rests, and we try to place A again but cap is now hit (B in slot).
//
//   We're going in circles. The cleanest approach:
//     Don't have A resting when we drive the trigger trade. Cancel A before the
//     trigger trade. After cascade, B rests (openOrders=1 from cascade bypass).
//     Re-place A after cascade — but cap hit! (openOrders=1=maxOpenOrders=1).
//     Use a second slot from cascade by having TWO stop-limits.
//
//   Truly simplest: MaxOpenOrders=2. Two cascade resting orders. Then add an
//   aggressive Limit taker that crosses one but not the other, with cap blocking.
//   The cap is openOrders(1)+1=2 > maxOpenOrders(2) = false. NO cap.
//
//   CONCLUSION: decision (b) cap-hit on a partial fill is ONLY achievable when:
//   - openOrders after all fills = maxOpenOrders, AND
//   - taker still has remaining quantity.
//
//   This requires the number of surviving resting orders after fills to equal
//   maxOpenOrders. The ONLY way to place more orders than maxOpenOrders is via
//   cascade bypass. Two cascaded resting orders + MaxOpenOrders=1:
//   openOrders=2 after cascades. Taker crosses one of the two, openOrders→1.
//   Remaining cap check: 1+1=2>1 → HIT. Decision (b) fires.
//
// See the test body for the concrete implementation.
// ---------------------------------------------------------------------------

func TestEngine_Cap_DecisionB_PartialFillCapHit_TradeKept_NoError(t *testing.T) {
	// MaxOpenOrders=1. We use cascade to plant two resting sells (bypassing cap).
	// Then a buy taker crosses the cheaper one, has remainder, cap hits on the rest.

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := New(Deps{
		Clock:         clk,
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(500),
		MaxOpenOrders: 1,
		MaxArmedStops: 100,
	})

	// Establish lastTradePrice=100 via a setup trade (uses 2 IDs).
	e.Place(PlaceCommand{UserID: "setup-s", Side: domain.Sell, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)}) //nolint
	e.Place(PlaceCommand{UserID: "setup-b", Side: domain.Buy, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)})   //nolint
	// openOrders=0. lastTradePrice=100.

	// Arm TWO buy stop-limits.
	// Stop-limit X: trigger=110, limit=115. Will rest as sell? No — buy stop-limits
	// become Limit BUY orders when fired. A buy limit at 115 with no asks below 115.
	// Stop-limit Y: trigger=110, limit=116. Same — becomes Limit BUY at 116.
	// Neither has ask liquidity at 115 or 116, so both rest.
	//
	// After driving a trade at 110: both X and Y fire (trigger=110<=110).
	// X becomes Limit(Buy,115): no asks at <=115. Rests. openOrders→1.
	// Y becomes Limit(Buy,116): no asks at <=116. Cascade rests. openOrders→2.
	// Now openOrders=2 > maxOpenOrders=1.
	//
	// Now place a sell at 115 (to provide ask liquidity for X).
	// But cap is hit (openOrders=2, maxOpenOrders=1). ErrTooManyOrders.
	//
	// So we need the ask liquidity for X to already be in place BEFORE X fires.
	// Revise: ask liquidity at 115 before arming X. But placing it first would
	// hit the cap or not (openOrders=0 at that point). Place it while slot is free!
	//
	// Timeline:
	//   openOrders=0 after setup.
	//   Place Sell Z at 115 qty=1. openOrders=1.    ← uses the one slot.
	//   Arm stop-limit X: trigger=110, limit=120.    ← arms (no cap on stops).
	//   Arm stop-limit Y: trigger=110, limit=121.    ← arms.
	//   Drive trade at 110:
	//     lastTradePrice=110. X fires (110<=110) before Y (same trigger, lower seq).
	//     X becomes Limit(Buy,120). Best ask=Z@115. 115<=120 → fills. Trade at 115.
	//     Z consumed. openOrders→0. lastTradePrice=115. Drain: no new triggers (Y: 110<=115 → Y fires!).
	//     Y fires. Y becomes Limit(Buy,121). No asks. Rests. openOrders→1 (cascade).
	//     Drain done.
	//   Now: openOrders=1 (Y resting at 121). maxOpenOrders=1.
	//   Hmm, X fully consumed Z and X was Filled. We have openOrders=1 = maxOpenOrders.
	//   No partial-fill scenario yet.
	//
	// I need X to PARTIALLY fill Z (not fully), so openOrders stays at 1 (Z still alive).
	// X qty=1, Z qty=5: X fills 1 from Z. Z remaining=4, PartiallyFilled, still resting.
	// openOrders=1 (Z still alive). X fully consumed (Filled). lastTradePrice=115.
	// Drain: Y fires (110<=115). Y becomes Limit(Buy,121). No asks (Z is a sell at 115,
	// but Y is a BUY at 121 — it WOULD cross Z if 115<=121! Y crosses Z.
	// fillQty=min(3,4)=3 (Y qty=3, Z remaining=4). Z remaining=1. Y Filled. Trade at 115.
	// lastTradePrice=115. Drain again: still 110<=115, but Y already drained. Done.
	// openOrders=1 (Z remaining=1, still resting). Both X,Y consumed (Filled). armedStops=0.
	// Still not seeing partial-fill-then-cap.
	//
	// THE ACTUAL CLEAN SCENARIO (finally):
	//   MaxOpenOrders=1.
	//   After cascade, openOrders=2 (two buy resting at 115 and 116).
	//   Now place a sell at 115 qty=1. Cap hit (openOrders=2>1). ErrTooManyOrders.
	//   Instead: sell already in place (openOrders... can't, cap=1).
	//
	// I'm going to use a different, simpler approach that genuinely tests decision (b):
	// MaxOpenOrders=2 with cascade overshoot to 3, then a taker fills one, remainder hits cap.

	// REVISED FINAL SCENARIO:
	// MaxOpenOrders=2.
	// Cascade via stop-limits to plant 3 resting buys (openOrders=3 > 2).
	// Then place a sell that crosses one (openOrders→2). Sell taker remaining crosses
	// another (openOrders→1). Sell remaining quantity hits cap: 1+1=2<=2. No cap.
	//
	// MaxOpenOrders=1. Plant 2 cascade buys (openOrders=2>1).
	// Sell qty=big crosses buy-A (openOrders→1). Sell remaining. Cap: 1+1=2>1. HIT!
	//
	// Let's do this: arm two buy stop-limits that both rest (no ask liquidity).
	// Drive a trade at 110 to fire them. Both rest. openOrders=2.
	// Then place a sell at 115 qty=1: cap-hit (can't). Use cascade or existing sell?
	// Actually we don't need to place a sell separately. The cascaded buys at 115/116
	// are the MAKERS. We then place a sell taker that crosses buy-A@115 (lower price),
	// then has remaining which would cross buy-B@116. Cap hits on buy-B's fill.
	// Wait — taker is a SELL, buy-A@115 is the maker. For a sell taker to cross
	// buy-A, the sell's price must be <= buy-A's price. If sell limit price=100,
	// it will cross both buy-A@115 and buy-B@116 (both > 100). Not partial.
	// If sell limit price=115: crosses buy-A@115 (115 >= 115). After fill, remaining.
	// Next maker is buy-B@116. 116 >= 115 (sell gate: sell crosses when Price.LessThan(bestBid)).
	// Wait: sell taker limit gate is `incoming.Price.GreaterThan(bestLevel.Price)` → break.
	// For sell taker at price=115: bestLevel is buy side, best bid = 116 (buy-B).
	// Gate: sell.Price(115) > bestBid(116)? No (115 < 116). So it CROSSES buy-B!
	// So sell@115 would cross both 116 and 115. That's a full fill. No remainder.
	// Sell qty must exceed total liquidity for remainder. buy-A qty=3, buy-B qty=3.
	// Sell qty=10: crosses buy-A (3 filled, openOrders→1), crosses buy-B (3 filled, openOrders→0).
	// All liquidity consumed. Remaining=4. Cap: 0+1=1>1? No (0+1=1 not >1). Rests. No cap.
	// MaxOpenOrders=0: every placement fails. No good.
	//
	// OK I think I need to re-read the plan more carefully.
	// Plan §9: "MaxOpenOrders=1, place a resting limit, then a cross taker that
	// partial-fills resting AND would rest its own remainder."
	// "Expect: trade emitted, taker PartiallyFilled, taker NOT in book, openOrders==0
	// (resting consumed; new limit truncated). NO error returned."
	//
	// So the plan's own example has openOrders==0 at the end! The maker IS fully
	// consumed. The cap-hit happens because AFTER the maker is consumed
	// (openOrders→0), the check is 0+1=1 which compared to maxOpenOrders=1 gives
	// 1>1 = FALSE. The remainder rests! openOrders=1.
	//
	// Wait... the plan says "openOrders==0 (resting consumed; new limit truncated)".
	// If openOrders==0 at the end, then the taker IS NOT resting. So the cap DID fire.
	// 0+1=1 > 1 is FALSE. That would mean the taker DOES rest. Contradiction.
	//
	// Unless MaxOpenOrders=0! With maxOpenOrders=0: 0+1=1 > 0 → TRUE. Cap hits.
	// But placing the initial resting limit with maxOpenOrders=0 would also fail.
	// Unless the initial limit is placed via cascade.
	//
	// OR: the plan's §9 example uses MaxOpenOrders=1 with the maker qty=something
	// and taker qty=something such that the maker is fully consumed AND the
	// cap check uses the PRE-consume value. But the plan explicitly says
	// "match runs before cap check" and "cap check is on post-match state".
	//
	// I think there may be an inconsistency in the plan's §9 description.
	// The plan at §7 Step 2b says:
	//   "case order.Type == domain.Limit:
	//      if e.openOrders+1 > e.maxOpenOrders: truncate, no error
	//      else: rest"
	// After the maker is fully consumed: openOrders=0. 0+1=1>1 → false. Remainder rests.
	//
	// The plan's §9 test expects "openOrders==0". This is only achievable if
	// maxOpenOrders=0 OR if there's another resting order keeping the slot full.
	//
	// Given the evidence, I believe the plan's §9 test description for decision (b)
	// intends: after the partial fill that DOES cap-hit, openOrders reflects
	// the state BEFORE the taker's remainder would have added. If the maker was
	// NOT fully consumed (maker stays resting), openOrders stays at 1, and
	// cap-check: 1+1=2>1 → HIT. Taker not rested. openOrders stays 1.
	// At end: openOrders=1 (just the surviving partial maker). "openOrders==0"
	// in the plan might be a typo or refers to a different interpretation.
	//
	// I will implement the test that matches the CODE CONTRACT (not the plan description):
	//   Maker partially filled (stays resting, openOrders=1).
	//   Taker has remaining. Cap: 1+1=2>1 → HIT.
	//   Taker PartiallyFilled, not rested. openOrders=1 (maker still alive). No error.

	// Reset engine for the actual test.
	e2 := New(Deps{
		Clock:         clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(500),
		MaxOpenOrders: 1,
		MaxArmedStops: 100,
	})

	// Place a resting sell with qty=10 at 100. openOrders=1.
	maker := placeLimit(t, e2, "alice", domain.Sell, "100", "10")
	if e2.openOrders != 1 {
		t.Fatalf("setup: openOrders should be 1, got %d", e2.openOrders)
	}

	// Taker: buy market qty=3.
	// Market taker fills 3 from alice. alice remaining=7, still resting (openOrders=1).
	// Taker Filled (market taker qty=3 exhausted). No remainder on taker.
	// → This tests partial fill of maker but taker has no remainder.
	_ = maker

	// For decision (b): we need a LIMIT taker with remainder after a partial fill.
	// Limit taker buy qty=3 at 100 — fully fills the taker (3 < 10). No remainder.
	// Limit taker buy qty=15 at 100 — fully fills alice (10 < 15). Taker remaining=5.
	//   alice consumed (openOrders→0). Cap: 0+1=1 <= 1. Taker rests. NOT cap-hit.
	//
	// The only remaining option: maker partial fill + openOrders stays at max.
	// Place two resting sells at 100 each, using cascade to bypass cap.
	// Then taker crosses one fully (openOrders→1), then... cap check for taker itself.
	//
	// After much analysis: the plan §9 "decision (b)" test is correctly described by
	// the two-cascade scenario below. The "openOrders==0" in the plan is an error
	// in the plan — the code implements option (b) where partial fills are kept and
	// cap-hit means "truncate remainder". With MaxOpenOrders=1 and maker partially
	// surviving (openOrders=1 after fills), cap fires, openOrders stays 1. No error.

	// Cascade setup: arm two buy stop-limits at the same trigger.
	// Drive a trade to fire both. Both rest (no asks). openOrders=2 (cascade bypass).
	// Now place a sell that crosses cheaper buy, cap hits for taker remainder.

	e3 := New(Deps{
		Clock:         clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(500),
		MaxOpenOrders: 1,
		MaxArmedStops: 100,
	})

	// Setup trade at 100 (establishes lastTradePrice=100).
	e3.Place(PlaceCommand{UserID: "s0", Side: domain.Sell, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)}) //nolint
	e3.Place(PlaceCommand{UserID: "b0", Side: domain.Buy, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)})   //nolint

	// Arm two buy stop-limits: trigger=110 (> 100), no ask at 115/116.
	// Both will rest as Limit(Buy, 115) and Limit(Buy, 116).
	e3.Place(PlaceCommand{UserID: "slX", Side: domain.Buy, Type: domain.StopLimit, TriggerPrice: mustDec("110"), Price: mustDec("115"), Quantity: mustDec("5")}) //nolint
	e3.Place(PlaceCommand{UserID: "slY", Side: domain.Buy, Type: domain.StopLimit, TriggerPrice: mustDec("110"), Price: mustDec("116"), Quantity: mustDec("5")}) //nolint
	if e3.armedStops != 2 {
		t.Fatalf("decision-b setup: armedStops should be 2, got %d", e3.armedStops)
	}

	// Drive a trade at 110 to fire both stop-limits.
	// Sell at 110 and buy at 110. But selling at 110 with openOrders=0 is fine (rests).
	e3.Place(PlaceCommand{UserID: "sell110", Side: domain.Sell, Type: domain.Limit, Price: mustDec("110"), Quantity: decimal.NewFromInt(1)}) //nolint
	e3.Place(PlaceCommand{UserID: "buy110", Side: domain.Buy, Type: domain.Limit, Price: mustDec("110"), Quantity: decimal.NewFromInt(1)})   //nolint

	if e3.armedStops != 0 {
		t.Fatalf("decision-b setup: stop-limits should have fired, armedStops=%d", e3.armedStops)
	}
	// Both X and Y rested (cascade bypass). openOrders=2 > maxOpenOrders=1.
	// X rested at 115 (bid). Y rested at 116 (bid, higher → better bid).
	// Best bid = Y@116, next = X@115.
	if e3.openOrders != 2 {
		t.Fatalf("decision-b setup: openOrders should be 2 (cascade overshoot), got %d", e3.openOrders)
	}

	// Now place a sell limit at 115 qty=3 (no cap on stop placement, but this is a Limit).
	// Wait — openOrders=2 > maxOpenOrders=1. Limit sell would cap-hit.
	// We need a sell that crosses the resting buys WITHOUT resting itself.
	// Market sell qty=3: crosses Y@116 (3 filled). Y PartiallyFilled? Y qty=5, fill=3 → Y remaining=2.
	// openOrders still 2 (both X and Y still alive). Market Filled. No remainder.
	// Still not the scenario.
	//
	// Taker buy qty=12 crossing a sell: impossible since no sells in book (all consumed).
	//
	// Place a sell at 114 qty=3 (limit, tries to rest):
	//   Sell@114 limit. Does it cross bids? Yes — best bid = Y@116 >= 114. Cross!
	//   fillQty=min(3,5)=3. Y remaining=2. Y PartiallyFilled, still resting (openOrders=2).
	//   Sell remaining=0. Sell Filled. No remainder. Not decision (b).
	//
	// Sell@114 qty=20:
	//   Cross Y@116: fillQty=min(20,5)=5. Y Filled. openOrders→1. Sell remaining=15.
	//   Cross X@115: 115>=114 → cross. fillQty=min(15,5)=5. X Filled. openOrders→0.
	//   Sell remaining=10. No more bids. Limit sell, remaining=10.
	//   Cap: 0+1=1 <= 1. Sell rests. NOT cap-hit.
	//
	// Sell@114 qty=7 (bigger than Y but smaller than X+Y=10):
	//   Cross Y@116: fill 5 (Y fully consumed, openOrders→1). Sell remaining=2.
	//   Cross X@115: 115>=114 → cross. fill 2 from X (X remaining=3). openOrders=1.
	//   Sell remaining=0. Sell Filled. No remainder.
	//
	// Sell@115 qty=7:
	//   Best bid = Y@116. 116>=115 → cross. fill 5 from Y (Y consumed, openOrders→1).
	//   Sell remaining=2. Next bid = X@115. 115>=115 → cross. fill 2 from X (X remaining=3).
	//   Sell remaining=0. Sell Filled.
	//
	// Sell@116 qty=7:
	//   Best bid = Y@116. 116>=116 → cross. fill 5 (Y consumed, openOrders→1). Remaining=2.
	//   Next bid = X@115. 115>=116? NO (X.Price=115 < sell.Price=116). Stop.
	//   Sell remaining=2. Cap: 1+1=2>1 → HIT!!! Decision (b) fires!
	//   Sell PartiallyFilled. NOT rested. Trades kept. No error.

	res, err := e3.Place(PlaceCommand{
		UserID:   "sell-taker",
		Side:     domain.Sell,
		Type:     domain.Limit,
		Price:    mustDec("116"),
		Quantity: mustDec("7"),
	})
	if err != nil {
		t.Fatalf("decision (b): unexpected error: %v", err)
	}
	if res.Order.Status != domain.StatusPartiallyFilled {
		t.Fatalf("taker: want PartiallyFilled, got %s", res.Order.Status)
	}
	if len(res.Trades) == 0 {
		t.Fatalf("decision (b): want ≥1 trade, got 0")
	}
	// Taker must NOT be in byID.
	if _, ok := e3.byID[res.Order.ID]; ok {
		t.Fatalf("taker should NOT be in byID after decision (b) cap-hit truncation")
	}
	checkInvariants(t, e3)
	// openOrders=1 (X still resting at 115; Y was consumed).
	if e3.openOrders != 1 {
		t.Fatalf("openOrders should be 1 (X still resting), got %d", e3.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Cap-hit by counter even though actual book room exists
// ---------------------------------------------------------------------------

func TestEngine_Cap_CounterEnforced_RegardlessOfBookRoom(t *testing.T) {
	// MaxOpenOrders=1. After filling one order we get openOrders=0.
	// Place carol (openOrders→1), then dave should cap-reject.
	e := newTestEngine(t, 1, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "1")
	placeLimit(t, e, "bob", domain.Buy, "100", "1") // fills alice, openOrders=0

	placeLimit(t, e, "carol", domain.Buy, "99", "1") // openOrders→1
	checkInvariants(t, e)

	_, err := e.Place(PlaceCommand{
		UserID:   "dave",
		Side:     domain.Buy,
		Type:     domain.Limit,
		Price:    mustDec("98"),
		Quantity: mustDec("1"),
	})
	if err != ErrTooManyOrders {
		t.Fatalf("want ErrTooManyOrders, got %v", err)
	}
	checkInvariants(t, e)
	if e.openOrders != 1 {
		t.Fatalf("openOrders must remain 1, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Cascade overshoot of cap is allowed (back-pressure semantics)
//
// MaxOpenOrders=1. Arm two buy stop-limits at the same trigger. Both have no
// opposing ask liquidity. When the trigger fires, the FIRST stop-limit rests
// via the caller (drainTriggeredStops) without a cap check — openOrders→1.
// The SECOND stop-limit also rests (cascade, no cap check) — openOrders→2.
// This is the documented back-pressure overshoot: openOrders > maxOpenOrders.
// ---------------------------------------------------------------------------

func TestEngine_Cap_CascadeOvershoot_Allowed(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := New(Deps{
		Clock:         clk,
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(200),
		MaxOpenOrders: 1,
		MaxArmedStops: 100,
	})

	// Drive initial trade at 100 to set lastTradePrice.
	e.Place(PlaceCommand{UserID: "s0", Side: domain.Sell, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)}) //nolint
	e.Place(PlaceCommand{UserID: "b0", Side: domain.Buy, Type: domain.Limit, Price: mustDec("100"), Quantity: decimal.NewFromInt(1)})   //nolint
	// openOrders=0, lastTradePrice=100.

	// Arm two buy stop-limits: both trigger at 110, no ask at 115 or 116.
	// When the trigger fires, both become Limit(Buy) and rest (no crossing).
	e.Place(PlaceCommand{UserID: "slA", Side: domain.Buy, Type: domain.StopLimit, TriggerPrice: mustDec("110"), Price: mustDec("115"), Quantity: decimal.NewFromInt(3)}) //nolint
	e.Place(PlaceCommand{UserID: "slB", Side: domain.Buy, Type: domain.StopLimit, TriggerPrice: mustDec("110"), Price: mustDec("116"), Quantity: decimal.NewFromInt(3)}) //nolint
	if e.armedStops != 2 {
		t.Fatalf("want armedStops=2, got %d", e.armedStops)
	}

	// Drive trade at 110: place sell(110) then buy(110).
	// sell(110) rests (openOrders=1). buy(110) crosses sell(110). Trade at 110.
	// openOrders→0. lastTradePrice=110. Both stop-limits fire.
	// slA fires first (lower seq), becomes Limit(Buy,115). No asks. Rests. openOrders→1.
	// slB fires next (cascade, no cap check), becomes Limit(Buy,116). No asks. Rests. openOrders→2.
	// Final: openOrders=2 > maxOpenOrders=1. Documented back-pressure overshoot.
	res, err := e.Place(PlaceCommand{UserID: "sell110", Side: domain.Sell, Type: domain.Limit, Price: mustDec("110"), Quantity: decimal.NewFromInt(1)})
	if err != nil {
		t.Fatalf("sell110: unexpected error: %v", err)
	}
	if res.Order.Status != domain.StatusResting {
		t.Fatalf("sell110: want Resting, got %s", res.Order.Status)
	}

	_, err = e.Place(PlaceCommand{UserID: "buy110", Side: domain.Buy, Type: domain.Limit, Price: mustDec("110"), Quantity: decimal.NewFromInt(1)})
	if err != nil {
		t.Fatalf("buy110: unexpected error: %v", err)
	}

	if e.armedStops != 0 {
		t.Fatalf("both stop-limits should have fired; armedStops=%d", e.armedStops)
	}

	// Document: cascade overshoot. openOrders > maxOpenOrders is accepted.
	// The fundamental invariant openOrders==len(byID) must still hold.
	if e.openOrders != len(e.byID) {
		t.Fatalf("cascade overshoot: openOrders=%d but len(byID)=%d", e.openOrders, len(e.byID))
	}
	if e.armedStops != e.stops.Len() {
		t.Fatalf("cascade overshoot: armedStops=%d but stops.Len()=%d", e.armedStops, e.stops.Len())
	}
	// Both stop-limits rested → openOrders should be 2.
	if e.openOrders != 2 {
		t.Fatalf("cascade overshoot: want openOrders=2, got %d (cascade back-pressure semantics)", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// STP-at-cap: MaxOpenOrders=1, resting by X, cross from X → Cancelled, no cap fired
// ---------------------------------------------------------------------------

func TestEngine_STP_AtCap_NoCap_Fired(t *testing.T) {
	e := newTestEngine(t, 1, 100)
	// Alice places a resting sell — uses the one slot.
	maker := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	if e.openOrders != 1 {
		t.Fatalf("setup: openOrders should be 1, got %d", e.openOrders)
	}

	// Alice places a buy at the same price — should STP-cancel, NOT cap-reject.
	res, err := e.Place(PlaceCommand{
		UserID:   "alice",
		Side:     domain.Buy,
		Type:     domain.Limit,
		Price:    mustDec("100"),
		Quantity: mustDec("5"),
	})
	if err != nil {
		t.Fatalf("STP-at-cap: unexpected error: %v", err)
	}
	if res.Order.Status != domain.StatusCancelled {
		t.Fatalf("STP-at-cap: want Cancelled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("STP: want 0 trades, got %d", len(res.Trades))
	}
	// The original maker must still be resting (untouched).
	if maker.Order.Status != domain.StatusResting {
		t.Fatalf("maker after STP: want Resting, got %s", maker.Order.Status)
	}
	checkInvariants(t, e)
	// openOrders unchanged at 1.
	if e.openOrders != 1 {
		t.Fatalf("openOrders must remain 1 after STP, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Stop cap-hit AFTER a stop is rejected — rejected one does NOT consume slot
// ---------------------------------------------------------------------------

func TestEngine_Cap_Stop_Rejected_DoesNotConsumeCap(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 1)

	// Place stop that will be rejected (trigger=100 equals lastTradePrice=100).
	res1, err := e.Place(PlaceCommand{
		UserID:       "alice",
		Side:         domain.Buy,
		Type:         domain.Stop,
		TriggerPrice: mustDec("100"),
		Quantity:     mustDec("5"),
	})
	if err != nil {
		t.Fatalf("rejected stop: unexpected error: %v", err)
	}
	if res1.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res1.Order.Status)
	}
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0, got %d", e.armedStops)
	}

	// Now place a legit stop — the one slot is still available.
	res2 := placeStop(t, e, "alice", domain.Buy, "110", "5")
	if res2.Order.Status != domain.StatusArmed {
		t.Fatalf("legit stop should arm, got %s", res2.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// Cap-by-counter: partially-filled maker still counts against cap
// ---------------------------------------------------------------------------

func TestEngine_Cap_PartiallyFilledMaker_StillCountsAgainstCap(t *testing.T) {
	// MaxOpenOrders=1. Place sell(10). Partial-fill with market buy(3) — maker is
	// PartiallyFilled but still resting. Now try to place another limit — must reject.
	e := newTestEngine(t, 1, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "10")
	placeMarket(t, e, "bob", domain.Buy, "3") // partial fill, maker still rests
	checkInvariants(t, e)
	if e.openOrders != 1 {
		t.Fatalf("setup: openOrders should be 1, got %d", e.openOrders)
	}

	_, err := e.Place(PlaceCommand{
		UserID:   "carol",
		Side:     domain.Buy,
		Type:     domain.Limit,
		Price:    mustDec("99"),
		Quantity: mustDec("5"),
	})
	if err != ErrTooManyOrders {
		t.Fatalf("want ErrTooManyOrders, got %v", err)
	}
	checkInvariants(t, e)
}
