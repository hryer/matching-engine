package clock_test

import (
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/ports"
)

// Compile-time interface checks — also exercised by the adapter files
// themselves, but kept here so the test package contributes an additional
// verification layer.
var (
	_ ports.Clock = (*clock.Real)(nil)
	_ ports.Clock = (*clock.Fake)(nil)
)

func TestReal_NowMovesForward(t *testing.T) {
	r := clock.NewReal()
	first := r.Now()
	second := r.Now()
	if second.Before(first) {
		t.Fatalf("Real.Now() went backwards: first=%v second=%v", first, second)
	}
}

func TestFake_NowReturnsSetInstant(t *testing.T) {
	instant := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := clock.NewFake(instant)
	if got := f.Now(); !got.Equal(instant) {
		t.Fatalf("expected %v, got %v", instant, got)
	}
}

func TestFake_Advance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := clock.NewFake(start)

	f.Advance(250 * time.Millisecond)
	want := start.Add(250 * time.Millisecond)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after 250ms: expected %v, got %v", want, got)
	}

	f.Advance(750 * time.Millisecond)
	want = start.Add(1 * time.Second)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after cumulative 1s: expected %v, got %v", want, got)
	}
}

func TestFake_Set(t *testing.T) {
	f := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newInstant := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	f.Set(newInstant)
	if got := f.Now(); !got.Equal(newInstant) {
		t.Fatalf("Set: expected %v, got %v", newInstant, got)
	}
}
