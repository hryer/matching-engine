package stops

import (
	"testing"

	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// dec is a test helper that parses a decimal string and panics on
// malformed input — acceptable in tests, never in production code.
func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decimal.NewFromString(%q): %v", s, err)
	}
	return d
}

// mkStop builds a minimal *domain.Order suitable for the stop book.
// The engine itself sets Status / Type / Quantity differently in
// production — here we only care about the fields StopBook reads
// (ID, Side, TriggerPrice, seq).
func mkStop(t *testing.T, id string, side domain.Side, ty domain.Type, trigger string, seq uint64) *domain.Order {
	t.Helper()
	o := &domain.Order{
		ID:           id,
		UserID:       "u-1",
		Side:         side,
		Type:         ty,
		TriggerPrice: dec(t, trigger),
		Quantity:     dec(t, "1"),
		Status:       domain.StatusArmed,
	}
	o.SetSeq(seq)
	return o
}

func TestStops_InsertAndGet(t *testing.T) {
	s := New()
	o := mkStop(t, "o1", domain.Buy, domain.Stop, "100", 1)
	s.Insert(o)

	got, ok := s.Get("o1")
	if !ok {
		t.Fatalf("Get(\"o1\"): want ok=true, got false")
	}
	if got != o {
		t.Fatalf("Get(\"o1\"): pointer mismatch")
	}

	if _, ok := s.Get("missing"); ok {
		t.Fatalf("Get(\"missing\"): want ok=false, got true")
	}
}

func TestStops_CancelRemovesFromBoth(t *testing.T) {
	s := New()
	o := mkStop(t, "o1", domain.Buy, domain.Stop, "100", 1)
	s.Insert(o)

	got, ok := s.Cancel("o1")
	if !ok || got != o {
		t.Fatalf("Cancel(\"o1\"): want (o, true), got (%v, %v)", got, ok)
	}

	// Cancelled order must be gone from byID *and* from the btree:
	// a DrainTriggered at a price that would otherwise have fired must
	// return nothing.
	if fired := s.DrainTriggered(dec(t, "200")); len(fired) != 0 {
		t.Fatalf("DrainTriggered after cancel: want empty, got %d orders", len(fired))
	}
	if _, ok := s.Get("o1"); ok {
		t.Fatalf("Get after cancel: want ok=false, got true")
	}
	if s.buys.Len() != 0 || s.sells.Len() != 0 {
		t.Fatalf("btree size after cancel: buys=%d sells=%d", s.buys.Len(), s.sells.Len())
	}
}

func TestStops_CancelMissingReturnsFalse(t *testing.T) {
	s := New()
	got, ok := s.Cancel("does-not-exist")
	if ok || got != nil {
		t.Fatalf("Cancel(missing): want (nil, false), got (%v, %v)", got, ok)
	}
}

func TestStops_DrainTriggeredEmpty(t *testing.T) {
	s := New()
	if got := s.DrainTriggered(dec(t, "100")); len(got) != 0 {
		t.Fatalf("DrainTriggered on empty: want empty, got %d orders", len(got))
	}
}

func TestStops_DrainTriggeredSingleBuy(t *testing.T) {
	s := New()
	o := mkStop(t, "b1", domain.Buy, domain.Stop, "100", 1)
	s.Insert(o)

	fired := s.DrainTriggered(dec(t, "100")) // inclusive: equal triggers
	if len(fired) != 1 || fired[0] != o {
		t.Fatalf("buy stop at trigger=100, last=100: want [b1], got %+v", fired)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after drain: want 0, got %d", s.Len())
	}
}

func TestStops_DrainTriggeredSingleSell(t *testing.T) {
	s := New()
	o := mkStop(t, "s1", domain.Sell, domain.Stop, "100", 1)
	s.Insert(o)

	fired := s.DrainTriggered(dec(t, "100")) // inclusive: equal triggers
	if len(fired) != 1 || fired[0] != o {
		t.Fatalf("sell stop at trigger=100, last=100: want [s1], got %+v", fired)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after drain: want 0, got %d", s.Len())
	}
}

func TestStops_DrainTriggeredMultipleBySeq(t *testing.T) {
	s := New()
	// Three buy stops at the same trigger, inserted out of seq order.
	a := mkStop(t, "a", domain.Buy, domain.Stop, "100", 3)
	b := mkStop(t, "b", domain.Buy, domain.Stop, "100", 1)
	c := mkStop(t, "c", domain.Buy, domain.Stop, "100", 2)
	s.Insert(a)
	s.Insert(b)
	s.Insert(c)

	fired := s.DrainTriggered(dec(t, "100"))
	if len(fired) != 3 {
		t.Fatalf("want 3 fired, got %d", len(fired))
	}
	if fired[0] != b || fired[1] != c || fired[2] != a {
		t.Fatalf("want seq order [b(1), c(2), a(3)]; got [%s(%d), %s(%d), %s(%d)]",
			fired[0].ID, fired[0].Seq(),
			fired[1].ID, fired[1].Seq(),
			fired[2].ID, fired[2].Seq(),
		)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after drain: want 0, got %d", s.Len())
	}
}

func TestStops_DrainTriggeredCrossSide(t *testing.T) {
	s := New()
	// Buy fires (trigger <= last) and sell fires (trigger >= last) at last==100.
	// Sell has the smaller seq so it must come first in the result regardless
	// of the order in which the two sides were drained internally.
	sell := mkStop(t, "s1", domain.Sell, domain.Stop, "100", 1)
	buy := mkStop(t, "b1", domain.Buy, domain.Stop, "100", 2)
	s.Insert(buy)
	s.Insert(sell)

	fired := s.DrainTriggered(dec(t, "100"))
	if len(fired) != 2 {
		t.Fatalf("want 2 fired, got %d", len(fired))
	}
	if fired[0] != sell || fired[1] != buy {
		t.Fatalf("want [s1(seq=1), b1(seq=2)]; got [%s(%d), %s(%d)]",
			fired[0].ID, fired[0].Seq(),
			fired[1].ID, fired[1].Seq(),
		)
	}
}

func TestStops_CancelledDoesNotFire(t *testing.T) {
	s := New()
	o := mkStop(t, "b1", domain.Buy, domain.Stop, "100", 1)
	s.Insert(o)

	if _, ok := s.Cancel("b1"); !ok {
		t.Fatalf("Cancel(\"b1\"): want ok=true")
	}

	// Drive the price well past the trigger — the cancelled stop must
	// not surface as a "ghost" trigger (see §05 cancel-of-armed-stop).
	if fired := s.DrainTriggered(dec(t, "999")); len(fired) != 0 {
		t.Fatalf("cancelled stop fired at last=999: want empty, got %d", len(fired))
	}
}

func TestStops_LenMatchesByID(t *testing.T) {
	s := New()
	checkLen := func(label string, want int) {
		t.Helper()
		if got := s.Len(); got != want {
			t.Fatalf("%s: Len=%d, want %d", label, got, want)
		}
		if got := len(s.byID); got != want {
			t.Fatalf("%s: len(byID)=%d, want %d", label, got, want)
		}
		if got := s.buys.Len() + s.sells.Len(); got != want {
			t.Fatalf("%s: buys+sells=%d, want %d", label, got, want)
		}
	}

	checkLen("empty", 0)

	s.Insert(mkStop(t, "b1", domain.Buy, domain.Stop, "100", 1))
	s.Insert(mkStop(t, "s1", domain.Sell, domain.Stop, "200", 2))
	s.Insert(mkStop(t, "b2", domain.Buy, domain.StopLimit, "150", 3))
	checkLen("after 3 inserts", 3)

	if _, ok := s.Cancel("s1"); !ok {
		t.Fatalf("Cancel(\"s1\") missing")
	}
	checkLen("after cancel s1", 2)

	// Drain b1 only (trigger=100 <= last=120; b2 trigger=150 does not fire).
	fired := s.DrainTriggered(dec(t, "120"))
	if len(fired) != 1 || fired[0].ID != "b1" {
		t.Fatalf("partial drain at last=120: want [b1], got %+v", fired)
	}
	checkLen("after partial drain", 1)

	// Drain remaining (b2 trigger=150 fires at last=150).
	fired = s.DrainTriggered(dec(t, "150"))
	if len(fired) != 1 || fired[0].ID != "b2" {
		t.Fatalf("final drain: want [b2], got %+v", fired)
	}
	checkLen("after final drain", 0)
}
