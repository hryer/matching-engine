---
name: Engine core — T-010/T-011 test suite notes
description: QA findings, test design traps, and production bugs uncovered while writing engine tests (T-010) and property/replay tests (T-011)
type: project
---

QA wrote 70 tests across 4 files in `internal/engine/` (package engine). T-011 adds 2 more files (property + replay).

**Why:** All §9 plan cases covered + adversarial cases + 5000-op fuzz-style invariant test.

## Test file layout

- `engine_match_test.go` — Limit/Market matching, STP, FIFO, Snapshot, Trades, decimal canonicalisation.
- `engine_stop_test.go` — Stop/StopLimit arming, rejection, firing, cascade depth≥2, seq-order firing.
- `engine_cap_test.go` — ErrTooManyOrders, ErrTooManyStops, decision (b) partial-fill-then-cap-hit, cascade overshoot.
- `engine_invariant_test.go` — 100-cycle counter consistency, 5000-op seeded fuzz, determinism replay, large qty, sell-stop-at-zero boundary.

## Production bugs found: NONE

All 70 tests pass. The implementation matches the plan contract exactly.

## Tricky test design issues (not bugs — test author traps)

**Decision (b) test**: The plan §9 says "MaxOpenOrders=1, place a resting limit, then a cross taker that partial-fills resting". This scenario does NOT trigger decision (b) cap-hit if the maker is FULLY consumed (openOrders drops to 0, so 0+1=1 <= 1 = no cap). The cap only fires if a resting order SURVIVES after the fills, keeping openOrders at max. The canonical setup uses cascade bypass to plant two resting buys (openOrders=2 > maxOpenOrders=1), then a sell taker at a price that crosses only one bid and has remainder blocked by price gate, triggering the cap on the remainder.

**Cascade chain test (depth≥2)**: Stop A is a buy stop-limit. When it fires, it becomes Limit(price). If there are asks at prices BELOW the limit price, A fills against those CHEAPER asks first. The resulting trade price may never reach the level needed to trigger stop B. Fix: ensure no asks exist at prices below the level that triggers B. The ask that triggers B must be the ONLY available ask when A fires.

**Cascade overshoot test**: Cannot use "place a sell at 110, then buy at 110" when openOrders is already at max — the sell placement itself would cap-reject. Instead arm two stop-limits at the same trigger; neither has ask liquidity; both rest from cascade (bypassing cap). openOrders ends at 2 > maxOpenOrders=1.

**Sell stop on fresh engine (§11.1)**: Any sell stop placed before the first trade is rejected because lastTradePrice=0 and trigger(any positive) >= 0. Tests that arm sell stops MUST first drive a trade to establish lastTradePrice > trigger. The `newEngineWithLastPrice` helper does this.

## T-011 property + replay tests

`engine_property_test.go` — `TestEngine_Property_AllInvariants`: 50 seeds × 100 ops; checks Inv 1, 2, 7, 8, 17, PriceLevelTotal after every op.
`replay_test.go` — `TestDeterministicReplay`: fixed 50-command slice, two engines, `json.Marshal(Trades(1000))` byte-compared; passes -count=1000.

**Key T-011 design decisions:**

- `invPriceLevelTotal` uses Snapshot to enumerate price levels (book internals not exported), then cross-checks against `e.byID` sum at each price — catches Total desync observable to Snapshot consumers.
- `inv7NoSelfTrade` needs an `orderID → userID` side table maintained next to the engine because trades don't embed UserIDs and filled orders are evicted from `e.byID`.
- `inv2FillsBound` processes only unseen trade IDs each op (via `seenTradeIDs` set) to avoid O(n²) full-rescan; still reads `Recent(20000)` to catch cascade-emitted trades.
- Stop/StopLimit ops in the property test are gated on `hadTrade` to avoid the fresh-engine sell-stop rejection noise (§11.1). Trigger ranges are buy: 201..250, sell: 50..99, well outside the 100..200 price band.
- Replay engine uses identical `Fake` clock (fixed instant, never auto-advances) and fresh `Monotonic` IDs per engine — the sole sources of non-determinism if any had existed.

**Production code change recommended (not made):** Add `(*Engine).Counters() (openOrders, armedStops int)` or an unexported `snapshotForTest()` for cleaner invariant access from tests. Currently the test reaches directly into `e.openOrders`, `e.armedStops`, `e.byID`, `e.stops`, `e.book`, `e.history` — all legal because `package engine`, but a documented accessor would be safer against future field renames.

## Invariant helper

`checkInvariants(t, e)` is defined in `engine_match_test.go` and verifies:
- `openOrders == len(byID)` 
- `armedStops == stops.Len()`
- No terminal orders in byID

Call it after every public method in every test.

**How to apply:** When adding new tests, use `newEngineWithLastPrice` whenever sell stops need to arm. Always call `checkInvariants` after every Place/Cancel. Decision (b) cap-hit tests need cascade overshoot setup to guarantee openOrders stays at max after fills.
