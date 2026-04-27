# T-010 — Engine core (struct, match, cascade, errors)

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B3 |
| Blocks | T-011, T-012, T-016 |
| Blocked by | T-001, T-002, T-003, T-004, T-008, T-009 |
| Touches files | `internal/engine/engine.go`, `internal/engine/match.go`, `internal/engine/errors.go`, `internal/engine/engine_test.go` |

## Goal

Implement the matching engine: the `Engine` struct, the four public methods (`Place`, `Cancel`, `Snapshot`, `Trades`), the `match` function, the cascade primitive `drainTriggeredStops`, and the engine-level sentinel errors. Specified in [§04 Matching Algorithm](../system_design/04-matching-algorithm.md), [§05 Stop Orders](../system_design/05-stop-orders.md), [§06 Concurrency & Determinism](../system_design/06-concurrency-and-determinism.md), and the resource-bounds rows of [§04, §05] plus [`ARCHITECT_PLAN.md` §3 invariants 12–17](../system_design/ARCHITECT_PLAN.md#concurrency--determinism).

## Context

This is the heart of the system. Everything above depends on the engine's public API; everything below is what the engine drives.

### Engine struct shape

```
type Engine struct {
    mu sync.Mutex

    book           *book.OrderBook
    stops          *stops.StopBook
    byID           map[string]*domain.Order // resting orders only — armed stops live in stops.byID
    lastTradePrice decimal.Decimal

    history ports.EventPublisher
    clock   ports.Clock
    ids     ports.IDGenerator

    seqCounter uint64

    // Resource-bound counters and caps (invariant 17)
    openOrders     int
    armedStops     int
    maxOpenOrders  int
    maxArmedStops  int
}

type Deps struct {
    Clock        ports.Clock
    IDs          ports.IDGenerator
    Publisher    ports.EventPublisher
    MaxOpenOrders int // 1_000_000 in production per [`ARCHITECT_PLAN.md` §7]
    MaxArmedStops int // 100_000 in production
}

func New(deps Deps) *Engine
```

### Public API (all under `e.mu`)

```
PlaceCommand: { UserID, Side, Type, Price, TriggerPrice, Quantity }
PlaceResult:  { Order *domain.Order, Trades []*domain.Trade }

func (e *Engine) Place(cmd PlaceCommand) (PlaceResult, error)
func (e *Engine) Cancel(orderID string) (*domain.Order, error)
func (e *Engine) Snapshot(depth int) (bids, asks []book.LevelSnapshot)
func (e *Engine) Trades(limit int) []*domain.Trade
```

### Match function ([§04](../system_design/04-matching-algorithm.md))

`match(incoming *domain.Order) []*domain.Trade` is a private method on `Engine` (or a free function with `*Engine` receiver argument — pick the receiver style for consistency). It:

1. Loops while `incoming.RemainingQuantity > 0`
2. Peeks the opposite side's `BestLevel`
3. Applies the limit-price gate (Market skips it)
4. Applies cancel-newest STP if `maker.UserID == incoming.UserID`
5. Computes `fillQty = min(taker.rem, maker.rem)`
6. Constructs trade with `Price = maker.Price`, decrements both sides, removes maker if filled
7. Calls `appendTrade` (which calls `Publisher.Publish` + `updateLastTradePrice` → `drainTriggeredStops`)
8. Sets terminal status (Filled / Resting / PartiallyFilled / Rejected) and rests if applicable

### Cascade ([§05](../system_design/05-stop-orders.md#trigger-algorithm))

`drainTriggeredStops` is called from `updateLastTradePrice`. Each triggered stop has its `Type` rewritten (`Stop → Market`, `StopLimit → Limit`), is removed from `armedStops` counter, and is re-submitted through `match`. Each newly-resting limit (from a stop-limit that didn't immediately match) increments `openOrders`.

### Resource bounds ([§04, §05] + [`ARCHITECT_PLAN.md` §3 invariant 17, §7](../system_design/ARCHITECT_PLAN.md))

- `openOrders` counter: incremented on every successful rest (insert into book), decremented on cancel and on full-fill of a maker. After every public method returns, `openOrders == count(orders resting in book)`.
- `armedStops` counter: incremented on `Place` of a Stop / StopLimit that arms, decremented on cancel of an armed stop and on cascade trigger (because the order leaves the stop book and may then rest, which increments `openOrders` separately).
- Cap check: at the top of `Place`, after determining the order will rest or arm, compare `openOrders+1 > maxOpenOrders` (or armed equivalent) and return `ErrTooManyOrders` / `ErrTooManyStops` before any state mutation. Cap errors must **not** be cached by the dedup layer (T-012).

### Sentinel errors

```
var (
    ErrTooManyOrders   = errors.New("engine: too many open orders")
    ErrTooManyStops    = errors.New("engine: too many armed stops")
    ErrOrderNotFound   = errors.New("engine: order not found")
    ErrAlreadyTerminal = errors.New("engine: order already terminal")
)
```

The HTTP layer (T-014) maps these to status codes (429 for caps, 404 for not-found, 409 for already-terminal).

### Concurrency contract ([§06](../system_design/06-concurrency-and-determinism.md))

- All four public methods take `e.mu` at entry and release at exit (`defer e.mu.Unlock()`).
- No public method calls another public method (no re-entry).
- Internal helpers (`match`, `drainTriggeredStops`, `updateLastTradePrice`, `appendTrade`) assume the lock is held.
- The engine spawns no goroutines.
- Every mutation of book / stops / history / lastTradePrice / counters happens under the mutex.
- A top-of-file comment block in `engine.go` documents this contract.

## Acceptance criteria

- [ ] `engine.go` defines `Engine`, `Deps`, `PlaceCommand`, `PlaceResult` and the four public methods
- [ ] `match.go` defines the private `match` method following the pseudocode in [§04](../system_design/04-matching-algorithm.md#pseudocode)
- [ ] `errors.go` defines the four sentinel errors above
- [ ] `Place` for `Limit`, `Market`, `Stop`, `StopLimit` follows the status transition table in [§04](../system_design/04-matching-algorithm.md#status-transition-table)
- [ ] Trigger-already-satisfied stops are **rejected** with `Status=Rejected`, no trades, not stored ([§05](../system_design/05-stop-orders.md#trigger-already-satisfied-at-placement)). The dedup layer caches this rejection (so retry returns same body)
- [ ] Stop cascade fires by `seq` (deterministic) and terminates ([§05 cascade termination](../system_design/05-stop-orders.md#cascade-termination))
- [ ] `Cancel` works for both resting orders (in book) and armed stops (in stops). Returns `ErrOrderNotFound` for unknown IDs, `ErrAlreadyTerminal` for already-terminal orders
- [ ] `Snapshot(depth)` consults only the order book, not the stop book ([§05](../system_design/05-stop-orders.md), invariant 8)
- [ ] `Trades(limit)` reads from `Publisher.Recent(limit)`, clamping `limit` at 1000 (per [`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md#7-open-decisions-to-lock-down-before-coding-starts))
- [ ] `openOrders` and `armedStops` counters maintained per the rules in Context above. Cap errors returned **before** any mutation
- [ ] `seqCounter` is incremented on every successful `Place` (for both resting and armed orders); used for FIFO tie-break and cascade ordering
- [ ] The composition root passes `Deps{ MaxOpenOrders: 1_000_000, MaxArmedStops: 100_000, ... }` (T-016 verifies this; this ticket only ensures fields exist on `Deps`)
- [ ] `engine_test.go` covers the layer-1 cases listed in [§09](../system_design/09-testing.md#layer-1--table-driven-matching-unit-tests):
    - empty book + market → Rejected
    - empty book + limit → Resting
    - limit crosses one level → single trade, Filled
    - limit crosses multiple levels → multi-trade, Filled
    - limit partially crosses → trades + remainder rests as PartiallyFilled
    - market eats book partially → PartiallyFilled, remainder dropped
    - FIFO within level
    - self-match cancel-newest
    - self-match in middle (one external maker filled, then own resting → Cancelled)
    - cancel resting → Cancelled, removed from book
    - cancel non-existent → ErrOrderNotFound
    - cancel already-cancelled → ErrAlreadyTerminal
    - stop buy with trigger > lastTradePrice → Armed
    - stop buy with trigger ≤ lastTradePrice at placement → Rejected
    - stop fires when last trade reaches trigger
    - stop cascade with two stops, both fire by seq
    - stop_limit fires → becomes Limit, rests if no immediate match
    - cap-hit: `MaxOpenOrders=2`, place 3rd → ErrTooManyOrders, no state mutation (counter and book size unchanged)
    - cap-hit: `MaxArmedStops=1`, place 2nd armed stop → ErrTooManyStops
    - counter consistency: after place + cancel cycles, `openOrders == count(byID)` and `armedStops == stops.Len()`
- [ ] All public methods locked at entry. Top-of-file comment in `engine.go` documents the lock contract
- [ ] `go vet ./internal/engine/...` and `go test ./internal/engine/...` clean

## Implementation notes

- `match` returns `[]*domain.Trade`; the caller (`Place`) sets terminal status on `incoming` and returns the slice plus the `*Order` in `PlaceResult`. Trade emission to the publisher happens inside the loop, not at the end (so each trade can drive `updateLastTradePrice → drainTriggeredStops` before the next iteration).
- `updateLastTradePrice` mutates `e.lastTradePrice` and immediately calls `drainTriggeredStops`. The latter pops triggered stops, mutates their Type, and calls `match` recursively. Recursion terminates because each stop is removed from `stops.byID` before re-entry.
- `appendTrade(t *domain.Trade)` is a tiny private helper that calls `e.history.Publish(t)` and `e.updateLastTradePrice(t.Price)`. Use it instead of repeating those two calls.
- For the cascade: collect triggered stops into a slice, sort by `seq`, then iterate and submit each to `match`. Already implemented as `stops.DrainTriggered` returning sorted slice (T-009) — engine just iterates.
- Self-match cancel-newest: the maker is **untouched**. The taker's terminal status is `Cancelled`, regardless of whether prior trades were produced against other makers. Whatever trades were produced before the self-match is hit are kept. ([§04](../system_design/04-matching-algorithm.md#self-match-policy-cancel-newest))
- Decimal comparisons: never `==` or `!=`. Use `.Cmp()`, `.LessThan`, `.GreaterThan`, `.IsZero()` ([§07](../system_design/07-decimal-arithmetic.md#common-pitfalls-in-matching-code)).
- The `byID` map (engine-level) holds **resting orders only**. When an order rests, insert into both `book` and `byID`. When it cancels or fully fills, remove from both. When a stop arms, it goes to `stops.byID` (managed by T-009), not engine `byID`. When a stop triggers and the resulting limit rests, it transitions from `stops.byID` (deleted by `DrainTriggered`) to engine `byID` + book (engine inserts after match returns).
- Cap-check ordering on `Place`:
    1. Compute the candidate target (rest, arm, or terminal-without-resting).
    2. If would-rest: check `openOrders+1 > maxOpenOrders`, return `ErrTooManyOrders` if so.
    3. If would-arm: check `armedStops+1 > maxArmedStops`, return `ErrTooManyStops` if so.
    4. **Only after** caps pass, mutate state.
    For limit/market that immediately fills without resting, no cap is consumed. For limit that partially fills then rests, cap is consumed when the remainder rests. The simplest correct ordering is: run match first (no rest yet), then check cap before the final `book.Insert`. Caveat: trades produced before the cap-hit have already been appended to history. Document this — the cap is enforced per-state, not per-call. (Open question if a reviewer pushes back: "should we hold trades pending and roll back if rest fails?" Decision per [`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md): no rollback; the cap is a back-pressure signal, the trades are real, and the rejected order's status reflects the partial outcome.)
    
    **OPEN QUESTION** for cap on partial-fill-that-would-rest: do we (a) reject the entire `Place` with `ErrTooManyOrders` after the partial fill, leaving the partial trades in history; or (b) accept the partial fill but not rest the remainder, setting status to `PartiallyFilled` and dropping the rest? Design docs do not explicitly settle this. Recommended decision: (b) — partial fills always succeed; only the rest step is gated by the cap; the order's terminal status reflects the truncation. Confirm with architect before implementing.
- For the cascade-fire counter accounting: when a stop triggers via `DrainTriggered`, decrement `armedStops` by the number returned. If the resulting limit rests, increment `openOrders`. If it fully fills, no `openOrders` change. If it's a triggered Stop (becomes Market), it never rests, so no `openOrders` change.

## Out of scope

- HTTP DTOs, validation, idempotency dedup (T-012, T-013, T-014).
- `cmd/server` composition (T-016).
- Property tests, replay tests (T-011 — explicitly a separate ticket so this one stays focused).
- Subscribe / fan-out on the publisher (engine just calls `Publish`).

## Tests required

See acceptance criteria — every bullet in the engine_test.go list is required. Use `engine_test.go` as the single test file; split into multiple files (`engine_match_test.go`, `engine_cancel_test.go`, etc.) only if the file passes ~600 lines.

Use a `Fake` clock and `Monotonic` IDs from the adapter packages so tests are deterministic. The publisher can be a real `*inmem.Ring` with capacity 100.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/engine/...` clean
- [ ] `go test ./internal/engine/... -race` green
- [ ] Top-of-file lock-discipline comment in `engine.go`
- [ ] No imports outside stdlib + `internal/domain`, `internal/domain/decimal`, `internal/ports`, `internal/engine/book`, `internal/engine/stops`
- [ ] OPEN QUESTION on partial-fill-then-cap-hit resolved (one of the two options confirmed by architect)
