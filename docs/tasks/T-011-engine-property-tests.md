# T-011 — Engine property + replay tests

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.0 h (±25%) |
| Owner | unassigned |
| Parallel batch | B4 |
| Blocks | T-017 |
| Blocked by | T-010 |
| Touches files | `internal/engine/engine_property_test.go`, `internal/engine/replay_test.go` |

## Goal

Build the seeded-random property test that exercises invariants 1, 2, 7, 8, 12, 15, 17 from [`ARCHITECT_PLAN.md` §3](../system_design/ARCHITECT_PLAN.md#3-critical-correctness-invariants), and the deterministic replay test that runs 1000× for byte-identical trade JSON. Specified in [§09 Testing](../system_design/09-testing.md#layer-2--property--invariant-tests) and [§06 Determinism](../system_design/06-concurrency-and-determinism.md#determinism-replay-test).

## Context

The property test layer is what catches the bugs we didn't think of. It runs random valid order streams (small and bounded) and after each operation asserts that the engine's invariants still hold.

The replay test runs a fixed sequence of ~50 orders against two freshly-constructed engines with the same `Fake` clock seed and `Monotonic` IDs, and asserts that the resulting trade slice marshals to byte-identical JSON. With `go test -count=1000` it catches any latent nondeterminism.

## Acceptance criteria

- [ ] `engine_property_test.go` defines a hand-rolled seeded generator (no external `gopter` / `quick`)
- [ ] Operation set: `Place(Limit)`, `Place(Market)`, `Place(Stop)`, `Place(StopLimit)`, `Cancel`. Bounded user pool (~5 user IDs), bounded price range (e.g. 100..200), bounded quantity (e.g. 1..10), bounded stream length (e.g. 100 ops × 100 iterations)
- [ ] After every operation, assert:
    - **Inv 1 — book never crosses:** if both bids and asks have entries, `bestBid.Price < bestAsk.Price` (strict; equality is also a violation per [`ARCHITECT_PLAN.md` §3](../system_design/ARCHITECT_PLAN.md))
    - **Inv 2 — sum-of-fills bound:** for every order touched, `sum(trade.Quantity where this order participated) <= original Quantity`
    - **Inv 7 — no self-trade:** no trade has `taker.UserID == maker.UserID`
    - **Inv 8 — armed stops invisible in snapshot:** `Snapshot()` output never contains a price level whose total comes from armed stops (achievable by checking `len(snapshot.bids) + len(snapshot.asks)` matches `count(orders resting in book)` only)
    - **Inv 15 — map iteration never affects output:** any ordered traversal goes through the btree (this is structural; not directly assertable but covered by the determinism replay)
    - **Inv 17 — counter consistency:** `engine.openOrders == count(byID resting)` and `engine.armedStops == stops.Len()` after every operation
    - **PriceLevel.Total consistency:** for every level in the book, `level.Total == sum(o.RemainingQuantity for o in level.Orders)`
- [ ] On any invariant failure, `t.Fatalf` with the seed, the operation index, and a dump of the offending state — the report must be enough to reproduce locally
- [ ] `replay_test.go` defines `TestDeterministicReplay`:
    - Build two engines with identical `Fake` clock (seeded at a fixed instant) and fresh `Monotonic` IDs
    - Submit a fixed 50-order command sequence to each (the sequence may be hand-built or seeded-random; either way, both engines see byte-identical commands in the same order)
    - Marshal `Trades(1000)` of each via `json.Marshal`
    - Assert the two byte slices are equal
    - The test must pass under `go test -run TestDeterministicReplay -count=1000`
- [ ] `go test ./internal/engine/... -race -count=10` clean
- [ ] `go test ./internal/engine/... -run TestDeterministicReplay -count=1000` clean

## Implementation notes

- Generator: `math/rand.New(rand.NewSource(seed))` with a fixed seed list (e.g. seeds `1..100`). This is the "seeded random" mentioned in [§09](../system_design/09-testing.md#layer-2--property--invariant-tests).
- For each iteration: pick op type by weighted random (limit > market > stop > stop-limit > cancel), pick parameters, submit. Reject silently any op that the engine validly rejects (e.g. Cancel on a non-existent ID — that's not an invariant failure, it's expected behaviour).
- For Inv 2, maintain a side-table `map[orderID]*decimal.Decimal` of cumulative-fills-per-order. On every trade, increment both maker and taker entries. After each op, scan and assert `cumFill <= originalQty`.
- For Inv 8, the cleanest assertion is to compare `Snapshot` quantity totals against a fresh recount of the engine's `byID` (resting orders only — the engine knows nothing about armed stops in `byID`). The condition is: `sum(snapshot.bids[i].Quantity) == sum(o.RemainingQuantity for o in byID where o.Side == Buy)`. Same for asks.
- For Inv 17, the property test must reach into the engine's internal counters. Either (a) expose an unexported helper `(*Engine).snapshotForTest() (openOrders, armedStops int)` accessible because the test is in the same package, or (b) add an exported `(*Engine).Counters()` method documented as "for diagnostics; counters reflect engine state at-call." Option (a) is cleaner.
- Replay test seed strategy: fix the command sequence in code (a literal slice of `PlaceCommand`). Two engines, same sequence, byte-compare JSON. `Trade.CreatedAt` is driven by `Fake.Now()` which doesn't auto-advance — make the test advance the clock between commands by a fixed delta (e.g. 1ms each) or accept identical timestamps and rely on `seq` for FIFO. Identical timestamps are simpler; trades are differentiated by `id` (`"t-1"`, `"t-2"`, ...).
- The `cancel` op needs an ID to target. Maintain a side-table of currently-known order IDs from previous `Place` results. Pick uniformly from this table; expect both successes and `ErrOrderNotFound` / `ErrAlreadyTerminal` (those are not invariant failures).
- Do not test the cap-hit error paths in the property test — they are covered by T-010's hand-crafted tests with small caps. The property test runs at production caps (or a higher synthetic cap) so cap errors do not interfere.

## Out of scope

- HTTP-level integration test (T-015).
- Cap-hit and partial-fill-then-cap edge cases (T-010).
- Performance / latency benchmarks (out of v1 scope; [§10](../system_design/10-hft-considerations.md) discusses what would be measured).
- Fuzzing with `go test -fuzz` (out of scope; could be added in v2).

## Tests required

- `TestEngine_Property_*` per invariant (or one master `TestEngine_Property_AllInvariants` that checks all in a single pass per op — recommended for speed)
- `TestDeterministicReplay` — passes 1000×

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `-race -count=10` clean
- [ ] `-count=1000` of replay clean
- [ ] On a deliberately-broken engine (e.g. delete a `Cmp` to force a numeric mistake), the property test fails with an actionable error
- [ ] Test file is `package engine` (same package as engine.go), enabling internal counter access without exporting it
