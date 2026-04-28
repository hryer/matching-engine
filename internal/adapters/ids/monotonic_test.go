package ids_test

import (
	"testing"

	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.IDGenerator = (*ids.Monotonic)(nil)

func TestMonotonic_OrderIDStartsAtOne(t *testing.T) {
	m := ids.NewMonotonic()
	got := m.NextOrderID()
	if got != "o-1" {
		t.Fatalf("expected o-1, got %q", got)
	}
}

func TestMonotonic_TradeIDStartsAtOne(t *testing.T) {
	m := ids.NewMonotonic()
	got := m.NextTradeID()
	if got != "t-1" {
		t.Fatalf("expected t-1, got %q", got)
	}
}

func TestMonotonic_CountersIndependent(t *testing.T) {
	m := ids.NewMonotonic()

	// Interleave order and trade ID calls and assert each sequence is
	// independent — advancing one counter must not affect the other.
	want := []struct {
		fn   func() string
		want string
	}{
		{m.NextOrderID, "o-1"},
		{m.NextTradeID, "t-1"},
		{m.NextOrderID, "o-2"},
		{m.NextTradeID, "t-2"},
		{m.NextOrderID, "o-3"},
		{m.NextTradeID, "t-3"},
	}

	for i, tc := range want {
		got := tc.fn()
		if got != tc.want {
			t.Fatalf("step %d: expected %q, got %q", i, tc.want, got)
		}
	}
}

func TestMonotonic_FormatString(t *testing.T) {
	m := ids.NewMonotonic()

	orderID := m.NextOrderID()
	if len(orderID) < 2 || orderID[:2] != "o-" {
		t.Fatalf("order ID %q does not have prefix \"o-\"", orderID)
	}

	tradeID := m.NextTradeID()
	if len(tradeID) < 2 || tradeID[:2] != "t-" {
		t.Fatalf("trade ID %q does not have prefix \"t-\"", tradeID)
	}
}
