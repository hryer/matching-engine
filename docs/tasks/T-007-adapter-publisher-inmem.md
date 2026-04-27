# T-007 — In-memory ring-buffer publisher

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.75 h (±25%) |
| Owner | unassigned |
| Parallel batch | B2 |
| Blocks | T-016 |
| Blocked by | T-003 |
| Touches files | `internal/adapters/publisher/inmem/inmem.go`, `internal/adapters/publisher/inmem/inmem_test.go` |

## Goal

Implement `ports.EventPublisher` as a bounded ring buffer holding the last 10,000 trades. Specified in [§02 Trade history](../system_design/02-data-structures.md#trade-history) and [§01](../system_design/01-architecture.md#what-this-layout-deliberately-includes).

## Context

The engine emits trades via `EventPublisher.Publish`. The in-memory adapter retains the last N trades (default **10,000**); older trades are silently dropped on overflow. `GET /trades?limit=N` (T-014) is served from this buffer via `Recent(limit int)`.

Like the ID generator, this adapter is mutated only inside the engine mutex during `Publish`. `Recent` is called from `engine.Trades()` (also under the engine mutex). So no internal synchronisation is needed.

## Acceptance criteria

- [ ] `internal/adapters/publisher/inmem/inmem.go` defines `type Ring struct{ buf []*domain.Trade; next, count, cap int }` (or equivalent ring layout)
- [ ] `func NewRing(capacity int) *Ring` constructs with the given capacity. Panic if `capacity <= 0`
- [ ] `Publish(trade *domain.Trade)` appends; on overflow, oldest trade is overwritten in O(1)
- [ ] `Recent(limit int) []*domain.Trade` returns up to `limit` trades, **newest first**, in chronological order (most recent at index 0)
- [ ] If `limit <= 0` or `limit > count`, clamp: `limit = min(limit, count)` for `limit > 0`; behaviour for `limit <= 0` is "return empty slice." Document the chosen behaviour in godoc
- [ ] Compile-time check: `var _ ports.EventPublisher = (*Ring)(nil)`
- [ ] `inmem_test.go` covers: empty ring returns empty slice; under-capacity insert preserves all; overflow drops oldest; `Recent` ordering is newest-first; `Recent(N)` where N > count returns all
- [ ] `go vet ./internal/adapters/publisher/inmem/...` clean, `go test ./internal/adapters/publisher/inmem/...` green

## Implementation notes

- Use a fixed-size slice plus head/tail indices. Avoid `append` in the hot path (`Publish` should be O(1)). Allocate `buf = make([]*domain.Trade, capacity)` once at construction.
- "Newest first" means the trade most recently `Publish`ed appears at `Recent()[0]`. The engine inserts in order; `Recent` reverses the logical order on read.
- The default capacity (`10_000`) lives at the call site (T-016), not here. The constructor takes whatever capacity the composition root passes.
- This adapter is **not** safe for concurrent use. Document and rely on the engine mutex. A future Kafka/WS adapter is concurrent-safe by construction.
- Do not add a `Subscribe` channel or pub-sub fan-out; that's a v2 concern ([§11](../system_design/11-production-evolution.md)).

## Out of scope

- WebSocket fan-out, Kafka publisher, or any external transport (deferred per [§11](../system_design/11-production-evolution.md)).
- Persistence to disk.
- Per-pair routing (single-pair v1).
- `Cancel` of trades (trades are immutable once published).

## Tests required

- `TestRing_EmptyReturnsNoTrades`
- `TestRing_UnderCapacityPreservesOrder`
- `TestRing_OverflowDropsOldest` — capacity 3, publish 5, assert only the last 3 remain
- `TestRing_RecentNewestFirst` — publish A, B, C; `Recent(3)` returns `[C, B, A]`
- `TestRing_LimitClamping` — publish 2, `Recent(10)` returns 2; `Recent(-1)` or `Recent(0)` returns empty
- `TestRing_PanicOnZeroCapacity` — `NewRing(0)` panics

## Definition of done

- [ ] All acceptance criteria checked
- [ ] No imports outside stdlib + `matching-engine/internal/domain` + `matching-engine/internal/ports`
- [ ] `Publish` is O(1) — no `append` reallocation in steady state
