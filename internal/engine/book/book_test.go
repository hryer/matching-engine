package book

import (
	"testing"

	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// dec parses a decimal string for tests; panics on bad input.
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// mkOrder constructs a resting-eligible Order with the bare minimum fields
// the book cares about. The book never reads UserID, Type, Status, Quantity,
// CreatedAt, or seq directly.
func mkOrder(id string, side domain.Side, price, qty string) *domain.Order {
	return &domain.Order{
		ID:                id,
		UserID:            "u-" + id,
		Side:              side,
		Type:              domain.Limit,
		Price:             dec(price),
		Quantity:          dec(qty),
		RemainingQuantity: dec(qty),
		Status:            domain.StatusResting,
	}
}

func TestBook_InsertAtNewLevel(t *testing.T) {
	b := New()
	o := mkOrder("o-1", domain.Buy, "100", "1.5")
	b.Insert(o)

	lvl := b.BestLevel(domain.Buy)
	if lvl == nil {
		t.Fatal("BestLevel(Buy) is nil after insert")
	}
	if !lvl.Price.Equal(dec("100")) {
		t.Errorf("level price: want 100, got %s", lvl.Price)
	}
	if !lvl.Total.Equal(dec("1.5")) {
		t.Errorf("level total: want 1.5, got %s", lvl.Total)
	}
	if lvl.Orders.Len() != 1 {
		t.Errorf("level FIFO length: want 1, got %d", lvl.Orders.Len())
	}
	if o.Elem() == nil || o.Level() == nil {
		t.Error("back-pointers (Elem, Level) not set on inserted order")
	}
}

func TestBook_InsertAtExistingLevelIsFIFO(t *testing.T) {
	b := New()
	a := mkOrder("o-A", domain.Buy, "100", "1")
	c := mkOrder("o-B", domain.Buy, "100", "2")
	b.Insert(a)
	b.Insert(c)

	lvl := b.BestLevel(domain.Buy)
	if lvl.Orders.Len() != 2 {
		t.Fatalf("level len: want 2, got %d", lvl.Orders.Len())
	}
	front := lvl.Orders.Front().Value.(*domain.Order)
	if front.ID != "o-A" {
		t.Errorf("FIFO front: want o-A, got %s", front.ID)
	}
	if !lvl.Total.Equal(dec("3")) {
		t.Errorf("total: want 3, got %s", lvl.Total)
	}
}

func TestBook_BestBidIsMax(t *testing.T) {
	b := New()
	b.Insert(mkOrder("o-1", domain.Buy, "100", "1"))
	b.Insert(mkOrder("o-2", domain.Buy, "102", "1"))
	b.Insert(mkOrder("o-3", domain.Buy, "101", "1"))

	best := b.BestLevel(domain.Buy)
	if best == nil {
		t.Fatal("BestLevel(Buy) nil")
	}
	if !best.Price.Equal(dec("102")) {
		t.Errorf("best bid: want 102, got %s", best.Price)
	}
}

func TestBook_BestAskIsMin(t *testing.T) {
	b := New()
	b.Insert(mkOrder("o-1", domain.Sell, "100", "1"))
	b.Insert(mkOrder("o-2", domain.Sell, "102", "1"))
	b.Insert(mkOrder("o-3", domain.Sell, "101", "1"))

	best := b.BestLevel(domain.Sell)
	if best == nil {
		t.Fatal("BestLevel(Sell) nil")
	}
	if !best.Price.Equal(dec("100")) {
		t.Errorf("best ask: want 100, got %s", best.Price)
	}
}

func TestBook_BestLevelOnEmptySideIsNil(t *testing.T) {
	b := New()
	if b.BestLevel(domain.Buy) != nil {
		t.Error("BestLevel(Buy) on empty book: want nil")
	}
	if b.BestLevel(domain.Sell) != nil {
		t.Error("BestLevel(Sell) on empty book: want nil")
	}
}

func TestBook_CancelLastOrderRemovesLevel(t *testing.T) {
	b := New()
	o := mkOrder("o-1", domain.Buy, "100", "1")
	b.Insert(o)
	b.Cancel(o)

	if b.BestLevel(domain.Buy) != nil {
		t.Error("BestLevel(Buy) after cancelling sole order: want nil")
	}
	if o.Elem() != nil || o.Level() != nil {
		t.Error("back-pointers not cleared after Cancel")
	}
}

func TestBook_CancelMidLevelDecrementsTotal(t *testing.T) {
	b := New()
	a := mkOrder("o-A", domain.Buy, "100", "1.5")
	c := mkOrder("o-B", domain.Buy, "100", "0.7")
	b.Insert(a)
	b.Insert(c)

	b.Cancel(a)

	lvl := b.BestLevel(domain.Buy)
	if lvl == nil {
		t.Fatal("level removed unexpectedly")
	}
	if lvl.Orders.Len() != 1 {
		t.Errorf("level len after cancel: want 1, got %d", lvl.Orders.Len())
	}
	if !lvl.Total.Equal(dec("0.7")) {
		t.Errorf("total: want 0.7, got %s", lvl.Total)
	}
	front := lvl.Orders.Front().Value.(*domain.Order)
	if front.ID != "o-B" {
		t.Errorf("FIFO front after cancel: want o-B, got %s", front.ID)
	}
}

func TestBook_CancelOnNonRestingIsNoop(t *testing.T) {
	b := New()
	o := mkOrder("o-1", domain.Buy, "100", "1")
	// Not inserted; back-pointers nil.
	b.Cancel(o) // must not panic
}

func TestBook_TotalConsistencyAfterMixedOps(t *testing.T) {
	b := New()
	a := mkOrder("o-A", domain.Sell, "200", "1")
	c := mkOrder("o-B", domain.Sell, "200", "2")
	d := mkOrder("o-C", domain.Sell, "200", "3")
	b.Insert(a)
	b.Insert(c)
	b.Cancel(c)
	b.Insert(d)

	lvl := b.BestLevel(domain.Sell)
	want := dec("4") // 1 + 3
	if !lvl.Total.Equal(want) {
		t.Errorf("total: want %s, got %s", want, lvl.Total)
	}
	if lvl.Orders.Len() != 2 {
		t.Errorf("level len: want 2, got %d", lvl.Orders.Len())
	}

	// Spot-check the invariant directly.
	var sum decimal.Decimal = decimal.Zero
	for e := lvl.Orders.Front(); e != nil; e = e.Next() {
		sum = sum.Add(e.Value.(*domain.Order).RemainingQuantity)
	}
	if !sum.Equal(lvl.Total) {
		t.Errorf("Total invariant violated: sum=%s, Total=%s", sum, lvl.Total)
	}
}

func TestBook_SnapshotDepth(t *testing.T) {
	b := New()
	for i, p := range []string{"100", "101", "102", "103", "104"} {
		// Each level has one order, qty (i+1)/10 — distinct totals to detect ordering bugs.
		_ = i
		b.Insert(mkOrder("b-"+p, domain.Buy, p, "1"))
		b.Insert(mkOrder("a-"+p, domain.Sell, p, "1"))
	}

	cases := []struct {
		name      string
		depth     int
		wantBids  int
		wantAsks  int
		wantBidPx string
		wantAskPx string
	}{
		{"depth 0", 0, 0, 0, "", ""},
		{"depth 1", 1, 1, 1, "104", "100"},
		{"depth 5 exact", 5, 5, 5, "104", "100"},
		{"depth 1000 (capped same)", 1000, 5, 5, "104", "100"},
		{"depth 5000 (clamped to 1000, returns all 5)", 5000, 5, 5, "104", "100"},
		{"depth -1 (clamped to 0)", -1, 0, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bids, asks := b.Snapshot(tc.depth)
			if len(bids) != tc.wantBids {
				t.Errorf("bids len: want %d, got %d", tc.wantBids, len(bids))
			}
			if len(asks) != tc.wantAsks {
				t.Errorf("asks len: want %d, got %d", tc.wantAsks, len(asks))
			}
			if tc.wantBidPx != "" && !bids[0].Price.Equal(dec(tc.wantBidPx)) {
				t.Errorf("first bid price: want %s, got %s", tc.wantBidPx, bids[0].Price)
			}
			if tc.wantAskPx != "" && !asks[0].Price.Equal(dec(tc.wantAskPx)) {
				t.Errorf("first ask price: want %s, got %s", tc.wantAskPx, asks[0].Price)
			}
		})
	}
}

func TestBook_SnapshotOrderingIsBestToWorst(t *testing.T) {
	b := New()
	b.Insert(mkOrder("b-100", domain.Buy, "100", "1"))
	b.Insert(mkOrder("b-102", domain.Buy, "102", "1"))
	b.Insert(mkOrder("b-101", domain.Buy, "101", "1"))
	b.Insert(mkOrder("a-103", domain.Sell, "103", "1"))
	b.Insert(mkOrder("a-105", domain.Sell, "105", "1"))
	b.Insert(mkOrder("a-104", domain.Sell, "104", "1"))

	bids, asks := b.Snapshot(10)
	wantBids := []string{"102", "101", "100"} // descending
	wantAsks := []string{"103", "104", "105"} // ascending
	for i, want := range wantBids {
		if !bids[i].Price.Equal(dec(want)) {
			t.Errorf("bid[%d]: want %s, got %s", i, want, bids[i].Price)
		}
	}
	for i, want := range wantAsks {
		if !asks[i].Price.Equal(dec(want)) {
			t.Errorf("ask[%d]: want %s, got %s", i, want, asks[i].Price)
		}
	}
}

func TestBook_PriceKeyCanonicalisation(t *testing.T) {
	// Direct check on the helper.
	cases := []struct{ a, b string }{
		{"500000000", "500000000.0"},
		{"500000000", "500000000.00"},
		{"100", "100.0"},
		{"0", "0.0"},
		{"0.5", "0.50"},
		{"1.23", "1.230"},
	}
	for _, c := range cases {
		ka := priceKey(dec(c.a))
		kb := priceKey(dec(c.b))
		if ka != kb {
			t.Errorf("priceKey(%s)=%q vs priceKey(%s)=%q (should collide)", c.a, ka, c.b, kb)
		}
	}

	// Different values must NOT collide.
	if priceKey(dec("100")) == priceKey(dec("1000")) {
		t.Error("priceKey(100) and priceKey(1000) collided")
	}

	// Round-trip via the book: two orders at logically-equal prices land on
	// one level.
	b := New()
	b.Insert(mkOrder("o-1", domain.Buy, "500000000", "1"))
	b.Insert(mkOrder("o-2", domain.Buy, "500000000.0", "2"))
	bids, _ := b.Snapshot(10)
	if len(bids) != 1 {
		t.Fatalf("equal-value prices on different keys: want 1 level, got %d", len(bids))
	}
	if !bids[0].Quantity.Equal(dec("3")) {
		t.Errorf("collapsed level total: want 3, got %s", bids[0].Quantity)
	}
}

func TestBook_RemoveFilledMaker(t *testing.T) {
	b := New()
	a := mkOrder("o-A", domain.Sell, "200", "1")
	c := mkOrder("o-B", domain.Sell, "200", "2")
	b.Insert(a)
	b.Insert(c)
	lvl := a.Level().(*PriceLevel)

	// Simulate the matcher fully filling A:
	//   1. matcher decrements level.Total by fillQty (= 1)
	//   2. matcher zeroes a.RemainingQuantity
	//   3. matcher calls RemoveFilledMaker
	lvl.Total = lvl.Total.Sub(dec("1"))
	a.RemainingQuantity = decimal.Zero
	b.RemoveFilledMaker(lvl, a)

	got := b.BestLevel(domain.Sell)
	if got == nil {
		t.Fatal("level removed when it should still hold o-B")
	}
	if got.Orders.Len() != 1 {
		t.Errorf("level len after removing A: want 1, got %d", got.Orders.Len())
	}
	front := got.Orders.Front().Value.(*domain.Order)
	if front.ID != "o-B" {
		t.Errorf("FIFO front after RemoveFilledMaker: want o-B, got %s", front.ID)
	}
	if !got.Total.Equal(dec("2")) {
		t.Errorf("total after removing A: want 2 (matcher already decremented), got %s", got.Total)
	}
	if a.Elem() != nil || a.Level() != nil {
		t.Error("RemoveFilledMaker did not clear back-pointers on A")
	}
}

func TestBook_RemoveFilledMakerEmptiesLevel(t *testing.T) {
	b := New()
	o := mkOrder("o-1", domain.Sell, "200", "1")
	b.Insert(o)
	lvl := o.Level().(*PriceLevel)

	// Fully fill the only resting order at this level.
	lvl.Total = lvl.Total.Sub(dec("1"))
	o.RemainingQuantity = decimal.Zero
	b.RemoveFilledMaker(lvl, o)

	if b.BestLevel(domain.Sell) != nil {
		t.Error("level not removed after RemoveFilledMaker emptied it")
	}
}
