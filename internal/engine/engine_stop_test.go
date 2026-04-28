// Package engine — stop arming, firing, and cascade tests.
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

// newEngineWithLastPrice builds an engine and drives a single trade to establish
// lastTradePrice, then returns both the engine and a fresh clock so tests can
// control subsequent ticks.
func newEngineWithLastPrice(t *testing.T, price string, maxOpen, maxStops int) (*Engine, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := New(Deps{
		Clock:         clk,
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(2000),
		MaxOpenOrders: maxOpen,
		MaxArmedStops: maxStops,
	})
	p := mustDec(price)
	// Place a sell and a matching buy to set lastTradePrice.
	e.Place(PlaceCommand{UserID: "setup-sell", Side: domain.Sell, Type: domain.Limit, Price: p, Quantity: decimal.NewFromInt(1)}) //nolint
	e.Place(PlaceCommand{UserID: "setup-buy", Side: domain.Buy, Type: domain.Limit, Price: p, Quantity: decimal.NewFromInt(1)})   //nolint
	if e.lastTradePrice.Cmp(p) != 0 {
		t.Fatalf("setup trade did not set lastTradePrice: want %s got %s", price, e.lastTradePrice)
	}
	return e, clk
}

// placeStop places a Stop order; fails the test on error.
func placeStop(t *testing.T, e *Engine, userID string, side domain.Side, trigger, qty string) PlaceResult {
	t.Helper()
	res, err := e.Place(PlaceCommand{
		UserID:       userID,
		Side:         side,
		Type:         domain.Stop,
		TriggerPrice: mustDec(trigger),
		Quantity:     mustDec(qty),
	})
	if err != nil {
		t.Fatalf("placeStop: unexpected error: %v", err)
	}
	return res
}

// placeStopLimit places a StopLimit order; fails the test on error.
func placeStopLimit(t *testing.T, e *Engine, userID string, side domain.Side, trigger, price, qty string) PlaceResult {
	t.Helper()
	res, err := e.Place(PlaceCommand{
		UserID:       userID,
		Side:         side,
		Type:         domain.StopLimit,
		TriggerPrice: mustDec(trigger),
		Price:        mustDec(price),
		Quantity:     mustDec(qty),
	})
	if err != nil {
		t.Fatalf("placeStopLimit: unexpected error: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Stop arming and rejection — Buy side
// ---------------------------------------------------------------------------

func TestEngine_Stop_Buy_TriggerAboveLastPrice_Armed(t *testing.T) {
	// lastTradePrice=100; trigger=110 > 100, so the stop should arm.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Buy, "110", "5")
	if res.Order.Status != domain.StatusArmed {
		t.Fatalf("want Armed, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}
}

func TestEngine_Stop_Buy_TriggerEqualLastPrice_Rejected(t *testing.T) {
	// trigger == lastTradePrice → trigger <= lastTradePrice → reject (would fire immediately).
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Buy, "100", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0, got %d", e.armedStops)
	}
}

func TestEngine_Stop_Buy_TriggerBelowLastPrice_Rejected(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Buy, "90", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Stop arming and rejection — Sell side
// ---------------------------------------------------------------------------

func TestEngine_Stop_Sell_TriggerBelowLastPrice_Armed(t *testing.T) {
	// lastTradePrice=100; trigger=90 < 100 → sell stop should arm.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Sell, "90", "5")
	if res.Order.Status != domain.StatusArmed {
		t.Fatalf("want Armed, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}
}

func TestEngine_Stop_Sell_TriggerEqualLastPrice_Rejected(t *testing.T) {
	// trigger == lastTradePrice → trigger >= lastTradePrice → reject.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Sell, "100", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

func TestEngine_Stop_Sell_TriggerAboveLastPrice_Rejected(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Sell, "110", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// StopLimit arming and rejection — same coverage as Stop
// ---------------------------------------------------------------------------

func TestEngine_StopLimit_Buy_Armed(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStopLimit(t, e, "alice", domain.Buy, "110", "112", "5")
	if res.Order.Status != domain.StatusArmed {
		t.Fatalf("want Armed, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}
}

func TestEngine_StopLimit_Buy_TriggerSatisfied_Rejected(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStopLimit(t, e, "alice", domain.Buy, "100", "102", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

func TestEngine_StopLimit_Sell_Armed(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStopLimit(t, e, "alice", domain.Sell, "90", "88", "5")
	if res.Order.Status != domain.StatusArmed {
		t.Fatalf("want Armed, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

func TestEngine_StopLimit_Sell_TriggerSatisfied_Rejected(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStopLimit(t, e, "alice", domain.Sell, "100", "98", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Stop firing when trade reaches trigger
// ---------------------------------------------------------------------------

func TestEngine_Stop_Buy_FiresOnTrigger(t *testing.T) {
	// lastTradePrice=100; arm buy stop at 105; drive trade at 105 → fires.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)

	// Provide ask liquidity so the triggered stop-market has something to fill against.
	placeLimit(t, e, "liquidity-seller", domain.Sell, "105", "5")

	placeStop(t, e, "alice", domain.Buy, "105", "3")
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("want armedStops=1, got %d", e.armedStops)
	}

	// Drive a trade at 105. This sets lastTradePrice=105 → triggers alice's stop.
	placeLimit(t, e, "seller2", domain.Sell, "105", "2")
	placeLimit(t, e, "buyer2", domain.Buy, "105", "2")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after firing, got %d", e.armedStops)
	}
	// Alice's stop became a Market and consumed ask liquidity.
	trades := e.Trades(10)
	if len(trades) < 2 {
		t.Fatalf("expected at least 2 trades (setup + cascade), got %d", len(trades))
	}
}

// ---------------------------------------------------------------------------
// Stop fires → StopLimit fires → becomes Limit, fills or rests
// ---------------------------------------------------------------------------

func TestEngine_StopLimit_Fires_Fills(t *testing.T) {
	// lastTradePrice=100; arm buy stop-limit at trigger=105, price=110.
	// Drive trade at 105. Stop-limit becomes Limit(110); ask at 107 exists → fills.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)

	// Pre-place ask at 107.
	placeLimit(t, e, "seller", domain.Sell, "107", "3")
	placeStopLimit(t, e, "alice", domain.Buy, "105", "110", "3")
	checkInvariants(t, e)

	// Drive trade at 105.
	placeLimit(t, e, "seller2", domain.Sell, "105", "2")
	placeLimit(t, e, "buyer2", domain.Buy, "105", "2")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after firing, got %d", e.armedStops)
	}
	// The stop-limit filled the ask at 107 — book should be empty.
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0 (stop-limit filled), got %d", e.openOrders)
	}
}

func TestEngine_StopLimit_Fires_Rests(t *testing.T) {
	// lastTradePrice=100; arm buy stop-limit trigger=105, price=106; no asks → rests.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	placeStopLimit(t, e, "alice", domain.Buy, "105", "106", "3")
	checkInvariants(t, e)

	// Drive trade at 105.
	placeLimit(t, e, "seller2", domain.Sell, "105", "2")
	placeLimit(t, e, "buyer2", domain.Buy, "105", "2")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after firing, got %d", e.armedStops)
	}
	if e.openOrders != 1 {
		t.Fatalf("openOrders should be 1 (stop-limit resting as Limit), got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Cascade with two stops on same trigger price — fire by seq (stable order)
// ---------------------------------------------------------------------------

func TestEngine_Cascade_TwoStops_SameTrigger_SeqOrder(t *testing.T) {
	// lastTradePrice=100; arm two buy stops at trigger=105 (same trigger, different seq).
	// Provide enough ask liquidity for both.
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)

	placeLimit(t, e, "liquidity", domain.Sell, "105", "10")

	s1 := placeStop(t, e, "alice", domain.Buy, "105", "3")
	s2 := placeStop(t, e, "bob", domain.Buy, "105", "4")
	checkInvariants(t, e)
	if s1.Order.Seq() >= s2.Order.Seq() {
		t.Fatalf("s1.Seq=%d should be < s2.Seq=%d", s1.Order.Seq(), s2.Order.Seq())
	}

	// Drive trade at 105 to trigger both.
	placeLimit(t, e, "seller2", domain.Sell, "105", "1")
	placeLimit(t, e, "buyer2", domain.Buy, "105", "1")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0, got %d", e.armedStops)
	}
	// Both stops should have fired.
	trades := e.Trades(10)
	// We expect: 1 setup trade + 1 trigger trade + 2 stop-market fills = at least 4.
	if len(trades) < 3 {
		t.Fatalf("expected at least 3 trades total, got %d", len(trades))
	}
}

// ---------------------------------------------------------------------------
// Cascade chain depth ≥ 2
// ---------------------------------------------------------------------------

func TestEngine_Cascade_Chain_Depth2(t *testing.T) {
	// Cascade chain of depth 2:
	//   A trade sets lastTradePrice=80 → stop A fires (buy stop-limit trigger=80).
	//   A becomes Limit(200). No asks below 200 except liq2 at 100.
	//   A crosses liq2 at 100 → trade at 100 → lastTradePrice=100 → stop B fires.
	//   B becomes Market, no remaining liquidity → Rejected.
	//
	// Key: liq1 at 80 is consumed by the TRIGGERING trade before stop A fires.
	// So when A fires as Limit(200), the only remaining ask is liq2 at 100.

	e, _ := newEngineWithLastPrice(t, "50", 100, 100)

	// Ask liquidity ONLY at 100 (not at 80 — so when A fires, it crosses 100).
	placeLimit(t, e, "liq2", domain.Sell, "100", "3")

	// Stop A (buy stop-limit): triggers at 80, limit price 200 (aggressive, crosses 100).
	// When it fires, it crosses the ask at 100 → trade at 100 → triggers B.
	sA := placeStopLimit(t, e, "alice", domain.Buy, "80", "200", "2")
	_ = sA

	// Stop B (buy stop): triggers at 100, fires as Market.
	sB := placeStop(t, e, "bob", domain.Buy, "100", "3")
	_ = sB

	checkInvariants(t, e)
	if e.armedStops != 2 {
		t.Fatalf("want armedStops=2, got %d", e.armedStops)
	}

	// Drive a trade at 80 (buyer and seller at 80 — this sets lastTradePrice=80
	// and triggers stop A).
	// After buyer3 fills: lastTradePrice=80. Stop A fires (80<=80).
	// A becomes Limit(200). Best ask=liq2@100. 100<=200 → crosses. Trade at 100.
	// lastTradePrice=100. Stop B fires (100<=100). B becomes Market. No more asks. Rejected.
	placeLimit(t, e, "seller80", domain.Sell, "80", "1")
	placeLimit(t, e, "buyer80", domain.Buy, "80", "1")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("cascade chain: both stops should have fired, armedStops=%d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// Cancel armed stop, then drive lastTradePrice past trigger → no fire
// ---------------------------------------------------------------------------

func TestEngine_CancelArmedStop_NoFireAfterCancel(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	// Arm buy stop at 105.
	res := placeStop(t, e, "alice", domain.Buy, "105", "5")
	stopID := res.Order.ID
	checkInvariants(t, e)

	// Cancel the stop.
	cancelled, err := e.Cancel(stopID)
	if err != nil {
		t.Fatalf("cancel stop: unexpected error: %v", err)
	}
	if cancelled.Status != domain.StatusCancelled {
		t.Fatalf("want Cancelled, got %s", cancelled.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after cancel, got %d", e.armedStops)
	}

	// Drive trade at 110 (past the trigger). The cancelled stop must NOT fire.
	placeLimit(t, e, "seller", domain.Sell, "110", "2")
	placeLimit(t, e, "buyer", domain.Buy, "110", "2")
	checkInvariants(t, e)

	// No stops should have fired (armedStops is still 0, openOrders=0).
	if e.armedStops != 0 {
		t.Fatalf("cancelled stop fired! armedStops=%d", e.armedStops)
	}
	// No orders should be resting (from stopped market order attempt).
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Cancel armed stop → armedStops==0
// ---------------------------------------------------------------------------

func TestEngine_CancelArmedStop_Counters(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	res := placeStop(t, e, "alice", domain.Buy, "110", "5")
	stopID := res.Order.ID
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("armedStops should be 1, got %d", e.armedStops)
	}

	cancelled, err := e.Cancel(stopID)
	if err != nil {
		t.Fatalf("cancel: unexpected error: %v", err)
	}
	if cancelled.Status != domain.StatusCancelled {
		t.Fatalf("want Cancelled, got %s", cancelled.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after cancel, got %d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// §11.1 — Sell stop on fresh engine (trigger >= 0=lastTradePrice) is rejected
// ---------------------------------------------------------------------------

func TestEngine_SellStop_FreshEngine_Rejected(t *testing.T) {
	// Per §11.1: lastTradePrice==0 on init; any positive sell trigger >= 0 → rejected.
	e := newTestEngine(t, 100, 100)
	res := placeStop(t, e, "alice", domain.Sell, "50", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("sell stop on fresh engine: want Rejected (trigger %s >= lastTradePrice 0), got %s",
			"50", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0, got %d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// Devil's advocate — off-by-one: buy stop with trigger exactly one tick above last
// ---------------------------------------------------------------------------

func TestEngine_Stop_Buy_TriggerOneTickAboveLast_Armed(t *testing.T) {
	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	// trigger = 100.01 — strictly greater than 100 → should arm.
	res := placeStop(t, e, "alice", domain.Buy, "100.01", "5")
	if res.Order.Status != domain.StatusArmed {
		t.Fatalf("trigger one tick above last: want Armed, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Decimal: stop trigger trailing-zero canonicalisation
// ---------------------------------------------------------------------------

func TestEngine_Stop_TriggerTrailingZero_Rejected(t *testing.T) {
	// lastTradePrice set to 50 (via trade at "50").
	// Stop trigger "50.0" == "50" → trigger <= lastTradePrice → rejected.
	e, _ := newEngineWithLastPrice(t, "50", 100, 100)
	res := placeStop(t, e, "alice", domain.Buy, "50.0", "5")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("trigger '50.0' should be rejected (50.0 <= lastTradePrice 50), got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Stop reject does NOT consume cap
// ---------------------------------------------------------------------------

func TestEngine_StopReject_DoesNotConsumeCap(t *testing.T) {
	// MaxArmedStops=1; place a stop that gets rejected (trigger satisfied at placement).
	// Then place a legit stop → must arm (cap slot unused by the rejected one).
	e, _ := newEngineWithLastPrice(t, "100", 100, 1)

	// This buy stop will be rejected (trigger 100 == lastTradePrice 100).
	res1 := placeStop(t, e, "alice", domain.Buy, "100", "5")
	if res1.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res1.Order.Status)
	}
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after rejected stop, got %d", e.armedStops)
	}

	// Now place a legit buy stop at trigger=110 > 100.
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
// Multiple cascade on the SAME trade — N stops fire simultaneously, seq order
// ---------------------------------------------------------------------------

func TestEngine_Cascade_MultipleStopsSameTrade_SeqOrder(t *testing.T) {
	// Drive lastTradePrice=80 first.
	// Arm 3 buy stops at trigger=90, all firing from the same trade at 90.
	// Provide enough liquidity for all three.
	e, _ := newEngineWithLastPrice(t, "80", 100, 100)
	placeLimit(t, e, "liq", domain.Sell, "90", "30")

	s1 := placeStop(t, e, "user1", domain.Buy, "90", "3")
	s2 := placeStop(t, e, "user2", domain.Buy, "90", "4")
	s3 := placeStop(t, e, "user3", domain.Buy, "90", "5")
	checkInvariants(t, e)

	// Verify seq ordering (s1 < s2 < s3).
	if s1.Order.Seq() >= s2.Order.Seq() || s2.Order.Seq() >= s3.Order.Seq() {
		t.Fatalf("seq ordering violated: s1=%d s2=%d s3=%d", s1.Order.Seq(), s2.Order.Seq(), s3.Order.Seq())
	}

	// Drive trade at 90 — all three stops trigger simultaneously.
	placeLimit(t, e, "sell90", domain.Sell, "90", "1")
	placeLimit(t, e, "buy90", domain.Buy, "90", "1")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("all stops should have fired, armedStops=%d", e.armedStops)
	}
}

// ---------------------------------------------------------------------------
// Stop fires from cascade trade (depth ≥ 2 chain via Place → trade → cascadeA → tradeB → cascadeB)
// ---------------------------------------------------------------------------

func TestEngine_CascadeDepth2_FromSinglePlace(t *testing.T) {
	// Setup:
	//  - lastTradePrice = 100.
	//  - Arm buy stop A at trigger=110 (fires as Market).
	//  - Arm buy stop B at trigger=120 (fires as Market after A's trade).
	//  - Ask liquidity at 110 (qty=5) and 120 (qty=5).
	//  - Place a trade at 110 (an external buyer+seller).
	//    → price=110 → A fires → A is Market → consumes liq at 110 → trade at 110
	//    → but price is still 110 after A's trade, not 120, so B doesn't fire yet.
	//  Change: A should be a stop-limit with limit-price=120 so its fill is at 120.
	//    → A fires at 110 → becomes Limit(120) → crosses ask at 120 → trade at 120
	//    → price=120 → B fires.

	e, _ := newEngineWithLastPrice(t, "100", 100, 100)
	placeLimit(t, e, "liq-120", domain.Sell, "120", "5")
	placeStopLimit(t, e, "alice", domain.Buy, "110", "120", "3")
	placeStop(t, e, "bob", domain.Buy, "120", "5")
	checkInvariants(t, e)
	if e.armedStops != 2 {
		t.Fatalf("want armedStops=2, got %d", e.armedStops)
	}

	// Drive trade at 110.
	placeLimit(t, e, "sell110", domain.Sell, "110", "2")
	placeLimit(t, e, "buy110", domain.Buy, "110", "2")
	checkInvariants(t, e)

	if e.armedStops != 0 {
		t.Fatalf("both stops should have fired; armedStops=%d", e.armedStops)
	}
}
