# T-009 — StopBook + cascade primitives

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.0 h (±25%) |
| Owner | unassigned |
| Parallel batch | B2 |
| Blocks | T-010 |
| Blocked by | T-002, T-004 |
| Touches files | `internal/engine/stops/stops.go`, `internal/engine/stops/stops_test.go` |

## Goal

Implement the stop book: two btrees (ascending buy, descending sell) plus a `byID` map for O(1) cancel. Provide the primitives the engine needs to drain triggered stops deterministically. Specified in [§05 Stop Orders & Cascade](../system_design/05-stop-orders.md).

## Context

The stop book is symmetric with the order book — same dependency (`google/btree`), same iteration pattern, same cancel-by-ID indirection.

- `buys` is sorted **ascending** by `TriggerPrice` so the smallest-trigger buy stop sits at the head and fires first when `lastTradePrice` rises.
- `sells` is sorted **descending** by `TriggerPrice` so the largest-trigger sell stop sits at the head and fires first when `lastTradePrice` falls.
- `byID map[string]*domain.Order` allows O(1) cancel of an armed stop.

The engine (T-010) calls `DrainTriggered(lastPrice) []*domain.Order`, which collects all newly-triggered stops, deletes them from both btree and byID, and returns them sorted by `seq` for deterministic cascade ordering ([§05 cascade termination](../system_design/05-stop-orders.md#cascade-termination)).

## Acceptance criteria

- [ ] `stops.go` defines `StopBook` containing `buys *btree.BTreeG[*domain.Order]`, `sells *btree.BTreeG[*domain.Order]`, `byID map[string]*domain.Order`
- [ ] btree comparators: buy stops ordered ascending by `TriggerPrice`, ties broken by ascending `seq`. Sell stops ordered descending by `TriggerPrice`, ties broken by ascending `seq`. Tie-breaking by `seq` is required so the btree never returns "equal" for two distinct orders (per [`ARCHITECT_PLAN.md` §4 risk register](../system_design/ARCHITECT_PLAN.md#4-risk-register) on btree `Less` non-determinism)
- [ ] `Insert(o *domain.Order)` adds to the relevant btree and to `byID`. Caller (engine) has already verified the order's `Type ∈ {Stop, StopLimit}` and that the trigger is not already satisfied
- [ ] `Cancel(orderID string) (*domain.Order, bool)` looks up `byID`, removes from both byID and the relevant btree, returns the order (or `nil, false` if absent). Both deletions must happen before returning; document this is the same critical section since the engine mutex serialises
- [ ] `DrainTriggered(lastTradePrice decimal.Decimal) []*domain.Order` returns all stops whose trigger condition is satisfied:
    - buy stop fires when `TriggerPrice <= lastTradePrice`
    - sell stop fires when `TriggerPrice >= lastTradePrice`
    Each triggered stop is removed from both its btree and from `byID` before being returned. Return slice is sorted by ascending `seq` (deterministic cascade order)
- [ ] `Len() int` returns the count of armed stops (sum of both btrees, equal to `len(byID)`). Used by T-010 for the `armedStops` counter sanity-check in the property test ([`ARCHITECT_PLAN.md` §3 invariant 17](../system_design/ARCHITECT_PLAN.md#resource-bounds))
- [ ] `Get(orderID string) (*domain.Order, bool)` — O(1) lookup; engine uses this on `Cancel(orderID)` paths to distinguish "not found" from "found but resting in book"
- [ ] `stops_test.go` covers: insert + Get; cancel removes from both btree and byID; DrainTriggered returns nothing when no trigger met; DrainTriggered returns single matching buy / sell; DrainTriggered returns multiple in `seq` order; cancel-then-drive-price-past-trigger does not fire (the cancelled-stop ghost test in [§05](../system_design/05-stop-orders.md#cancel-of-armed-stop)); empty StopBook returns 0 from `Len`
- [ ] `go vet ./internal/engine/stops/...` and `go test ./internal/engine/stops/...` clean

## Implementation notes

- The `Less` comparators must be **strict total orders** to keep btree behaviour deterministic. For buy stops:
    ```go
    func(a, b *domain.Order) bool {
        if c := a.TriggerPrice.Cmp(b.TriggerPrice); c != 0 { return c < 0 }
        return a.Seq() < b.Seq()
    }
    ```
    For sell stops, flip the price comparison only.
- `DrainTriggered` should peek-pop in a loop using `tree.Min()` (the head of each btree). For buys: while head exists and head.TriggerPrice <= lastTradePrice, delete and append. Same for sells with the inverted condition.
- After collecting from both sides, `sort.SliceStable(triggered, func(i, j int) bool { return triggered[i].Seq() < triggered[j].Seq() })` is required so a same-price-hit on both sides interleaves by placement order ([§05 cascade ordering](../system_design/05-stop-orders.md#trigger-algorithm)).
- Triggers are **inclusive** for both sides (`<=` for buy, `>=` for sell) per [`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md#7-open-decisions-to-lock-down-before-coding-starts).
- Do **not** mutate `o.Type` or `o.Status` here. The engine (T-010) does that after `DrainTriggered` returns.
- Do **not** call `match` recursively from this package. T-010 owns the cascade re-entry.

## Out of scope

- The cascade re-entry loop itself (T-010).
- `lastTradePrice` storage (lives on the engine, T-010).
- Order book interactions (T-008, T-010).
- Trigger-already-satisfied rejection check (T-010 — this package assumes the caller has already filtered).

## Tests required

- `TestStops_InsertAndGet`
- `TestStops_CancelRemovesFromBoth`
- `TestStops_CancelMissingReturnsFalse`
- `TestStops_DrainTriggeredEmpty`
- `TestStops_DrainTriggeredSingleBuy` and `TestStops_DrainTriggeredSingleSell`
- `TestStops_DrainTriggeredMultipleBySeq` — three buys at trigger 100, 100, 100 with `seq=3,1,2`; drive lastPrice to 100; result is `seq=1,2,3`
- `TestStops_DrainTriggeredCrossSide` — one buy at 100 and one sell at 100; both trigger when lastPrice == 100; result ordered by seq
- `TestStops_CancelledDoesNotFire` — insert, cancel, DrainTriggered after price moves past trigger returns empty
- `TestStops_LenMatchesByID` — after a sequence of insert/cancel/drain, `Len() == len(byID)`

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet` and `go test` clean
- [ ] No imports outside stdlib + `internal/domain`, `internal/domain/decimal`, `github.com/google/btree`
