// Package engine — deterministic replay test.
//
// TestDeterministicReplay submits a fixed 50-command sequence to two freshly
// constructed engines (identical Fake clock, fresh Monotonic IDs) and asserts
// that json.Marshal(Trades(1000)) produces byte-identical output.
//
// The test must pass under go test -run TestDeterministicReplay -count=1000.
package engine

import (
	"encoding/json"
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/adapters/publisher/inmem"
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
)

// replayEngine constructs a fresh engine with a Fake clock fixed to a known
// instant and a fresh Monotonic ID generator. Both engines in the replay test
// use the same instant so trade.CreatedAt is byte-identical in JSON.
func replayEngine() *Engine {
	return New(Deps{
		Clock:         clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(500),
		MaxOpenOrders: 10000,
		MaxArmedStops: 1000,
	})
}

// replayCommands is the fixed 50-command sequence submitted to both engines.
// It exercises: resting limits, immediate crosses, partial fills, market orders,
// multi-level sweeps, and self-trade prevention. The sequence is hand-built so
// its expected behaviour is auditable without running the engine.
//
// Prices live in the 95..115 range; quantities 1..8.
// User IDs: "alice", "bob", "carol", "dave", "eve".
var replayCommands = []PlaceCommand{
	// Seed the bid side.
	{UserID: "alice", Side: domain.Buy, Type: domain.Limit, Price: dec("100"), Quantity: dec("5")},
	{UserID: "bob", Side: domain.Buy, Type: domain.Limit, Price: dec("99"), Quantity: dec("3")},
	{UserID: "carol", Side: domain.Buy, Type: domain.Limit, Price: dec("98"), Quantity: dec("4")},
	{UserID: "dave", Side: domain.Buy, Type: domain.Limit, Price: dec("97"), Quantity: dec("2")},
	{UserID: "eve", Side: domain.Buy, Type: domain.Limit, Price: dec("96"), Quantity: dec("6")},

	// Seed the ask side.
	{UserID: "alice", Side: domain.Sell, Type: domain.Limit, Price: dec("101"), Quantity: dec("4")},
	{UserID: "bob", Side: domain.Sell, Type: domain.Limit, Price: dec("102"), Quantity: dec("3")},
	{UserID: "carol", Side: domain.Sell, Type: domain.Limit, Price: dec("103"), Quantity: dec("5")},
	{UserID: "dave", Side: domain.Sell, Type: domain.Limit, Price: dec("104"), Quantity: dec("2")},
	{UserID: "eve", Side: domain.Sell, Type: domain.Limit, Price: dec("105"), Quantity: dec("7")},

	// Cross: bob buys at 101 → matches alice's ask at 101, fills 3 (partial).
	{UserID: "bob", Side: domain.Buy, Type: domain.Limit, Price: dec("101"), Quantity: dec("3")},

	// Cross: carol sells at 100 → matches alice's bid at 100, fills 4 (partial alice, partial carol).
	{UserID: "carol", Side: domain.Sell, Type: domain.Limit, Price: dec("100"), Quantity: dec("4")},

	// Market buy: sweeps best ask(s).
	{UserID: "dave", Side: domain.Buy, Type: domain.Market, Quantity: dec("2")},

	// Market sell: sweeps best bid.
	{UserID: "eve", Side: domain.Sell, Type: domain.Market, Quantity: dec("2")},

	// STP: alice places a sell at 99 — alice already has a bid at 100, so the
	// self-trade prevention fires and cancels the incoming (no trade emitted).
	// (alice's bid at 100 may be partially filled from earlier; if it is gone
	// this command simply rests at 99 — either way, determinism holds.)
	{UserID: "alice", Side: domain.Sell, Type: domain.Limit, Price: dec("99"), Quantity: dec("1")},

	// Add more depth on both sides.
	{UserID: "bob", Side: domain.Buy, Type: domain.Limit, Price: dec("95"), Quantity: dec("8")},
	{UserID: "carol", Side: domain.Sell, Type: domain.Limit, Price: dec("110"), Quantity: dec("8")},
	{UserID: "dave", Side: domain.Buy, Type: domain.Limit, Price: dec("94"), Quantity: dec("3")},
	{UserID: "eve", Side: domain.Sell, Type: domain.Limit, Price: dec("111"), Quantity: dec("3")},
	{UserID: "alice", Side: domain.Buy, Type: domain.Limit, Price: dec("93"), Quantity: dec("5")},

	// Cross: large market buy sweeps multiple ask levels.
	{UserID: "bob", Side: domain.Buy, Type: domain.Market, Quantity: dec("8")},

	// Cross: large market sell sweeps multiple bid levels.
	{UserID: "carol", Side: domain.Sell, Type: domain.Market, Quantity: dec("6")},

	// Limit crosses at aggressive prices.
	{UserID: "dave", Side: domain.Buy, Type: domain.Limit, Price: dec("115"), Quantity: dec("4")},
	{UserID: "eve", Side: domain.Sell, Type: domain.Limit, Price: dec("90"), Quantity: dec("4")},

	// Resting-only limits (no cross expected at these prices).
	{UserID: "alice", Side: domain.Buy, Type: domain.Limit, Price: dec("88"), Quantity: dec("2")},
	{UserID: "bob", Side: domain.Sell, Type: domain.Limit, Price: dec("120"), Quantity: dec("2")},
	{UserID: "carol", Side: domain.Buy, Type: domain.Limit, Price: dec("87"), Quantity: dec("1")},
	{UserID: "dave", Side: domain.Sell, Type: domain.Limit, Price: dec("121"), Quantity: dec("1")},
	{UserID: "eve", Side: domain.Buy, Type: domain.Limit, Price: dec("86"), Quantity: dec("3")},

	// More crosses.
	{UserID: "alice", Side: domain.Sell, Type: domain.Limit, Price: dec("85"), Quantity: dec("3")},
	{UserID: "bob", Side: domain.Buy, Type: domain.Limit, Price: dec("125"), Quantity: dec("2")},
	{UserID: "carol", Side: domain.Sell, Type: domain.Market, Quantity: dec("1")},
	{UserID: "dave", Side: domain.Buy, Type: domain.Market, Quantity: dec("1")},
	{UserID: "eve", Side: domain.Sell, Type: domain.Limit, Price: dec("80"), Quantity: dec("5")},

	// STP attempt: dave places a buy that would hit his own resting sell.
	{UserID: "dave", Side: domain.Buy, Type: domain.Limit, Price: dec("122"), Quantity: dec("1")},

	// Resting limits to fill the spread.
	{UserID: "alice", Side: domain.Buy, Type: domain.Limit, Price: dec("82"), Quantity: dec("4")},
	{UserID: "bob", Side: domain.Sell, Type: domain.Limit, Price: dec("83"), Quantity: dec("4")},
	{UserID: "carol", Side: domain.Buy, Type: domain.Limit, Price: dec("81"), Quantity: dec("2")},
	{UserID: "dave", Side: domain.Sell, Type: domain.Limit, Price: dec("84"), Quantity: dec("2")},
	{UserID: "eve", Side: domain.Buy, Type: domain.Limit, Price: dec("79"), Quantity: dec("6")},

	// Market sweep.
	{UserID: "alice", Side: domain.Buy, Type: domain.Market, Quantity: dec("3")},
	{UserID: "bob", Side: domain.Sell, Type: domain.Market, Quantity: dec("3")},

	// Final resting orders — no crosses.
	{UserID: "carol", Side: domain.Buy, Type: domain.Limit, Price: dec("70"), Quantity: dec("1")},
	{UserID: "dave", Side: domain.Sell, Type: domain.Limit, Price: dec("130"), Quantity: dec("1")},
	{UserID: "eve", Side: domain.Buy, Type: domain.Limit, Price: dec("69"), Quantity: dec("1")},
	{UserID: "alice", Side: domain.Sell, Type: domain.Limit, Price: dec("131"), Quantity: dec("1")},
	{UserID: "bob", Side: domain.Buy, Type: domain.Limit, Price: dec("68"), Quantity: dec("1")},
	{UserID: "carol", Side: domain.Sell, Type: domain.Limit, Price: dec("132"), Quantity: dec("1")},
	{UserID: "dave", Side: domain.Buy, Type: domain.Limit, Price: dec("67"), Quantity: dec("1")},
	{UserID: "eve", Side: domain.Sell, Type: domain.Limit, Price: dec("133"), Quantity: dec("1")},
}

// dec is a convenience constructor used only in the replay command slice.
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("replay dec: " + err.Error())
	}
	return d
}

func init() {
	// Guard: the command slice must contain exactly 50 elements.
	if len(replayCommands) != 50 {
		panic("replayCommands must have exactly 50 elements")
	}
}

// TestDeterministicReplay submits a fixed 50-command sequence to two engines
// constructed with identical Fake clocks and fresh Monotonic ID generators,
// then asserts that json.Marshal(Trades(1000)) is byte-identical.
//
// This test must pass under: go test -run TestDeterministicReplay -count=1000
func TestDeterministicReplay(t *testing.T) {
	e1 := replayEngine()
	e2 := replayEngine()

	for i, cmd := range replayCommands {
		_, err1 := e1.Place(cmd)
		_, err2 := e2.Place(cmd)

		// Both engines must agree on error vs no-error for every command.
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("cmd[%d]: engine error disagreement: e1=%v e2=%v", i, err1, err2)
		}
	}

	trades1 := e1.Trades(1000)
	trades2 := e2.Trades(1000)

	j1, err := json.Marshal(trades1)
	if err != nil {
		t.Fatalf("json.Marshal e1 trades: %v", err)
	}
	j2, err := json.Marshal(trades2)
	if err != nil {
		t.Fatalf("json.Marshal e2 trades: %v", err)
	}

	if string(j1) != string(j2) {
		t.Fatalf("replay non-determinism detected:\ne1: %s\ne2: %s", j1, j2)
	}

	// Sanity: at least one trade must have been produced by this sequence.
	if len(trades1) == 0 {
		t.Fatal("replay: no trades produced — command sequence may be broken")
	}
}
