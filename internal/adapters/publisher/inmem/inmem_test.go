package inmem_test

import (
	"testing"
	"time"

	"matching-engine/internal/adapters/publisher/inmem"
	"matching-engine/internal/domain"
	"matching-engine/internal/ports"
)

// Compile-time interface check.
var _ ports.EventPublisher = (*inmem.Ring)(nil)

// makeTrade is a minimal helper; only ID matters for ordering assertions.
func makeTrade(id string) *domain.Trade {
	return &domain.Trade{
		ID:        id,
		CreatedAt: time.Now(),
	}
}

func TestRing_EmptyReturnsNoTrades(t *testing.T) {
	r := inmem.NewRing(10)
	got := r.Recent(5)
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d trades", len(got))
	}
}

func TestRing_UnderCapacityPreservesOrder(t *testing.T) {
	r := inmem.NewRing(10)
	trades := []*domain.Trade{
		makeTrade("t-1"),
		makeTrade("t-2"),
		makeTrade("t-3"),
	}
	for _, tr := range trades {
		r.Publish(tr)
	}

	got := r.Recent(3)
	// Newest first: t-3, t-2, t-1
	want := []string{"t-3", "t-2", "t-1"}
	if len(got) != len(want) {
		t.Fatalf("expected %d trades, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("index %d: expected %q, got %q", i, id, got[i].ID)
		}
	}
}

func TestRing_OverflowDropsOldest(t *testing.T) {
	r := inmem.NewRing(3)
	for _, id := range []string{"t-1", "t-2", "t-3", "t-4", "t-5"} {
		r.Publish(makeTrade(id))
	}

	// Only t-3, t-4, t-5 should remain; t-1 and t-2 are overwritten.
	got := r.Recent(3)
	want := []string{"t-5", "t-4", "t-3"}
	if len(got) != len(want) {
		t.Fatalf("expected %d trades, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("index %d: expected %q, got %q", i, id, got[i].ID)
		}
	}
}

func TestRing_RecentNewestFirst(t *testing.T) {
	r := inmem.NewRing(10)
	r.Publish(makeTrade("A"))
	r.Publish(makeTrade("B"))
	r.Publish(makeTrade("C"))

	got := r.Recent(3)
	want := []string{"C", "B", "A"}
	if len(got) != len(want) {
		t.Fatalf("expected %d trades, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("index %d: expected %q, got %q", i, id, got[i].ID)
		}
	}
}

func TestRing_LimitClamping(t *testing.T) {
	r := inmem.NewRing(10)
	r.Publish(makeTrade("t-1"))
	r.Publish(makeTrade("t-2"))

	// limit > count: should return all 2 trades
	got := r.Recent(10)
	if len(got) != 2 {
		t.Fatalf("Recent(10) with 2 stored: expected 2, got %d", len(got))
	}

	// limit <= 0: should return empty
	for _, lim := range []int{0, -1, -100} {
		got = r.Recent(lim)
		if len(got) != 0 {
			t.Fatalf("Recent(%d): expected empty, got %d trades", lim, len(got))
		}
	}
}

func TestRing_PanicOnZeroCapacity(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected NewRing(0) to panic, but it did not")
		}
	}()
	inmem.NewRing(0)
}
