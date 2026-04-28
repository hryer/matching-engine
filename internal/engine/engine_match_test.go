// Package engine — match tests (Limit, Market, STP, FIFO, Snapshot, Trades).
// Uses package engine (not engine_test) to access unexported state.
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
// helpers
// ---------------------------------------------------------------------------

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newTestEngine(t *testing.T, maxOpen, maxStops int) *Engine {
	t.Helper()
	return New(Deps{
		Clock:         clock.NewFake(t0),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(2000),
		MaxOpenOrders: maxOpen,
		MaxArmedStops: maxStops,
	})
}

func mustDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("mustDec: " + err.Error())
	}
	return d
}

func checkInvariants(t *testing.T, e *Engine) {
	t.Helper()
	if e.openOrders != len(e.byID) {
		t.Fatalf("invariant violated: openOrders=%d but len(byID)=%d", e.openOrders, len(e.byID))
	}
	if e.armedStops != e.stops.Len() {
		t.Fatalf("invariant violated: armedStops=%d but stops.Len()=%d", e.armedStops, e.stops.Len())
	}
	// No terminal orders should live in byID.
	for id, o := range e.byID {
		if o.Status == domain.StatusFilled ||
			o.Status == domain.StatusCancelled ||
			o.Status == domain.StatusRejected {
			t.Fatalf("byID contains terminal order id=%s status=%s", id, o.Status)
		}
	}
}

func placeLimit(t *testing.T, e *Engine, userID string, side domain.Side, price, qty string) PlaceResult {
	t.Helper()
	res, err := e.Place(PlaceCommand{
		UserID:   userID,
		Side:     side,
		Type:     domain.Limit,
		Price:    mustDec(price),
		Quantity: mustDec(qty),
	})
	if err != nil {
		t.Fatalf("placeLimit: unexpected error: %v", err)
	}
	return res
}

func placeMarket(t *testing.T, e *Engine, userID string, side domain.Side, qty string) PlaceResult {
	t.Helper()
	res, err := e.Place(PlaceCommand{
		UserID:   userID,
		Side:     side,
		Type:     domain.Market,
		Quantity: mustDec(qty),
	})
	if err != nil {
		t.Fatalf("placeMarket: unexpected error: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Empty-book and trivial cases
// ---------------------------------------------------------------------------

func TestEngine_EmptyBook_Market_Rejected(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res := placeMarket(t, e, "alice", domain.Buy, "10")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades, got %d", len(res.Trades))
	}
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
}

func TestEngine_EmptyBook_Limit_Rests(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	if res.Order.Status != domain.StatusResting {
		t.Fatalf("want Resting, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades, got %d", len(res.Trades))
	}
	checkInvariants(t, e)
	if e.openOrders != 1 {
		t.Fatalf("openOrders should be 1, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Limit matching — one level
// ---------------------------------------------------------------------------

func TestEngine_Limit_CrossesOneLevel(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	maker := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	taker := placeLimit(t, e, "bob", domain.Buy, "100", "5")

	if taker.Order.Status != domain.StatusFilled {
		t.Fatalf("taker: want Filled, got %s", taker.Order.Status)
	}
	if maker.Order.Status != domain.StatusFilled {
		t.Fatalf("maker: want Filled, got %s", maker.Order.Status)
	}
	if len(taker.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(taker.Trades))
	}
	trade := taker.Trades[0]
	if trade.Price.Cmp(mustDec("100")) != 0 {
		t.Fatalf("trade price: want 100, got %s", trade.Price)
	}
	if trade.Quantity.Cmp(mustDec("5")) != 0 {
		t.Fatalf("trade qty: want 5, got %s", trade.Quantity)
	}
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0 after full fill, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Limit matching — multiple levels (N trades, price order)
// ---------------------------------------------------------------------------

func TestEngine_Limit_CrossesMultipleLevels(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// Three sell levels at ascending prices.
	placeLimit(t, e, "alice", domain.Sell, "99", "3")
	placeLimit(t, e, "alice", domain.Sell, "100", "3")
	placeLimit(t, e, "alice", domain.Sell, "101", "3")

	// Aggressive buy that crosses all three.
	res := placeLimit(t, e, "bob", domain.Buy, "101", "9")
	if res.Order.Status != domain.StatusFilled {
		t.Fatalf("taker: want Filled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 3 {
		t.Fatalf("want 3 trades, got %d", len(res.Trades))
	}
	// Trades must be in ascending price order (best ask first).
	prices := []string{"99", "100", "101"}
	for i, trade := range res.Trades {
		want := mustDec(prices[i])
		if trade.Price.Cmp(want) != 0 {
			t.Fatalf("trade[%d]: want price %s, got %s", i, prices[i], trade.Price)
		}
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Limit partial cross — remainder rests
// ---------------------------------------------------------------------------

func TestEngine_Limit_PartialCross_RemainderRests(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "3")
	res := placeLimit(t, e, "bob", domain.Buy, "100", "10")

	if res.Order.Status != domain.StatusPartiallyFilled {
		t.Fatalf("taker: want PartiallyFilled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(res.Trades))
	}
	remain := mustDec("7")
	if res.Order.RemainingQuantity.Cmp(remain) != 0 {
		t.Fatalf("remaining: want 7, got %s", res.Order.RemainingQuantity)
	}
	checkInvariants(t, e)
	if e.openOrders != 1 {
		t.Fatalf("openOrders should be 1 (taker resting), got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Limit price gate — strictly less-than check
// ---------------------------------------------------------------------------

func TestEngine_Limit_AtExactBestPrice_Fills(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "5")
	res := placeLimit(t, e, "bob", domain.Buy, "100", "5")
	if res.Order.Status != domain.StatusFilled {
		t.Fatalf("buy at exactly ask price: want Filled, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
}

func TestEngine_Limit_OneTickWorse_Rests(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "5")
	res := placeLimit(t, e, "bob", domain.Buy, "99", "5")
	if res.Order.Status != domain.StatusResting {
		t.Fatalf("buy one tick below ask: want Resting, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades, got %d", len(res.Trades))
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Market matching — partial, full, no liquidity
// ---------------------------------------------------------------------------

func TestEngine_Market_EatsBookPartially(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "3")
	res := placeMarket(t, e, "bob", domain.Buy, "10")
	if res.Order.Status != domain.StatusPartiallyFilled {
		t.Fatalf("want PartiallyFilled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(res.Trades))
	}
	// Market remainder is dropped — not in book.
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
}

func TestEngine_Market_EatsBookFully(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "5")
	res := placeMarket(t, e, "bob", domain.Buy, "5")
	if res.Order.Status != domain.StatusFilled {
		t.Fatalf("want Filled, got %s", res.Order.Status)
	}
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
}

func TestEngine_Market_NoLiquidity_Rejected(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res := placeMarket(t, e, "bob", domain.Buy, "10")
	if res.Order.Status != domain.StatusRejected {
		t.Fatalf("want Rejected, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades, got %d", len(res.Trades))
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// FIFO — two orders same price
// ---------------------------------------------------------------------------

func TestEngine_FIFO_TwoMakers_SamePrice_PlacementOrder(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// Two sell makers at same price; taker should fill first-placed first.
	r1 := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	r2 := placeLimit(t, e, "bob", domain.Sell, "100", "5")

	res := placeLimit(t, e, "carol", domain.Buy, "100", "10")
	if len(res.Trades) != 2 {
		t.Fatalf("want 2 trades, got %d", len(res.Trades))
	}
	if res.Trades[0].MakerOrderID != r1.Order.ID {
		t.Fatalf("first trade should be against alice (r1=%s), got MakerOrderID=%s", r1.Order.ID, res.Trades[0].MakerOrderID)
	}
	if res.Trades[1].MakerOrderID != r2.Order.ID {
		t.Fatalf("second trade should be against bob (r2=%s), got MakerOrderID=%s", r2.Order.ID, res.Trades[1].MakerOrderID)
	}
	checkInvariants(t, e)
}

func TestEngine_FIFO_SamePrice_SameCreatedAt_SeqTiebreak(t *testing.T) {
	// Clock pinned — both makers have identical CreatedAt; seq must be tiebreaker.
	e := newTestEngine(t, 100, 100)
	// Clock does NOT advance between placements.
	r1 := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	r2 := placeLimit(t, e, "bob", domain.Sell, "100", "5")

	res := placeLimit(t, e, "carol", domain.Buy, "100", "10")
	if len(res.Trades) != 2 {
		t.Fatalf("want 2 trades, got %d", len(res.Trades))
	}
	// Placement order must be preserved even with equal timestamps.
	if res.Trades[0].MakerOrderID != r1.Order.ID {
		t.Fatalf("seq tiebreak: first trade must be alice (r1=%s), got %s", r1.Order.ID, res.Trades[0].MakerOrderID)
	}
	if res.Trades[1].MakerOrderID != r2.Order.ID {
		t.Fatalf("seq tiebreak: second trade must be bob (r2=%s), got %s", r2.Order.ID, res.Trades[1].MakerOrderID)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Self-match (STP cancel-newest)
// ---------------------------------------------------------------------------

func TestEngine_STP_FirstMaker_Cancelled_NoTrades(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	maker := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	origRemaining := maker.Order.RemainingQuantity

	res := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	if res.Order.Status != domain.StatusCancelled {
		t.Fatalf("taker: want Cancelled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades, got %d", len(res.Trades))
	}
	// Maker must be untouched.
	if maker.Order.Status != domain.StatusResting {
		t.Fatalf("maker: want Resting, got %s", maker.Order.Status)
	}
	if maker.Order.RemainingQuantity.Cmp(origRemaining) != 0 {
		t.Fatalf("maker remaining qty changed: want %s, got %s", origRemaining, maker.Order.RemainingQuantity)
	}
	checkInvariants(t, e)
	// The maker should still be in byID.
	if _, ok := e.byID[maker.Order.ID]; !ok {
		t.Fatalf("maker should still be in byID after STP cancel of taker")
	}
}

func TestEngine_STP_Middle_OneTradeKept_OwnMakerUntouched(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// External maker first (will be filled).
	external := placeLimit(t, e, "bob", domain.Sell, "99", "2")
	// Own maker second (STP should fire here).
	own := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	origRemaining := own.Order.RemainingQuantity

	// Alice buys aggressively enough to hit both levels.
	res := placeLimit(t, e, "alice", domain.Buy, "100", "10")
	if res.Order.Status != domain.StatusCancelled {
		t.Fatalf("taker: want Cancelled, got %s", res.Order.Status)
	}
	// One trade against external maker must be kept.
	if len(res.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(res.Trades))
	}
	if res.Trades[0].MakerOrderID != external.Order.ID {
		t.Fatalf("trade should be against external maker, got MakerOrderID=%s", res.Trades[0].MakerOrderID)
	}
	// External maker fully filled.
	if external.Order.Status != domain.StatusFilled {
		t.Fatalf("external maker: want Filled, got %s", external.Order.Status)
	}
	// Own maker must be untouched.
	if own.Order.Status != domain.StatusResting {
		t.Fatalf("own maker: want Resting, got %s", own.Order.Status)
	}
	if own.Order.RemainingQuantity.Cmp(origRemaining) != 0 {
		t.Fatalf("own maker qty changed unexpectedly: %s", own.Order.RemainingQuantity)
	}
	checkInvariants(t, e)
}

func TestEngine_STP_DeeperNonSelfMaker_NotConsumed(t *testing.T) {
	// self maker at 100, non-self maker at 101 (deeper). STP fires at 100, stops.
	e := newTestEngine(t, 100, 100)
	self := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	deeper := placeLimit(t, e, "carol", domain.Sell, "101", "5")
	_ = deeper

	res := placeLimit(t, e, "alice", domain.Buy, "101", "10")
	if res.Order.Status != domain.StatusCancelled {
		t.Fatalf("taker: want Cancelled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("want 0 trades (STP fired on first maker), got %d", len(res.Trades))
	}
	// Self maker still resting.
	if self.Order.Status != domain.StatusResting {
		t.Fatalf("self maker: want Resting, got %s", self.Order.Status)
	}
	// Deeper non-self maker must be untouched.
	if deeper.Order.Status != domain.StatusResting {
		t.Fatalf("deeper non-self maker: want Resting, got %s", deeper.Order.Status)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Cancel — resting, armed, non-existent, already-filled, already-cancelled
// ---------------------------------------------------------------------------

func TestEngine_Cancel_Resting(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	id := res.Order.ID
	checkInvariants(t, e)

	cancelled, err := e.Cancel(id)
	if err != nil {
		t.Fatalf("cancel: unexpected error: %v", err)
	}
	if cancelled.Status != domain.StatusCancelled {
		t.Fatalf("want Cancelled, got %s", cancelled.Status)
	}
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
	bids, _ := e.Snapshot(10)
	if len(bids) != 0 {
		t.Fatalf("book should be empty after cancel, got %d bid levels", len(bids))
	}
}

func TestEngine_Cancel_NonExistent(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	_, err := e.Cancel("no-such-id")
	if err != ErrOrderNotFound {
		t.Fatalf("want ErrOrderNotFound, got %v", err)
	}
	checkInvariants(t, e)
}

func TestEngine_Cancel_AlreadyFilled_ReturnsNotFound(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	maker := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	placeLimit(t, e, "bob", domain.Buy, "100", "5") // fully fills alice
	if maker.Order.Status != domain.StatusFilled {
		t.Fatalf("maker should be Filled")
	}
	_, err := e.Cancel(maker.Order.ID)
	if err != ErrOrderNotFound {
		t.Fatalf("want ErrOrderNotFound for filled maker, got %v", err)
	}
	checkInvariants(t, e)
}

func TestEngine_Cancel_AlreadyCancelled_ReturnsNotFound(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	e.Cancel(res.Order.ID) //nolint — first cancel succeeds
	_, err := e.Cancel(res.Order.ID)
	if err != ErrOrderNotFound {
		t.Fatalf("want ErrOrderNotFound on second cancel, got %v", err)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func TestEngine_Snapshot_Empty(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	bids, asks := e.Snapshot(0)
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("Snapshot(0) must be empty, got bids=%d asks=%d", len(bids), len(asks))
	}
}

func TestEngine_Snapshot_FiveLevel_BidDescAskAsc(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// 5 bid levels at prices 96-100 (descending expected).
	for i := 96; i <= 100; i++ {
		placeLimit(t, e, "alice", domain.Buy, decimal.NewFromInt(int64(i)).String(), "1")
	}
	// 5 ask levels at prices 101-105 (ascending expected).
	for i := 101; i <= 105; i++ {
		placeLimit(t, e, "alice", domain.Sell, decimal.NewFromInt(int64(i)).String(), "1")
	}
	bids, asks := e.Snapshot(10)
	if len(bids) != 5 {
		t.Fatalf("want 5 bid levels, got %d", len(bids))
	}
	if len(asks) != 5 {
		t.Fatalf("want 5 ask levels, got %d", len(asks))
	}
	// Bids descending.
	for i := 1; i < len(bids); i++ {
		if !bids[i-1].Price.GreaterThan(bids[i].Price) {
			t.Fatalf("bids not descending at index %d: %s >= %s", i, bids[i-1].Price, bids[i].Price)
		}
	}
	// Asks ascending.
	for i := 1; i < len(asks); i++ {
		if !asks[i-1].Price.LessThan(asks[i].Price) {
			t.Fatalf("asks not ascending at index %d: %s >= %s", i, asks[i-1].Price, asks[i].Price)
		}
	}
}

func TestEngine_Snapshot_DoesNotIncludeArmedStops(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// Drive a trade so sell stops are not immediately rejected.
	placeLimit(t, e, "seller", domain.Sell, "100", "1")
	placeLimit(t, e, "buyer", domain.Buy, "100", "1") // lastTradePrice = 100

	// Place a buy stop (trigger must be > lastTradePrice=100).
	_, err := e.Place(PlaceCommand{
		UserID:       "alice",
		Side:         domain.Buy,
		Type:         domain.Stop,
		TriggerPrice: mustDec("110"),
		Quantity:     mustDec("5"),
	})
	if err != nil {
		t.Fatalf("stop placement: %v", err)
	}

	bids, asks := e.Snapshot(10)
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("Snapshot should not include armed stops, got bids=%d asks=%d", len(bids), len(asks))
	}
}

// ---------------------------------------------------------------------------
// Trades API
// ---------------------------------------------------------------------------

func TestEngine_Trades_Zero_Empty(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "1")
	placeLimit(t, e, "bob", domain.Buy, "100", "1")
	trades := e.Trades(0)
	if len(trades) != 0 {
		t.Fatalf("Trades(0) should return empty, got %d", len(trades))
	}
}

func TestEngine_Trades_Clamped_To_1000(t *testing.T) {
	// Place 1100 trades then request 1500 — expect 1000.
	pub := inmem.NewRing(2000)
	e2 := New(Deps{
		Clock:         clock.NewFake(t0),
		IDs:           ids.NewMonotonic(),
		Publisher:     pub,
		MaxOpenOrders: 200000,
		MaxArmedStops: 100,
	})
	for i := 0; i < 1100; i++ {
		// Each pair produces one trade.
		e2.Place(PlaceCommand{UserID: "s", Side: domain.Sell, Type: domain.Limit, Price: mustDec("100"), Quantity: mustDec("1")}) //nolint
		e2.Place(PlaceCommand{UserID: "b", Side: domain.Buy, Type: domain.Limit, Price: mustDec("100"), Quantity: mustDec("1")})   //nolint
	}
	trades := e2.Trades(1500)
	if len(trades) != 1000 {
		t.Fatalf("Trades(1500) should be clamped to 1000, got %d", len(trades))
	}
}

func TestEngine_Trades_NewestFirst(t *testing.T) {
	clk := clock.NewFake(t0)
	e2 := New(Deps{
		Clock:         clk,
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(100),
		MaxOpenOrders: 100,
		MaxArmedStops: 100,
	})

	// Three trades at different times.
	for i := 0; i < 3; i++ {
		clk.Advance(time.Second)
		e2.Place(PlaceCommand{UserID: "s", Side: domain.Sell, Type: domain.Limit, Price: mustDec("100"), Quantity: mustDec("1")}) //nolint
		e2.Place(PlaceCommand{UserID: "b", Side: domain.Buy, Type: domain.Limit, Price: mustDec("100"), Quantity: mustDec("1")})   //nolint
	}
	trades := e2.Trades(3)
	if len(trades) != 3 {
		t.Fatalf("want 3 trades, got %d", len(trades))
	}
	for i := 1; i < len(trades); i++ {
		if !trades[i-1].CreatedAt.After(trades[i].CreatedAt) {
			t.Fatalf("trades not newest-first at index %d: %v <= %v", i, trades[i-1].CreatedAt, trades[i].CreatedAt)
		}
	}
}

func TestEngine_Trades_NegativeLimit_Empty(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	placeLimit(t, e, "alice", domain.Sell, "100", "1")
	placeLimit(t, e, "bob", domain.Buy, "100", "1")
	trades := e.Trades(-5)
	if len(trades) != 0 {
		t.Fatalf("Trades(-5) should return empty, got %d", len(trades))
	}
}

// ---------------------------------------------------------------------------
// Decimal trailing-zero canonicalisation
// ---------------------------------------------------------------------------

func TestEngine_Decimal_TrailingZero_MatchesAsExpected(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// "100.0" and "100" are equal decimals; the book's priceKey must canonicalise them.
	placeLimit(t, e, "alice", domain.Sell, "100.0", "5")
	res := placeLimit(t, e, "bob", domain.Buy, "100", "5")
	if res.Order.Status != domain.StatusFilled {
		t.Fatalf("trailing-zero canonicalisation: want Filled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(res.Trades))
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Adversarial — Cancel during partial-fill remainder; idempotent re-place
// ---------------------------------------------------------------------------

func TestEngine_CancelDuringPartialFillRemainder(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	// A rests with qty 10.
	a := placeLimit(t, e, "alice", domain.Sell, "100", "10")
	// B partially fills A (qty 3) and rests with qty 7.
	b := placeLimit(t, e, "bob", domain.Buy, "100", "3")
	_ = b
	// A is still PartiallyFilled and resting with qty 7.
	if a.Order.Status != domain.StatusPartiallyFilled {
		t.Fatalf("maker A after partial fill: want PartiallyFilled, got %s", a.Order.Status)
	}
	checkInvariants(t, e)
	// Cancel B's rested portion — B placed a buy at 100 which fully filled A's partial,
	// but actually B was a taker that was fully filled (3 qty = A's offered 3 against 3).
	// Re-read: alice sells 10, bob buys 3 — bob fully fills with 3 taken from alice.
	// Alice now has RemainingQty=7, Status=PartiallyFilled, still in book.
	// Cancel alice.
	cancelled, err := e.Cancel(a.Order.ID)
	if err != nil {
		t.Fatalf("cancel partially-filled maker: unexpected error: %v", err)
	}
	if cancelled.Status != domain.StatusCancelled {
		t.Fatalf("want Cancelled, got %s", cancelled.Status)
	}
	checkInvariants(t, e)
	if e.openOrders != 0 {
		t.Fatalf("openOrders should be 0, got %d", e.openOrders)
	}
}

func TestEngine_IdempotentRePlaceAfterCancel(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	res1 := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	e.Cancel(res1.Order.ID) //nolint
	checkInvariants(t, e)
	res2 := placeLimit(t, e, "alice", domain.Buy, "100", "5")
	if res2.Order.Status != domain.StatusResting {
		t.Fatalf("re-place after cancel: want Resting, got %s", res2.Order.Status)
	}
	checkInvariants(t, e)
	if e.openOrders != 1 {
		t.Fatalf("openOrders should be 1, got %d", e.openOrders)
	}
}

// ---------------------------------------------------------------------------
// Adversarial — Cancel-after-fill regression
// ---------------------------------------------------------------------------

func TestEngine_CancelAfterFill_ReturnsNotFound(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	maker := placeLimit(t, e, "alice", domain.Sell, "100", "5")
	placeLimit(t, e, "bob", domain.Buy, "100", "5") // fully fills alice
	_, err := e.Cancel(maker.Order.ID)
	if err != ErrOrderNotFound {
		t.Fatalf("want ErrOrderNotFound, got %v", err)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Adversarial — Very small qty (precision test)
// ---------------------------------------------------------------------------

func TestEngine_SmallQty_Precision(t *testing.T) {
	e := newTestEngine(t, 100, 100)
	tiny := "0.000000000000000001" // 1e-18
	placeLimit(t, e, "alice", domain.Sell, "100", tiny)
	res := placeLimit(t, e, "bob", domain.Buy, "100", tiny)
	if res.Order.Status != domain.StatusFilled {
		t.Fatalf("tiny qty fill: want Filled, got %s", res.Order.Status)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(res.Trades))
	}
	if res.Trades[0].Quantity.Cmp(mustDec(tiny)) != 0 {
		t.Fatalf("trade qty mismatch: want %s got %s", tiny, res.Trades[0].Quantity)
	}
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Snapshot under load (200 mixed ops)
// ---------------------------------------------------------------------------

func TestEngine_SnapshotUnderLoad(t *testing.T) {
	e := newTestEngine(t, 100000, 100)
	var restingIDs []string

	for i := 0; i < 100; i++ {
		price := decimal.NewFromInt(int64(100 + i%10)).String()
		r := placeLimit(t, e, "alice", domain.Buy, price, "1")
		if r.Order.Status == domain.StatusResting {
			restingIDs = append(restingIDs, r.Order.ID)
		}
	}
	for i := 0; i < 100; i++ {
		price := decimal.NewFromInt(int64(110 + i%10)).String()
		r := placeLimit(t, e, "bob", domain.Sell, price, "1")
		if r.Order.Status == domain.StatusResting {
			restingIDs = append(restingIDs, r.Order.ID)
		}
	}

	checkInvariants(t, e)

	// Cancel half.
	cancelled := 0
	for i, id := range restingIDs {
		if i%2 == 0 {
			e.Cancel(id) //nolint
			cancelled++
		}
	}
	checkInvariants(t, e)

	bids, asks := e.Snapshot(1000)
	// Verify ordering.
	for i := 1; i < len(bids); i++ {
		if !bids[i-1].Price.GreaterThanOrEqual(bids[i].Price) {
			t.Fatalf("bids not descending at %d", i)
		}
	}
	for i := 1; i < len(asks); i++ {
		if !asks[i-1].Price.LessThanOrEqual(asks[i].Price) {
			t.Fatalf("asks not ascending at %d", i)
		}
	}
	// Invariants must hold.
	checkInvariants(t, e)
}

// ---------------------------------------------------------------------------
// Devil's advocate — StopLimit triggers, limit price past the market, still rests
// ---------------------------------------------------------------------------

func TestEngine_StopLimit_TriggersFires_LimitPricePastMarket_Rests(t *testing.T) {
	// Set up: drive lastTradePrice to 100 via a trade, then arm a sell stop-limit
	// with trigger=100 (would have been rejected at init — need lastTradePrice < 100
	// to arm a sell stop). Actually: sell stop arms when trigger < lastTradePrice.
	// So we need lastTradePrice > trigger to arm, and trigger > 0.
	// Drive a trade at 80 first, arm sell stop at trigger=90 (90 > 80, so rejected).
	// Instead: drive a trade at 120 (need 120 > 90 trigger).
	// Simpler: arm a buy stop with trigger 110 when lastTradePrice=100.
	// Then drive lastTradePrice to 110 — stop fires as Market.
	// For a StopLimit that rests: trigger=110, no ask liquidity above market.
	// Actually let's test a buy stop-limit that fires into a market with no opposing
	// liquidity so the triggered limit rests.

	e := newTestEngine(t, 100, 100)
	// Drive initial trade to set lastTradePrice=100.
	placeLimit(t, e, "seller", domain.Sell, "100", "1")
	placeLimit(t, e, "buyer", domain.Buy, "100", "1")

	// Arm buy stop-limit: trigger=105, limit_price=106.
	// Fires as Limit(price=106) when price rises to 105. No asks in book — rests.
	_, err := e.Place(PlaceCommand{
		UserID:       "alice",
		Side:         domain.Buy,
		Type:         domain.StopLimit,
		TriggerPrice: mustDec("105"),
		Price:        mustDec("106"),
		Quantity:     mustDec("3"),
	})
	if err != nil {
		t.Fatalf("stop-limit placement: %v", err)
	}
	checkInvariants(t, e)
	if e.armedStops != 1 {
		t.Fatalf("want armedStops=1, got %d", e.armedStops)
	}

	// Drive a trade at 105 to trigger the stop-limit.
	placeLimit(t, e, "seller2", domain.Sell, "105", "2")
	res := placeLimit(t, e, "buyer2", domain.Buy, "105", "2")
	_ = res
	checkInvariants(t, e)

	// Stop-limit fired, no asks at 106 — should rest as Limit.
	if e.armedStops != 0 {
		t.Fatalf("armedStops should be 0 after firing, got %d", e.armedStops)
	}
	// The triggered stop-limit order is now resting in the book.
	if e.openOrders != 1 {
		t.Fatalf("openOrders should be 1 (triggered stop-limit resting), got %d", e.openOrders)
	}
	bids, _ := e.Snapshot(10)
	if len(bids) != 1 {
		t.Fatalf("want 1 bid level (triggered stop-limit at 106), got %d", len(bids))
	}
	if bids[0].Price.Cmp(mustDec("106")) != 0 {
		t.Fatalf("resting bid price: want 106, got %s", bids[0].Price)
	}
}
