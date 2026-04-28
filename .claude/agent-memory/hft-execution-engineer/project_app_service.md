---
name: App service — T-012 implementation notes
description: Idempotency dedup design, lock ordering, ID arithmetic in tests, and key edge cases for internal/app/service.go
type: project
---

T-012 ships `internal/app/service.go` and `service_test.go`.

## Lock ordering
`dedupMu` → `engine.mu`. Only `Place` takes both. `Cancel`/`Snapshot`/`Trades` take only `engine.mu`. Deadlock is structurally impossible.

## Caching rule
- Business rejections (`Status=Rejected`, nil error) ARE cached.
- Sentinel errors (`ErrTooManyOrders`, `ErrTooManyStops`) are NOT cached.
- The entire `Place` call (including the engine call) runs under `dedupMu`, so concurrent same-key requests serialise here and only one reaches the engine.

## ID arithmetic trap in tests
`engine.Place` calls `e.ids.NextOrderID()` unconditionally **before** the cap check. A cap-hit call consumes an order ID even though `PlaceResult{}` is returned. When constructing ID-based "control order" assertions, account for IDs consumed by error-path engine calls. Example from `TestService_EngineErrorNotCached`:
- Place A → o-1 (rests)
- Place B (cap-hit) → o-2 consumed, error returned
- Cancel A
- Retry B (not cached, engine called again) → o-3 (rests)
The ID skip from o-2 to o-3 is the observable proof that the retry reached the engine.

## Business rejection path (simplest test setup)
Fresh engine has `lastTradePrice == decimal.Zero`. Any sell stop with a positive trigger satisfies the already-satisfied rule (`trigger >= 0`), returning `(PlaceResult{Order: rejected}, nil)`. This is the cheapest path to a cacheable business rejection — no trade setup required.

## Pass-through signatures
`Cancel` returns `(*domain.Order, error)` matching `engine.Cancel`.
`Snapshot` returns `(bids, asks []book.LevelSnapshot)` matching `engine.Snapshot`.
`Trades` returns `[]*domain.Trade` matching `engine.Trades`.

**Why:** and **How to apply:**
**Why:** Keeping signatures identical avoids any wrapper allocation on the pass-through path and makes T-014 (HTTP DTO layer) directly callable on engine types.
**How to apply:** If T-014 needs a different shape, add the mapping there, not here.
