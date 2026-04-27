# T-012 — `app.Service` with idempotency dedup

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.0 h (±25%) |
| Owner | unassigned |
| Parallel batch | B4 |
| Blocks | T-014, T-016 |
| Blocked by | T-010 |
| Touches files | `internal/app/service.go`, `internal/app/service_test.go` |

## Goal

Implement `app.Service`, the thin transport-agnostic layer that sits between HTTP handlers and the engine. Its single responsibility in v1 is **idempotency dedup** keyed by `(user_id, client_order_id)`. Specified in [§08 Idempotency](../system_design/08-http-api.md#idempotency), [§01 What this layout deliberately includes](../system_design/01-architecture.md#what-this-layout-deliberately-includes), and [`ARCHITECT_PLAN.md` §3 invariant 16](../system_design/ARCHITECT_PLAN.md#idempotency).

## Context

The engine knows nothing about `client_order_id`. Idempotency lives here, **outside** the engine. The dedup map and its mutex are local to `app.Service`.

### Required behaviour ([§08 behaviour matrix](../system_design/08-http-api.md#behaviour-matrix))

| Scenario | Behaviour |
|---|---|
| Valid key, never seen | Run engine. Cache the `PlaceResult`. Return it. |
| Valid key, seen, same body | Return cached result byte-identically. Do not re-run engine. |
| Valid key, seen, different body | Return cached result. (`client_order_id` is the source of truth; body-hash mismatch is v2.) |
| Engine returns sentinel error (e.g. `ErrTooManyOrders`, any error) | Do **not** cache. Propagate the error so the caller can retry. |
| Engine returns business-rejected (`Status=Rejected`) | **Cache.** Same retry returns same rejection. |
| Two concurrent requests, same key, key not yet seen | Second blocks; sees cached result. Engine called exactly once. |

### Lock ordering ([§06 Lock ordering](../system_design/06-concurrency-and-determinism.md#lock-ordering--two-mutexes-on-place))

`dedupMu` (this ticket) is acquired **before** `engine.mu` (T-010), and only on the `Place` path. No other path takes `dedupMu`. No path takes both in any other order. This is invariant 13 in [`ARCHITECT_PLAN.md` §3](../system_design/ARCHITECT_PLAN.md#concurrency--determinism) — top-of-file comment in `service.go` documents it explicitly.

The simplest correct implementation is: hold `dedupMu` for the entire `Place` (including the engine call). That way concurrent same-key requests serialise on `dedupMu` and only one reaches the engine. The downside is that distinct-key requests also serialise on `dedupMu` — but since `dedupMu` is held only for a map lookup, an engine call, and a map insert, the extra contention is negligible at v1 traffic.

If profiling later shows `dedupMu` becoming a bottleneck, the upgrade path is a per-key sync (e.g. `sync.Map` plus `singleflight` or a striped lock). Out of scope for v1.

## Acceptance criteria

- [ ] `internal/app/service.go` defines `Service` containing `engine *engine.Engine`, `dedupMu sync.Mutex`, `dedup map[string]engine.PlaceResult`
- [ ] `func NewService(eng *engine.Engine) *Service` constructs with empty map
- [ ] `Place(cmd PlaceCommand) (engine.PlaceResult, error)` where `PlaceCommand` mirrors `engine.PlaceCommand` plus `ClientOrderID string` (the engine struct does not have this field; the service's command does). Implementation: build the engine command from the service command, dedup-lookup, call engine, cache result, return
- [ ] Dedup key construction: `userID + "\x00" + clientOrderID`. Validation upstream guarantees `client_order_id` is ASCII printable (no `\x00`) so the separator is collision-proof ([§08 Idempotency](../system_design/08-http-api.md#how-it-works))
- [ ] Engine errors are **not** cached; business rejections **are** cached
- [ ] `Cancel`, `Snapshot`, `Trades` are pass-throughs to the engine. They take only `engine.mu` (no `dedupMu`)
- [ ] Top-of-file godoc explicitly documents the `dedupMu → engine.mu` lock ordering
- [ ] `service_test.go` covers:
    - dedup hit returns cached result without re-invoking engine (use a fake engine or count engine calls; recommended: a tiny custom engine wrapper that counts calls)
    - dedup miss invokes engine and caches
    - business-rejected order is cached (retry returns same rejection without re-invoking engine)
    - engine error is **not** cached (retry re-invokes the engine — a second engine call is observable)
    - distinct `client_order_id`s with same `user_id` are independent
    - distinct `user_id`s with same `client_order_id` are independent
    - concurrent same-key requests: spawn 100 goroutines submitting the same command; assert the engine was called exactly once
- [ ] `go vet ./internal/app/...` and `go test ./internal/app/... -race` clean

## Implementation notes

- The fake/counting engine for tests is most easily achieved by making `Service` accept an interface, e.g. `type Engine interface { Place(...); Cancel(...); Snapshot(...); Trades(...) }`. But this is overkill — adding an interface just for tests is exactly the kind of thing the architect deliberately avoided (see "no premature seams" in [§01](../system_design/01-architecture.md)). Recommended pragmatic alternative: in tests, construct a real `engine.New(...)` and observe that calls increment trade IDs / order IDs from the `Monotonic` adapter. The test then checks "after the second submit, NextOrderID would be 'o-2' if engine was called twice; assert it's still 'o-2' for engine-was-called-once cases" — verify by placing a follow-up control order and observing the assigned ID.
- An alternative for the "engine called exactly once" assertion: place a real engine, place the duplicated command twice, then run a third unrelated `Place`. Observe the third order's assigned ID. If the dedup worked, the third order is `"o-2"`; if dedup failed, it's `"o-3"`. This is the cleanest test of the contract.
- Hold `dedupMu` for the **entire** Place call including the engine invocation. Two concurrent same-key requests: first acquires, calls engine (engine.mu serialises it), caches, releases. Second acquires, finds the cached entry, returns it without calling engine. Correct and simple.
- Never read `dedup` outside `dedupMu` — concurrent map read/write is undefined behaviour ([§06](../system_design/06-concurrency-and-determinism.md#lock-discipline-rules-in-code-review)).
- Cache key: `key := cmd.UserID + "\x00" + cmd.ClientOrderID`. Use a single `string` rather than a struct key — slightly cheaper and avoids the need to define a key type.
- Cache value: the **whole** `engine.PlaceResult` including the `*Order` and `[]*Trade` slices. The HTTP DTO conversion (T-014) is downstream; this layer returns engine types as-is.
- Note that the cached `*Order` and trades are the **same pointers** as those held by the engine. A retry returns the same pointers. The HTTP layer marshals them (read-only) every retry. As long as nothing in the HTTP layer mutates these objects (it doesn't — DTO conversion is read-only), this is safe.

## Out of scope

- HTTP DTO shape, validation pipeline (T-013, T-014).
- TTL/LRU eviction of the dedup map (deferred to v2 per [§08 What's not in v1](../system_design/08-http-api.md#whats-not-in-v1-deferred-to-11)).
- Body-hash validation for key reuse with different parameters (deferred).
- Per-user rate limiting (deferred).
- Persistence of dedup state across restart (the design is in-memory, restart wipes per [§08](../system_design/08-http-api.md#whats-not-in-v1-deferred-to-11)).

## Tests required

- `TestService_DedupMissCallsEngine`
- `TestService_DedupHitReturnsCached`
- `TestService_BusinessRejectIsCached`
- `TestService_EngineErrorNotCached`
- `TestService_DistinctClientOrderIDIsIndependent`
- `TestService_DistinctUserIDIsIndependent`
- `TestService_ConcurrentSameKeyEngineCalledOnce` (use `sync.WaitGroup`, 100 goroutines)
- `TestService_PassthroughCancelSnapshotTrades` — basic happy-path that the wrappers do nothing more than forward

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `-race -count=10` clean
- [ ] Top-of-file lock-ordering comment present
- [ ] No imports outside stdlib + `internal/engine`, `internal/domain` (decimal not needed at this layer)
