package app_test

import (
	"sync"
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/adapters/publisher/inmem"
	"matching-engine/internal/app"
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
	"matching-engine/internal/engine"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newService(maxOpen, maxStops int) *app.Service {
	eng := engine.New(engine.Deps{
		Clock:         clock.NewFake(t0),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(2000),
		MaxOpenOrders: maxOpen,
		MaxArmedStops: maxStops,
	})
	return app.NewService(eng)
}

func mustDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("mustDec: " + err.Error())
	}
	return d
}

// limitCmd returns a service PlaceCommand for a resting limit buy.
func limitCmd(userID, coid, price, qty string) app.PlaceCommand {
	return app.PlaceCommand{
		PlaceCommand: engine.PlaceCommand{
			UserID:   userID,
			Side:     domain.Buy,
			Type:     domain.Limit,
			Price:    mustDec(price),
			Quantity: mustDec(qty),
		},
		ClientOrderID: coid,
	}
}

// sellStopCmd returns a service PlaceCommand for a sell stop.
// On a fresh engine (lastTradePrice==0), any positive trigger satisfies the
// already-satisfied rule and yields a business rejection (Rejected, nil error).
func sellStopCmd(userID, coid, trigger string) app.PlaceCommand {
	return app.PlaceCommand{
		PlaceCommand: engine.PlaceCommand{
			UserID:       userID,
			Side:         domain.Sell,
			Type:         domain.Stop,
			TriggerPrice: mustDec(trigger),
			Quantity:     mustDec("1"),
		},
		ClientOrderID: coid,
	}
}

// ---------------------------------------------------------------------------
// TestService_DedupMissCallsEngine
//
// A first call with a key that has never been seen must reach the engine and
// return an order with ID "o-1".
// ---------------------------------------------------------------------------

func TestService_DedupMissCallsEngine(t *testing.T) {
	svc := newService(10, 10)

	res, err := svc.Place(limitCmd("alice", "coid-1", "100", "1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Order == nil {
		t.Fatal("expected non-nil Order")
	}
	if res.Order.ID != "o-1" {
		t.Fatalf("expected order ID o-1, got %s", res.Order.ID)
	}
	if res.Order.Status != domain.StatusResting {
		t.Fatalf("expected Resting, got %s", res.Order.Status)
	}
}

// ---------------------------------------------------------------------------
// TestService_DedupHitReturnsCached
//
// A second call with the same (UserID, ClientOrderID) must return the cached
// result byte-identically and must NOT call the engine again. We verify the
// "no second engine call" invariant by placing a control order with a
// different ClientOrderID afterward: if the dedup worked, the control order
// receives ID "o-2"; if dedup failed and the dup call hit the engine, the
// control would receive "o-3".
// ---------------------------------------------------------------------------

func TestService_DedupHitReturnsCached(t *testing.T) {
	svc := newService(10, 10)

	first, err := svc.Place(limitCmd("alice", "coid-1", "100", "1"))
	if err != nil {
		t.Fatalf("first place: %v", err)
	}

	// Duplicate call — same user + same ClientOrderID.
	second, err := svc.Place(limitCmd("alice", "coid-1", "101", "2")) // different body, same key
	if err != nil {
		t.Fatalf("second place: %v", err)
	}

	// Must be pointer-identical to the cached result.
	if second.Order != first.Order {
		t.Fatalf("cached result should return same *Order pointer")
	}

	// Control order: must get "o-2", not "o-3", proving the engine was called exactly once.
	ctrl, err := svc.Place(limitCmd("alice", "coid-control", "99", "1"))
	if err != nil {
		t.Fatalf("control place: %v", err)
	}
	if ctrl.Order.ID != "o-2" {
		t.Fatalf("control order ID: want o-2 (engine called once for coid-1), got %s", ctrl.Order.ID)
	}
}

// ---------------------------------------------------------------------------
// TestService_BusinessRejectIsCached
//
// A business-rejected result (Status=Rejected, nil error) must be cached.
// Subsequent calls with the same key must return the same rejection without
// re-invoking the engine.
//
// On a fresh engine, lastTradePrice==0. A sell stop with any positive trigger
// satisfies the already-satisfied rule (trigger >= lastTradePrice==0) and is
// immediately rejected with (PlaceResult{Order: rejected}, nil). This is the
// simplest path to a business rejection.
// ---------------------------------------------------------------------------

func TestService_BusinessRejectIsCached(t *testing.T) {
	svc := newService(10, 10)

	first, err := svc.Place(sellStopCmd("alice", "stop-1", "100"))
	if err != nil {
		t.Fatalf("first place: unexpected error: %v", err)
	}
	if first.Order.Status != domain.StatusRejected {
		t.Fatalf("expected Rejected status, got %s", first.Order.Status)
	}

	// Retry — must return the cached rejection, not hit the engine.
	second, err := svc.Place(sellStopCmd("alice", "stop-1", "100"))
	if err != nil {
		t.Fatalf("second place: unexpected error: %v", err)
	}
	if second.Order != first.Order {
		t.Fatalf("cached rejection should return same *Order pointer")
	}

	// Control order: must get "o-2" (engine called once for stop-1).
	ctrl, err := svc.Place(limitCmd("alice", "coid-control", "99", "1"))
	if err != nil {
		t.Fatalf("control place: %v", err)
	}
	if ctrl.Order.ID != "o-2" {
		t.Fatalf("control ID: want o-2, got %s (engine should have been called once for the rejected stop)", ctrl.Order.ID)
	}
}

// ---------------------------------------------------------------------------
// TestService_EngineErrorNotCached
//
// Engine sentinel errors (ErrTooManyOrders) must NOT be cached. A retry with
// the same ClientOrderID must reach the engine again.
//
// Setup:
//   MaxOpenOrders=1.
//   Place limit A (rests, "o-1").
//   Place limit B — cap hit, ErrTooManyOrders. Engine internally allocates
//   "o-2" (NextOrderID is called before the cap check) but returns an empty
//   PlaceResult with the error. Not cached.
//   Cancel A to free the slot.
//   Retry B with the same coid-B — since not cached, engine is called again.
//   Engine allocates "o-3" and B now rests (cap is free).
//   The fact that B's retry order is "o-3" (not "o-2") proves the engine was
//   invoked a second time — no additional control order needed.
// ---------------------------------------------------------------------------

func TestService_EngineErrorNotCached(t *testing.T) {
	svc := newService(1, 10)

	// Place limit A — rests at "o-1", openOrders=1.
	resA, err := svc.Place(limitCmd("alice", "coid-A", "100", "1"))
	if err != nil {
		t.Fatalf("place A: %v", err)
	}
	if resA.Order.ID != "o-1" {
		t.Fatalf("A: want o-1, got %s", resA.Order.ID)
	}

	// Place limit B — cap hit; engine consumes "o-2" internally but returns error.
	// The error must NOT be cached.
	_, err = svc.Place(limitCmd("alice", "coid-B", "99", "1"))
	if err != engine.ErrTooManyOrders {
		t.Fatalf("B first attempt: want ErrTooManyOrders, got %v", err)
	}

	// Cancel A to free the cap slot.
	if _, err = svc.Cancel(resA.Order.ID); err != nil {
		t.Fatalf("cancel A: %v", err)
	}

	// Retry B — must NOT be cached, so engine is called again.
	// This time the cap is free. Engine allocates "o-3" (not "o-2", because
	// the first cap-hit call already consumed that ID). B rests at "o-3".
	// If the error had been cached, the dedup would return the error without
	// calling the engine, and we would never reach this assertion.
	resB, err := svc.Place(limitCmd("alice", "coid-B", "99", "1"))
	if err != nil {
		t.Fatalf("B retry: %v", err)
	}
	// "o-3" proves the engine was invoked on the retry (o-2 was consumed on the
	// first failed attempt; a cached path would have returned an error here).
	if resB.Order.ID != "o-3" {
		t.Fatalf("B retry: want o-3 (engine called again on retry), got %s", resB.Order.ID)
	}
}

// ---------------------------------------------------------------------------
// TestService_DistinctClientOrderIDIsIndependent
//
// Two commands from the same user with different ClientOrderIDs must each
// reach the engine independently and receive distinct results.
// ---------------------------------------------------------------------------

func TestService_DistinctClientOrderIDIsIndependent(t *testing.T) {
	svc := newService(10, 10)

	res1, err := svc.Place(limitCmd("alice", "coid-1", "100", "1"))
	if err != nil {
		t.Fatalf("place 1: %v", err)
	}
	res2, err := svc.Place(limitCmd("alice", "coid-2", "99", "1"))
	if err != nil {
		t.Fatalf("place 2: %v", err)
	}

	if res1.Order.ID == res2.Order.ID {
		t.Fatalf("distinct ClientOrderIDs should produce distinct orders, both got %s", res1.Order.ID)
	}
	if res1.Order == res2.Order {
		t.Fatal("distinct ClientOrderIDs should not share Order pointers")
	}
}

// ---------------------------------------------------------------------------
// TestService_DistinctUserIDIsIndependent
//
// Two commands with the same ClientOrderID but different UserIDs must each
// reach the engine independently. The dedup key includes UserID.
// ---------------------------------------------------------------------------

func TestService_DistinctUserIDIsIndependent(t *testing.T) {
	svc := newService(10, 10)

	resAlice, err := svc.Place(limitCmd("alice", "shared-coid", "100", "1"))
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	resBob, err := svc.Place(limitCmd("bob", "shared-coid", "99", "1"))
	if err != nil {
		t.Fatalf("bob: %v", err)
	}

	if resAlice.Order.ID == resBob.Order.ID {
		t.Fatalf("distinct UserIDs with same ClientOrderID should produce distinct orders")
	}
	if resAlice.Order.UserID != "alice" {
		t.Fatalf("alice order has wrong UserID: %s", resAlice.Order.UserID)
	}
	if resBob.Order.UserID != "bob" {
		t.Fatalf("bob order has wrong UserID: %s", resBob.Order.UserID)
	}
}

// ---------------------------------------------------------------------------
// TestService_ConcurrentSameKeyEngineCalledOnce
//
// 100 goroutines submit the same (UserID, ClientOrderID) concurrently on a
// key that has never been seen. dedupMu serialises them: only one reaches the
// engine, the others receive the cached result. We verify "engine called once"
// by placing a control order after the goroutines complete and asserting its
// assigned ID is "o-2" — if the engine had been called N times the control
// would receive "o-(N+1)".
// ---------------------------------------------------------------------------

func TestService_ConcurrentSameKeyEngineCalledOnce(t *testing.T) {
	svc := newService(200, 200)

	const goroutines = 100
	results := make([]engine.PlaceResult, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.Place(limitCmd("alice", "concurrent-coid", "100", "1"))
		}()
	}
	wg.Wait()

	// All goroutines must have succeeded.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	// All results must point to the same *Order (same pointer = same cached result).
	first := results[0].Order
	if first == nil {
		t.Fatal("result[0].Order is nil")
	}
	for i := 1; i < goroutines; i++ {
		if results[i].Order != first {
			t.Fatalf("goroutine %d returned a different *Order pointer — engine was called more than once", i)
		}
	}

	// Control order placed after all goroutines complete. If the engine was
	// called exactly once for the concurrent group, the control gets "o-2".
	ctrl, err := svc.Place(limitCmd("alice", "control-coid", "99", "1"))
	if err != nil {
		t.Fatalf("control place: %v", err)
	}
	if ctrl.Order.ID != "o-2" {
		t.Fatalf("control ID: want o-2 (engine called once for concurrent group), got %s", ctrl.Order.ID)
	}
}

// ---------------------------------------------------------------------------
// TestService_PassthroughCancelSnapshotTrades
//
// Cancel, Snapshot, and Trades must forward to the engine without dedup
// interference. Basic happy-path: place a resting limit, snapshot shows it,
// cancel it, snapshot is now empty, trades returns no trades.
// ---------------------------------------------------------------------------

func TestService_PassthroughCancelSnapshotTrades(t *testing.T) {
	svc := newService(10, 10)

	// Place a resting limit buy at 100.
	res, err := svc.Place(limitCmd("alice", "coid-1", "100", "5"))
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if res.Order.Status != domain.StatusResting {
		t.Fatalf("expected Resting, got %s", res.Order.Status)
	}

	// Snapshot depth=5 must show one bid level at 100.
	bids, asks := svc.Snapshot(5)
	if len(asks) != 0 {
		t.Fatalf("snapshot: expected 0 asks, got %d", len(asks))
	}
	if len(bids) != 1 {
		t.Fatalf("snapshot: expected 1 bid level, got %d", len(bids))
	}
	if !bids[0].Price.Equal(mustDec("100")) {
		t.Fatalf("snapshot: bid price want 100, got %s", bids[0].Price.String())
	}
	if !bids[0].Quantity.Equal(mustDec("5")) {
		t.Fatalf("snapshot: bid quantity want 5, got %s", bids[0].Quantity.String())
	}

	// Cancel the order.
	cancelled, err := svc.Cancel(res.Order.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != domain.StatusCancelled {
		t.Fatalf("cancel: expected Cancelled, got %s", cancelled.Status)
	}

	// Snapshot must now be empty.
	bids, asks = svc.Snapshot(5)
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("snapshot after cancel: expected empty book, got bids=%d asks=%d", len(bids), len(asks))
	}

	// Trades returns empty (no fills occurred).
	trades := svc.Trades(10)
	if len(trades) != 0 {
		t.Fatalf("trades: expected 0, got %d", len(trades))
	}
}
