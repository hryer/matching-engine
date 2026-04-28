# T-010 — Engine Core: Tech Lead Plan

> Authoritative plan for Batch B3. Implementer A and Implementer B work in parallel against this document. QA (Implementer C) writes `engine_test.go` against the public + internal API specified here.
>
> Source spec: `docs/tasks/T-010-engine-core.md` plus `docs/system_design/04-matching-algorithm.md`, `05-stop-orders.md`, `06-concurrency-and-determinism.md`, `07-decimal-arithmetic.md`. The B2 outputs are the ground truth for downstream API shape — every signature below has been reconciled against the real code on disk in `internal/engine/book/`, `internal/engine/stops/`, `internal/ports/`, `internal/domain/`, `internal/adapters/{clock,ids,publisher/inmem}/`.

---

## 1. Resolution of the OPEN QUESTION (partial-fill-then-cap-hit)

**Question.** When `Place(limit)` partially fills and the remainder would rest, but `openOrders+1 > maxOpenOrders`, do we (a) reject the entire `Place` after the partial fill, or (b) accept the partial fill and drop the remainder, returning `Status=PartiallyFilled`?

**Decision: (b). Confirmed and pinned.**

### Why (b) over (a)

1. **Trades are real and already published.** `match` calls `appendTrade` per fill, which calls `e.history.Publish(...)` and `updateLastTradePrice(...)`, which can cascade-trigger stops that themselves produce more trades. By the time the cap-check fires, history is mutated and counterparties (other users' makers) have had their resting state mutated. There is no "rollback" available without making the publisher and the maker mutations transactional, which the design doc explicitly disclaims (`docs/tasks/T-010-engine-core.md` Implementation notes: "no rollback; the cap is a back-pressure signal, the trades are real").
2. **Symmetry with Market.** A `Market` order that exhausts liquidity and cannot rest already terminates as `PartiallyFilled` with the remainder dropped (`§04` status table row 6). Option (b) makes a capped `Limit` behave identically: trades land, terminal status reflects truncation, no resting state. (a) would invent a third behaviour ("trades happened but the order is `Rejected`") that has no precedent in the status machine.
3. **Cap is a back-pressure signal, not a validation error.** `ErrTooManyOrders` exists to protect engine resource bounds. Once a partial fill has occurred, the resource pressure has already been *reduced* (a maker may have been fully consumed and removed). Failing the call after producing fills is the worst of both worlds: the caller gets an error, yet trades were emitted in their name.
4. **HTTP semantics.** T-014 will map `ErrTooManyOrders` to `429`. Returning `429` after producing trades is misleading: the client thinks no work was done, but stops cascaded and trade history grew. Option (b) returns `201` with `Status=PartiallyFilled` and `len(trades) > 0`, which is honest about what the engine did.

### Concrete code shape implementing (b)

In `match.go`:

```go
// match runs the price-time loop. It NEVER calls book.Insert. The caller
// (Place) is responsible for the rest step, gated on the openOrders cap.
// Pre: e.mu held. Returns trades produced. Mutates incoming.RemainingQuantity
// and incoming.Status (terminal status set here for all paths EXCEPT the
// "limit, 0 < remaining < original, would-rest" case, which is left to Place
// because the cap-check decides between PartiallyFilled-and-rest and
// PartiallyFilled-truncated).
```

In `engine.go` `Place` for `Limit`:

```go
trades := e.match(incoming)

switch {
case incoming.Status == domain.StatusCancelled,    // STP
     incoming.Status == domain.StatusFilled,
     incoming.Status == domain.StatusRejected:
    // match set the terminal status; nothing to rest.
case incoming.Type == domain.Limit && len(trades) == 0:
    // Would rest at original size. Cap check first.
    if e.openOrders+1 > e.maxOpenOrders {
        return PlaceResult{}, ErrTooManyOrders     // pre-mutation: no trades, no insert
    }
    incoming.Status = domain.StatusResting
    e.book.Insert(incoming)
    e.byID[incoming.ID] = incoming
    e.openOrders++
case incoming.Type == domain.Limit:
    // Partial fill. Try to rest the remainder; if cap-hit, truncate.
    if e.openOrders+1 > e.maxOpenOrders {
        incoming.Status = domain.StatusPartiallyFilled  // truncated, NOT rested
        // trades already published; do not return error
    } else {
        incoming.Status = domain.StatusPartiallyFilled
        e.book.Insert(incoming)
        e.byID[incoming.ID] = incoming
        e.openOrders++
    }
}
```

The "no error" branch on partial-fill-cap-hit is the **whole** content of decision (b). Mark it with a comment block citing `§04 status transition table` and this plan.

### Side note: cap-hit on a never-traded `Limit`

The same cap-check guards the zero-trade case (`len(trades) == 0`). In that path, returning `ErrTooManyOrders` with no trades and no state mutation is unambiguous (this is the case T-010 acceptance test "cap-hit: `MaxOpenOrders=2`, place 3rd → ErrTooManyOrders, no state mutation" verifies). No question to resolve there.

---

## 2. File split between Implementer A and Implementer B

| File | Owner | Notes |
|---|---|---|
| `internal/engine/engine.go` | **A** | Engine struct, Deps, PlaceCommand, PlaceResult, New, Place, Cancel, Snapshot, Trades, counter accounting, cap checks, top-of-file lock-discipline comment. |
| `internal/engine/errors.go` | **A** | The four sentinel errors (`ErrTooManyOrders`, `ErrTooManyStops`, `ErrOrderNotFound`, `ErrAlreadyTerminal`). |
| `internal/engine/match.go` | **B** | `match`, STP detection, trade construction, `appendTrade`, `updateLastTradePrice`, `drainTriggeredStops`. |
| `internal/engine/engine_test.go` | **C (QA)** | All test cases in §9 of this plan. A and B do **not** write tests in this ticket. |

### Why this split

A owns the public surface and lifecycle bookkeeping. B owns the matching kernel and the cascade. The split aligns with how the spec already decomposes the work: `§04` pseudocode and `§05` cascade live entirely in `match.go`; the public contract, locking, and resource-bound semantics live entirely in `engine.go`. The contact surface is small (six calls A → B, zero calls B → A that aren't through the contract listed below).

Both engineers must read `§3` (Internal API contract) and `§4` (Counter accounting) before writing code. Whoever implements `book.Insert` calls (A in `Place`, B in `match` cascade re-entry — see §3) **must** maintain the `e.byID` map and the `openOrders` counter consistently with the rules in §4.

### A → B contact surface (what A consumes from B)

A calls these symbols defined in `match.go`:

- `func (e *Engine) match(incoming *domain.Order) []*domain.Trade`

That is the **only** symbol A calls from B. `appendTrade`, `updateLastTradePrice`, `drainTriggeredStops` are internal to `match.go` and not called from `engine.go`.

### B → A contact surface (what B consumes from A)

B reads/writes these fields on `*Engine` (defined by A in `engine.go`):

- `e.book *book.OrderBook`
- `e.stops *stops.StopBook`
- `e.byID map[string]*domain.Order`
- `e.lastTradePrice decimal.Decimal`
- `e.history ports.EventPublisher`
- `e.clock ports.Clock`
- `e.ids ports.IDGenerator`
- `e.openOrders int`
- `e.armedStops int`

B calls these methods/functions defined elsewhere (already in B2 code, NOT defined by A):

- `e.book.BestLevel(side domain.Side) *book.PriceLevel`
- `e.book.RemoveFilledMaker(level *book.PriceLevel, o *domain.Order)`
- `e.book.Insert(o *domain.Order)` — used from cascade path when a triggered stop-limit fully fails to match and rests
- `e.stops.DrainTriggered(lastTradePrice decimal.Decimal) []*domain.Order`
- `e.history.Publish(t *domain.Trade)`
- `e.clock.Now() time.Time`
- `e.ids.NextTradeID() string`

**Critical convention.** When B's cascade re-enters `match` for a triggered stop-limit that ends up resting (its `Type` was rewritten to `Limit` and `len(trades) == 0`), B's code must:

1. Insert into the book: `e.book.Insert(triggered)`
2. Add to the engine ID map: `e.byID[triggered.ID] = triggered`
3. Increment the counter: `e.openOrders++`
4. Set status: `triggered.Status = domain.StatusResting`

This is the **only** place inside `match.go` where B touches `e.byID` and `e.openOrders`. See §4 for full counter rules.

The cascade path crucially does **not** consult `e.maxOpenOrders` (cap-check is a `Place`-time concern; cap is a back-pressure signal at user-call boundary, not at internal cascade boundary). A cascade overshoot is acceptable because the original `Place` already paid the cap budget — see §4 row "cascade-trigger-stoplimit-becomes-limit-rests".

### Symbol ownership table (no overlap)

| Symbol | File | Owner |
|---|---|---|
| `Engine` (struct) | `engine.go` | A |
| `Deps` (struct) | `engine.go` | A |
| `PlaceCommand` (struct) | `engine.go` | A |
| `PlaceResult` (struct) | `engine.go` | A |
| `New` | `engine.go` | A |
| `Place` | `engine.go` | A |
| `Cancel` | `engine.go` | A |
| `Snapshot` | `engine.go` | A |
| `Trades` | `engine.go` | A |
| `(e *Engine).match` | `match.go` | B |
| `(e *Engine).appendTrade` | `match.go` | B |
| `(e *Engine).updateLastTradePrice` | `match.go` | B |
| `(e *Engine).drainTriggeredStops` | `match.go` | B |
| `ErrTooManyOrders` | `errors.go` | A |
| `ErrTooManyStops` | `errors.go` | A |
| `ErrOrderNotFound` | `errors.go` | A |
| `ErrAlreadyTerminal` | `errors.go` | A |

---

## 3. Internal API contract (concrete Go signatures)

### Package and imports (both files)

```go
package engine

// engine.go imports:
//   "errors"
//   "sync"
//   "matching-engine/internal/domain"
//   "matching-engine/internal/domain/decimal"
//   "matching-engine/internal/engine/book"
//   "matching-engine/internal/engine/stops"
//   "matching-engine/internal/ports"

// match.go imports:
//   "matching-engine/internal/domain"
//   "matching-engine/internal/domain/decimal"
//   "matching-engine/internal/engine/book"  // for *book.PriceLevel type cast on o.Level()

// errors.go imports:
//   "errors"
```

### Engine struct (A defines, B reads)

```go
type Engine struct {
    mu sync.Mutex

    book           *book.OrderBook
    stops          *stops.StopBook
    byID           map[string]*domain.Order // resting orders only — armed stops live in stops (StopBook owns its own byID)
    lastTradePrice decimal.Decimal

    history ports.EventPublisher
    clock   ports.Clock
    ids     ports.IDGenerator

    seqCounter uint64

    openOrders    int
    armedStops    int
    maxOpenOrders int
    maxArmedStops int
}
```

### Deps struct (A defines)

```go
type Deps struct {
    Clock         ports.Clock
    IDs           ports.IDGenerator
    Publisher     ports.EventPublisher
    MaxOpenOrders int
    MaxArmedStops int
}
```

`New` zero-values are **not** sentinel-replaced. T-016 is responsible for passing the production limits (`1_000_000` / `100_000`). If a caller passes `0` for `MaxOpenOrders`, the cap check `openOrders+1 > 0` is always true and `Place` rejects every limit/stop. Tests exercise tiny caps (e.g. `MaxOpenOrders=2`) — no defaulting.

### PlaceCommand / PlaceResult (A defines)

```go
type PlaceCommand struct {
    UserID       string
    Side         domain.Side
    Type         domain.Type
    Price        decimal.Decimal // zero for Market / Stop
    TriggerPrice decimal.Decimal // zero for Limit / Market
    Quantity     decimal.Decimal
}

type PlaceResult struct {
    Order  *domain.Order
    Trades []*domain.Trade
}
```

The HTTP layer (T-013/T-014) is responsible for input validation. The engine assumes valid inputs (positive quantities, type/price coherence). No engine-level validation of business inputs.

### New (A defines)

```go
func New(deps Deps) *Engine {
    return &Engine{
        book:           book.New(),
        stops:          stops.New(),
        byID:           make(map[string]*domain.Order),
        lastTradePrice: decimal.Zero,
        history:        deps.Publisher,
        clock:          deps.Clock,
        ids:            deps.IDs,
        maxOpenOrders:  deps.MaxOpenOrders,
        maxArmedStops:  deps.MaxArmedStops,
    }
}
```

### Public methods (A defines)

```go
func (e *Engine) Place(cmd PlaceCommand) (PlaceResult, error)
func (e *Engine) Cancel(orderID string) (*domain.Order, error)
func (e *Engine) Snapshot(depth int) (bids, asks []book.LevelSnapshot)
func (e *Engine) Trades(limit int) []*domain.Trade
```

Notes on each:

- `Place`: locks `e.mu`, increments `seqCounter`, allocates ID via `e.ids.NextOrderID()`, sets `CreatedAt = e.clock.Now()`, dispatches by `Type` (see §7).
- `Cancel`: locks `e.mu`. Look up `e.byID[orderID]` first, then `e.stops.Get(orderID)`. If neither: `ErrOrderNotFound`. If found-but-already-terminal (`Status ∈ {Filled, Cancelled, Rejected}`): `ErrAlreadyTerminal` — note that resting orders by construction are not in a terminal state, so this branch fires only if a maker was filled but somehow lingers in `byID` (this should be impossible by invariant, but defensive). For armed stops, status is `StatusArmed` so the check passes. On success: remove from `e.byID` + `e.book.Cancel(o)` and decrement `e.openOrders`, OR remove from stop book via `e.stops.Cancel(orderID)` and decrement `e.armedStops`. Set `o.Status = StatusCancelled`.
- `Snapshot`: locks `e.mu`, calls `e.book.Snapshot(depth)`. Stops are NOT included.
- `Trades`: locks `e.mu`. Clamp `limit` to `[0, 1000]`. Call `e.history.Recent(limit)`.

### Internal helpers (B defines, called only with `e.mu` held)

```go
// match runs the price-time loop on incoming. Returns trades produced.
// Mutates incoming.RemainingQuantity. Sets incoming.Status terminally
// for: Filled, Cancelled (STP), Rejected (Market with no trades),
// Market PartiallyFilled. Does NOT set status for Limit cases — the
// caller (Place) decides between Resting / PartiallyFilled / cap-hit
// based on (len(trades), remaining, e.openOrders+1 > e.maxOpenOrders).
//
// match performs maker mutations (RemoveFilledMaker, status updates,
// removal from e.byID, openOrders-- on full-fill) inline. It calls
// appendTrade per produced trade, which drives the cascade.
//
// Pre: e.mu held. No allocations except the trades slice.
func (e *Engine) match(incoming *domain.Order) []*domain.Trade

// appendTrade publishes the trade and drives the stop cascade.
// Pre: e.mu held.
func (e *Engine) appendTrade(t *domain.Trade)

// updateLastTradePrice sets e.lastTradePrice = p and immediately
// drains any newly-triggered stops.
// Pre: e.mu held.
func (e *Engine) updateLastTradePrice(p decimal.Decimal)

// drainTriggeredStops pops every stop whose trigger condition is now
// satisfied (via stops.DrainTriggered, which already sorts by seq),
// rewrites Type (Stop -> Market, StopLimit -> Limit), and re-enters
// match for each. For each triggered order:
//   - decrement e.armedStops (one per drained order)
//   - call match
//   - if the resulting order's terminal status is StatusResting (a
//     stop-limit that didn't immediately fill), insert into e.book +
//     e.byID and increment e.openOrders. Note: drainTriggeredStops sets
//     this status itself when len(trades)==0 and Type==Limit, because
//     match leaves Limit terminal-status decision to the caller (per
//     contract above).
//   - if PartiallyFilled (stop-limit that partial-filled), insert into
//     e.book + e.byID and increment e.openOrders.
//   - if Filled, Rejected (Market with no liquidity), Cancelled (STP):
//     no further action.
// Pre: e.mu held.
func (e *Engine) drainTriggeredStops()
```

**Key contract subtlety on `match` terminal status.** `match` is called from two places: `Place` and `drainTriggeredStops`. In both cases the caller is the one that decides whether to rest a Limit (because cap-check applies in Place; cascade-rest is unconditional). To keep `match` callable from both, **`match` does NOT set terminal status for the Limit case** (it only sets `Filled` if `RemainingQuantity.IsZero()`, `Cancelled` for STP, `Rejected` for Market-no-trades, `PartiallyFilled` for Market-partial). The caller inspects `(incoming.Status, len(trades), incoming.RemainingQuantity)` and decides Resting vs PartiallyFilled vs cap-rejected.

This is a deliberate **deviation** from the §04 pseudocode, which puts the rest step inside `match`. The deviation is necessary because the cap-check requires the caller's policy decision (a `Place`-time concern that doesn't apply to cascade re-entry). Document this with a comment block at the top of `match.go` and reference this plan section.

### B2 method signatures (verified against the real code)

Quoted from disk so implementers don't guess:

```go
// internal/engine/book/book.go
func New() *OrderBook
func (b *OrderBook) Insert(o *domain.Order)
func (b *OrderBook) Cancel(o *domain.Order)
func (b *OrderBook) RemoveFilledMaker(level *PriceLevel, o *domain.Order)
func (b *OrderBook) BestLevel(s domain.Side) *PriceLevel
func (b *OrderBook) Snapshot(depth int) (bids, asks []LevelSnapshot)

// internal/engine/book/level.go
type PriceLevel struct {
    Price  decimal.Decimal
    Orders *list.List
    Total  decimal.Decimal
}
type LevelSnapshot struct {
    Price    decimal.Decimal
    Quantity decimal.Decimal
}

// internal/engine/stops/stops.go
func New() *StopBook
func (s *StopBook) Insert(o *domain.Order)
func (s *StopBook) Cancel(orderID string) (*domain.Order, bool)
func (s *StopBook) Get(orderID string) (*domain.Order, bool)
func (s *StopBook) Len() int
func (s *StopBook) DrainTriggered(lastTradePrice decimal.Decimal) []*domain.Order
```

Notes for the implementer:

- `book.RemoveFilledMaker` does **not** decrement `level.Total` — the matcher does that inline (`bestLevel.Total = bestLevel.Total.Sub(fillQty)`). Read the comment in `book.go` carefully before invoking.
- `book.Cancel` **does** decrement `level.Total`. Use it from the public `Engine.Cancel` path (resting cancel by user), NOT from inside `match` (where `RemoveFilledMaker` is correct).
- `stops.DrainTriggered` already sorts by seq. Don't re-sort.
- `BestLevel(side)` is the level **on that side** (i.e. `BestLevel(Buy)` returns the best bid). The matcher needs the **opposite** side from the taker, so for a Buy taker call `BestLevel(domain.Sell)`. Read the comment in `book.go` carefully — there's a subtle naming gotcha here.
- `o.Level().(*book.PriceLevel)` is the type assertion `match` uses to recover the level pointer for `RemoveFilledMaker`. The `level` field is `any` to avoid an import cycle.

### Decimal helpers (use these, never `==`)

- `a.Cmp(b) int` — returns -1/0/1.
- `a.LessThan(b) bool`, `a.GreaterThan(b) bool`, `a.LessThanOrEqual(b) bool`, `a.GreaterThanOrEqual(b) bool`.
- `a.IsZero() bool`.
- `a.Sub(b)`, `a.Add(b)` — returns new `Decimal`; `Decimal` is a value type so this is fine.

---

## 4. Counter accounting rules

| Event | `openOrders` Δ | `armedStops` Δ | Where (file, function) |
|---|---|---|---|
| `Place(limit)` rests at original size (no fills) | +1 | 0 | `engine.go` `Place` after `e.book.Insert` |
| `Place(limit)` fully fills | 0 | 0 | n/a (no rest) |
| `Place(limit)` partial fill, would rest, cap NOT hit | +1 | 0 | `engine.go` `Place` after `e.book.Insert` |
| `Place(limit)` partial fill, would rest, **cap hit** | 0 | 0 | `engine.go` `Place` no insert; status `PartiallyFilled`; trades kept (decision (b)) |
| `Place(market)` any outcome | 0 | 0 | n/a (never rests) |
| `Place(stop)` arms (trigger not yet satisfied) | 0 | +1 | `engine.go` `Place` after `e.stops.Insert` |
| `Place(stop_limit)` arms | 0 | +1 | `engine.go` `Place` after `e.stops.Insert` |
| `Place(stop)` rejected — trigger already satisfied at placement | 0 | 0 | `engine.go` `Place`; status `Rejected`; not stored |
| `Place(stop_limit)` rejected — trigger already satisfied | 0 | 0 | same |
| `Cancel` resting order | -1 | 0 | `engine.go` `Cancel` after `e.book.Cancel` and `delete(e.byID, …)` |
| `Cancel` armed stop | 0 | -1 | `engine.go` `Cancel` after `e.stops.Cancel` |
| `Cancel` non-existent | 0 | 0 | n/a (returns `ErrOrderNotFound`) |
| Maker fully filled in `match` | -1 | 0 | `match.go` `match`; remove from `e.byID`, call `RemoveFilledMaker`, decrement |
| Maker partially filled in `match` | 0 | 0 | n/a (still resting) |
| Cascade: stop fires, becomes Market, fully fills | 0 each | -1 each | `match.go` `drainTriggeredStops`: `armedStops--` per drained, then `match`. No `openOrders` change because Market never rests. |
| Cascade: stop fires, becomes Market, partial fills | 0 | -1 | same. Market remainder dropped, no rest. |
| Cascade: stop fires, becomes Market, no liquidity | 0 | -1 | same. Market `Rejected`, no rest. |
| Cascade: stop_limit fires, becomes Limit, fully fills | 0 | -1 | same. `armedStops--`; no `openOrders++`. |
| Cascade: stop_limit fires, becomes Limit, no fill (rests) | +1 | -1 | `match.go` `drainTriggeredStops`: `armedStops--` AND `openOrders++` after `e.book.Insert`. **No cap-check in cascade.** |
| Cascade: stop_limit fires, partial fill (rests) | +1 | -1 | same. |

### Cross-checked invariants (asserted in QA tests, §9)

- After every public method returns: `e.openOrders == len(e.byID)`.
- After every public method returns: `e.armedStops == e.stops.Len()`.
- The sum `e.openOrders` never exceeds `e.maxOpenOrders` *except* transiently during a cascade that started inside `match`. By the time `Place` returns, the only way `e.openOrders > e.maxOpenOrders` is if the cascade pushed it over — which is acceptable per the design ("cap is a back-pressure signal at the user-call boundary"). Document this caveat in the engine.go top-of-file comment.

### Implementation locations summary (so A and B don't trip)

- All `e.byID` writes for **resting orders inserted by user**: `engine.go` `Place` after `e.book.Insert`.
- All `e.byID` deletes for **resting orders fully filled as makers**: `match.go` `match`, before/after `RemoveFilledMaker`. (Symmetric placement: delete from `byID` and call book's removal in the same critical region — see §4 for ordering.)
- All `e.byID` writes for **stop-limit triggered into a resting Limit**: `match.go` `drainTriggeredStops` after `e.book.Insert`.
- All `e.byID` deletes for **user cancel of a resting order**: `engine.go` `Cancel`.
- `e.openOrders` follows the same locations.
- `e.armedStops` is touched only in `engine.go` `Place` (+1 on arm, 0 on reject), `engine.go` `Cancel` (-1 on armed-cancel), and `match.go` `drainTriggeredStops` (-1 per drained).

---

## 5. Lock discipline — top-of-file comment block in `engine.go`

A must paste this as a doc comment immediately above `package engine`. Do not paraphrase; the comment is a contract that QA will read.

```go
// Package engine is the price-time-FIFO matching engine described in
// docs/system_design/04-matching-algorithm.md, /05-stop-orders.md, and
// /06-concurrency-and-determinism.md.
//
// CONCURRENCY CONTRACT.
//
//   1. The engine is protected by a single sync.Mutex (e.mu). Every
//      public method (Place, Cancel, Snapshot, Trades) takes e.mu at
//      entry and releases it via defer at exit.
//
//   2. No public method calls another public method (no re-entry into
//      the lock). The engine never spawns a goroutine. All matching
//      work runs on the caller's goroutine, serialised by the mutex.
//
//   3. Unexported helpers (match, appendTrade, updateLastTradePrice,
//      drainTriggeredStops) ASSUME e.mu is held. They are private to
//      the engine package and must never be called from outside it.
//
//   4. Every mutation of book, stops, history, lastTradePrice, byID,
//      openOrders, armedStops, or seqCounter happens under e.mu.
//
//   5. The dedup map in app.Service has its own mutex (dedupMu)
//      acquired BEFORE e.mu, on the Place path only. See
//      docs/system_design/06-concurrency-and-determinism.md for the
//      full lock-ordering argument and why deadlock is structurally
//      impossible.
//
// DETERMINISM CONTRACT.
//
//   - Time is read only via e.clock.Now(). Never call time.Now()
//     directly anywhere in this package.
//   - IDs come only from e.ids. No counter increment outside that
//     interface (other than e.seqCounter, which is the placement seq
//     and is engine-internal).
//   - Map iteration order is never observable: e.byID is used only
//     for O(1) lookup, never ranged-over for ordered work. Ordered
//     traversal goes through book / stops btrees.
//   - Decimal comparisons use .Cmp / .Equal / .IsZero only — never ==.
//
// RESOURCE BOUNDS.
//
//   openOrders == len(e.byID) after every public method returns.
//   armedStops == e.stops.Len() after every public method returns.
//   Cap checks are at the user-call boundary in Place; a cascade
//   triggered mid-Place may transiently exceed the cap (back-pressure
//   semantics — see docs/tasks/T-010-engine-core.md).
package engine
```

The four public methods must each begin with:

```go
e.mu.Lock()
defer e.mu.Unlock()
```

as the first two lines. No other interleaved logic before the lock.

---

## 6. STP cancel-newest details

Source: `docs/system_design/04-matching-algorithm.md` "Self-match policy: cancel-newest".

### Rule (reproduced)

When the head-of-FIFO maker on the opposite side has `maker.UserID == incoming.UserID`:

1. The taker's terminal status is set to `domain.StatusCancelled`. This holds **regardless** of how many trades were produced against other makers earlier in the loop.
2. Trades produced against other makers earlier in this `match` call are kept in the returned slice and have already been published via `appendTrade`.
3. The maker is **untouched**: its `RemainingQuantity`, `Status`, `Elem`, `Level` are unchanged. It remains at the head of the FIFO.
4. The taker's `RemainingQuantity` is whatever's left when STP triggers. The remainder is **not** rested, **not** dropped to history, simply abandoned with the order in `Cancelled` state.

### Where to detect STP in `match` (B's responsibility)

Detect STP **before** computing `fillQty` and **before** constructing the trade. The check goes immediately after recovering the head maker from the level's FIFO:

```go
maker := bestLevel.Orders.Front().Value.(*domain.Order)

// Self-match prevention: cancel-newest. Detect BEFORE computing fill,
// so no trade is emitted against own maker. Prior trades against other
// makers are kept. Maker is untouched.
if maker.UserID == incoming.UserID {
    incoming.Status = domain.StatusCancelled
    return trades  // break loop; caller does not insert
}
```

The `return trades` is the loop exit. After this, `Place` (in `engine.go`) sees `incoming.Status == StatusCancelled` and routes through the "match set the terminal status; nothing to rest" branch in §1's switch. No book insert.

### Edge case: STP on first iteration

If the incoming order self-matches against the very first maker, `trades == nil` and the terminal status is `Cancelled`. This is the `len(trades) == 0` row of the §04 status table for self-match. Documented as expected behaviour.

### What B must NOT do

- Do **not** mutate the maker (no status change, no `RemoveFilledMaker`).
- Do **not** call `e.book.Cancel(maker)` — the maker stays resting.
- Do **not** call `appendTrade` on a "self-trade"; no trade exists.
- Do **not** continue the loop to look for a non-self-match maker further down the FIFO. The §04 rule is "cancel-newest at the first self-match"; honest counterparties expect price-time priority, so we cannot skip a maker.

---

## 7. Cap-check ordering — algorithm for `Place` (A's responsibility)

This is the precise sequence A implements in `engine.go` `Place`. The OPEN QUESTION resolution from §1 is inlined.

### Step 0: lock and prelude

```go
e.mu.Lock()
defer e.mu.Unlock()

e.seqCounter++
order := &domain.Order{
    ID:                e.ids.NextOrderID(),
    UserID:            cmd.UserID,
    Side:              cmd.Side,
    Type:              cmd.Type,
    Price:             cmd.Price,
    TriggerPrice:      cmd.TriggerPrice,
    Quantity:          cmd.Quantity,
    RemainingQuantity: cmd.Quantity,
    CreatedAt:         e.clock.Now(),
}
order.SetSeq(e.seqCounter)
```

### Step 1: dispatch by Type

```go
switch cmd.Type {
case domain.Stop, domain.StopLimit:
    return e.placeStop(order)              // helper inside engine.go
case domain.Limit, domain.Market:
    return e.placeMatchable(order)         // helper inside engine.go
default:
    // unreachable; HTTP layer validated Type. Defensive:
    order.Status = domain.StatusRejected
    return PlaceResult{Order: order}, nil
}
```

A may inline these helpers or keep them as private methods on `*Engine`. Either is fine; helper methods make the lock-discipline comment in §5 cleaner because they all assume `e.mu` held and never lock themselves.

### Step 2a: stop / stop-limit placement (`placeStop`)

```go
// Trigger-already-satisfied check.
if order.Side == domain.Buy && order.TriggerPrice.LessThanOrEqual(e.lastTradePrice) {
    order.Status = domain.StatusRejected
    return PlaceResult{Order: order}, nil  // Note: rejection is a SUCCESS return; T-012 caches it.
}
if order.Side == domain.Sell && order.TriggerPrice.GreaterThanOrEqual(e.lastTradePrice) {
    order.Status = domain.StatusRejected
    return PlaceResult{Order: order}, nil
}

// Cap-check BEFORE arming. State has not been mutated yet.
if e.armedStops+1 > e.maxArmedStops {
    return PlaceResult{}, ErrTooManyStops
}

// Arm.
order.Status = domain.StatusArmed
e.stops.Insert(order)
e.armedStops++
return PlaceResult{Order: order}, nil
```

Note: trigger-already-satisfied returns `(PlaceResult{Order: rejected}, nil)`, **not** an error. The dedup layer (T-012) will cache this rejection so retries return the same body. Cap errors return `(PlaceResult{}, ErrTooManyStops)` — these are NOT cached by dedup.

Edge case worth pinning: at `lastTradePrice == 0` (engine just initialised, no trades yet), every buy stop with positive trigger satisfies `0 <= trigger` (the rule is buy fires at `trigger <= last`, so a fresh engine cannot reject a buy stop on this rule because `trigger > 0`). Symmetrically, every sell stop is rejectable on a fresh engine because `trigger >= 0`. **Wait — this is wrong.** Let me re-read §05: "if Side == Sell && trigger >= lastTradePrice → reject". A sell stop with trigger 50 against `lastTradePrice == 0` has `50 >= 0`, so it would be rejected. That can't be right for a fresh engine. → **Flagged in §11 (Lead's flagged ambiguities). Default: implement the rule as written; ambiguity is in the design doc, not in this plan.**

### Step 2b: limit / market placement (`placeMatchable`)

```go
trades := e.match(order)

switch {
case order.Status == domain.StatusCancelled:
    // STP fired in match. trades may be non-empty (trades against other makers).
    return PlaceResult{Order: order, Trades: trades}, nil

case order.Status == domain.StatusFilled:
    return PlaceResult{Order: order, Trades: trades}, nil

case order.Status == domain.StatusRejected:
    // Market with no trades. match set this.
    return PlaceResult{Order: order, Trades: trades}, nil

case order.Status == domain.StatusPartiallyFilled:
    // Market with partial fill. Remainder dropped, no rest. match set this.
    return PlaceResult{Order: order, Trades: trades}, nil

case order.Type == domain.Limit && len(trades) == 0:
    // Limit, no fills, would rest at original size.
    if e.openOrders+1 > e.maxOpenOrders {
        return PlaceResult{}, ErrTooManyOrders
    }
    order.Status = domain.StatusResting
    e.book.Insert(order)
    e.byID[order.ID] = order
    e.openOrders++
    return PlaceResult{Order: order, Trades: trades}, nil

case order.Type == domain.Limit:
    // Limit, partial fill. Cap-check on remainder rest.
    // OPEN QUESTION resolution: option (b). Trades are kept; cap-hit
    // truncates rest but does NOT return error.
    if e.openOrders+1 > e.maxOpenOrders {
        order.Status = domain.StatusPartiallyFilled  // truncated, NOT rested
        return PlaceResult{Order: order, Trades: trades}, nil
    }
    order.Status = domain.StatusPartiallyFilled
    e.book.Insert(order)
    e.byID[order.ID] = order
    e.openOrders++
    return PlaceResult{Order: order, Trades: trades}, nil
}
```

The cases are exhaustive given that `match` sets all terminal statuses except `StatusResting`. Add a defensive `panic("unreachable")` at the end with a comment listing the case matrix; this catches a class of regressions if `match`'s status-setting policy ever drifts.

### Why `match` runs *before* the cap check on Limit

The §04 pseudocode and the ticket Implementation notes both prescribe this. Justification: a partial fill may *consume* a maker that was holding a slot in `e.openOrders` — running match first means we evaluate the cap against the *post-match* state, which is the state the order would actually rest into. Otherwise we'd reject orders that could have rested cleanly because of consumed-and-removed makers.

---

## 8. Determinism checklist

Hard rules. CI / `go vet` / property tests will catch some of these; the rest QA reviews. None are optional.

### Banned in this package

- `time.Now()` direct call. Use `e.clock.Now()` exclusively.
- `rand.*` of any flavour.
- `go func() { … }()` — the engine spawns no goroutines.
- `==` or `!=` on `decimal.Decimal`. Use `.Cmp`, `.Equal`, `.IsZero`. (Note: `decimal.Decimal` is `shopspring/decimal.Decimal`, a struct with private fields — `==` happens to compile but compares internal representation, which is wrong for trailing-zero-equivalent values.)
- `fmt.Sprintf("%v", price)` for any price-keyed lookup. The book package's `priceKey` is the canonical key and is private to that package.
- Calling another public method from inside a public method (e.g. `Place` calling `Snapshot`). Re-entry deadlocks the mutex. If you need shared logic, factor it into a private helper that assumes the lock is held.
- Ranging over `e.byID` for ordered work. Map iteration is randomised. `e.byID` is for O(1) lookup only.
- Iterating `e.stops.byID` directly — that map is private. Always go through `stops.DrainTriggered`, which sorts.
- Iterating book levels by ranging the `levels` map — the book's `BestLevel` and `Snapshot` use the btree.
- Storing wall-clock-derived data on the order beyond `CreatedAt = e.clock.Now()`.

### Allowed

- Fixed sort with stable comparator: `stops.DrainTriggered` already does this internally — don't re-sort in `match`.
- `seqCounter` is a per-engine `uint64`. It's monotonic and read/written under the lock; no atomics needed.

### Replay test invariant (T-011 will verify; flag in this batch)

Two engines fed the same `(cmd, fakeClockTick)` sequence must produce byte-identical trade slices. If a QA test fails this, it's a determinism bug in this package.

---

## 9. QA test plan delegation (Implementer C writes these in `engine_test.go`)

QA owns `engine_test.go`. Use `clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))`, `ids.NewMonotonic()`, `inmem.NewRing(100)`. Each test calls `engine.New(deps)` with explicit `MaxOpenOrders` / `MaxArmedStops` per test.

### Layer-1 coverage from acceptance criteria (required)

#### Empty-book and trivial cases

- [ ] empty book + market → `Status=Rejected`, `len(Trades)==0`, no state mutation
- [ ] empty book + limit (any price) → `Status=Resting`, `len(Trades)==0`, in book, `openOrders==1`

#### Limit matching

- [ ] limit crosses one level → 1 trade, taker `Status=Filled`, maker `Status=Filled`, level removed
- [ ] limit crosses multiple levels → N trades in price order, taker `Status=Filled`
- [ ] limit partially crosses → trades + remainder rests, `Status=PartiallyFilled`, in book, `openOrders==1`
- [ ] limit at exactly best price → fills (gate is `<` strict, not `<=`)
- [ ] limit one tick worse than best → no fill, rests

#### Market matching

- [ ] market eats book partially → `Status=PartiallyFilled`, remainder NOT in book, `openOrders==0`
- [ ] market eats book fully → `Status=Filled`, no rest, `openOrders==0`
- [ ] market with no liquidity → `Status=Rejected`, no trades, no rest

#### FIFO

- [ ] two orders same price, taker eats both → trades produced in placement order
- [ ] two orders same price + same `CreatedAt` (test clock pinned) → still in placement order (`seq` tiebreak)

#### Self-match (STP cancel-newest)

- [ ] self-match on first maker → `Status=Cancelled`, `len(Trades)==0`, maker untouched, maker remaining qty unchanged
- [ ] self-match in middle (one external maker filled, then own resting) → `Status=Cancelled`, 1 trade kept, own maker untouched
- [ ] self-match where a deeper non-self maker exists below own → STP fires at first self-match, deeper maker is **not** consumed

#### Cancel

- [ ] cancel resting order → returns `*Order` with `Status=Cancelled`, removed from book, `openOrders==0`
- [ ] cancel armed stop → returns `*Order` with `Status=Cancelled`, removed from stop book, `armedStops==0`
- [ ] cancel non-existent → `(nil, ErrOrderNotFound)`
- [ ] cancel already-filled (maker that got filled fully via taker) → either the order is already gone from `byID`, so this returns `ErrOrderNotFound` (preferred behaviour); document the chosen code path
- [ ] cancel already-cancelled → `(nil, ErrOrderNotFound)` (cancelled orders are not in `byID` after cancel)

#### Stop arming and rejection

- [ ] stop buy, trigger > lastTradePrice → `Status=Armed`, in stop book, `armedStops==1`
- [ ] stop buy, trigger <= lastTradePrice → `Status=Rejected`, NOT in stop book, `armedStops==0`
- [ ] stop sell, trigger < lastTradePrice → `Status=Armed`
- [ ] stop sell, trigger >= lastTradePrice → `Status=Rejected`
- [ ] stop_limit same coverage as stop above

#### Stop firing and cascade

- [ ] stop fires when last trade reaches trigger → emits Market match, stop becomes Market, removed from `armedStops`
- [ ] stop_limit fires → becomes Limit. If matchable, fills; if not, rests in book and increments `openOrders`
- [ ] cascade with two stops on same trigger price → fire by `seq` (stable order)
- [ ] cascade chain: place trade triggers stop A → stop A's match triggers stop B → both fire deterministically
- [ ] cancel armed stop, then drive `lastTradePrice` past trigger → no fire (regression test: cancel must remove from both `byID` and btree)

#### Resource caps

- [ ] cap-hit on 3rd Limit with `MaxOpenOrders=2`, fresh book → `(zero, ErrTooManyOrders)`. `openOrders==2` unchanged. No book insert. No trade emitted.
- [ ] cap-hit on 2nd Stop with `MaxArmedStops=1` → `(zero, ErrTooManyStops)`. `armedStops==1` unchanged.
- [ ] **partial-fill-then-cap-hit (decision (b))**: `MaxOpenOrders=1`, place a resting limit, then place a cross taker that partial-fills the resting and would rest its own remainder. Expect: trade emitted, taker `Status=PartiallyFilled`, taker NOT in book, `openOrders==0` (resting was consumed; new limit truncated). NO error returned.
- [ ] cap-hit by counter, but actual room exists in book if you ignored the cap → still rejected (cap is the ceiling regardless of book occupancy)
- [ ] cascade overshoot of cap is allowed: configure tiny `MaxOpenOrders=1`, set up so that a cascade-triggered stop-limit must rest. Expect: cascade rests it, `openOrders == 2 > maxOpenOrders`. Document this as the back-pressure semantics. (This is the only test that asserts the "transient overshoot" behaviour — it pins it so a future "fix" doesn't silently change semantics.)

#### Counter consistency

- [ ] after 100 random place + cancel cycles, assert `e.openOrders == count of orders in book` and `e.armedStops == e.stops.Len()` (use a helper that walks the snapshot to count, or expose a test-only helper — recommend exposing `internal/engine` package-internal `func (e *Engine) testInvariants(t *testing.T)` only inside `engine_test.go` since it's `package engine`)

#### Public API surface

- [ ] `Snapshot(0)` → empty bids and asks
- [ ] `Snapshot(10)` on a 5-level book → 5 bids ordered desc, 5 asks ordered asc
- [ ] `Snapshot` does NOT include armed stops (place a stop, snapshot, assert it's not there)
- [ ] `Trades(0)` → empty
- [ ] `Trades(1500)` is clamped to 1000 (place 1100 trades, request 1500, get 1000)
- [ ] `Trades(N)` returns newest first

### Adversarial cases (Tech Lead added — these are paranoid checks)

- [ ] **STP-at-cap.** `MaxOpenOrders=1`, place a resting Limit by user X. Place a cross from user X (would self-match). Expect `Cancelled`, no error, no cap hit fired (because no resting), `openOrders==1` unchanged.
- [ ] **Cancel-after-fill regression.** Place A as resting. Place B as taker that fully fills A. Now Cancel(A.ID). Expect `ErrOrderNotFound` (A was removed from `byID` on full fill).
- [ ] **Stop fires from a cascade trade, not just a top-level Place.** Triple chain: Place limit, place taker that fills it (last=P1), place stop-limit armed at P2 just past P1, place taker that fills past P2 → cascade fires stop-limit, which produces a trade at P3, which triggers a third stop. Verify the third stop fires (cascade depth ≥ 2).
- [ ] **Decimal trailing-zero canonicalisation across engine + book.** Place limit at "100.0", place opposing limit at "100" — they must match. Also place a stop at trigger "50.0" against `lastTradePrice` of "50" — rejection rule fires correctly.
- [ ] **Stop reject does NOT consume cap.** With `MaxArmedStops=1`, place a stop that gets rejected (trigger satisfied at placement). Then place a legit stop. Expect armed (the rejected one didn't burn the slot).
- [ ] **Cancel during partial-fill remainder.** Place A (rests). Place B (partial fill of A; B rests). Cancel B. Verify A is intact, B removed, counters consistent.
- [ ] **Idempotent re-place after cancel.** Place, Cancel, Place again with a fresh `cmd`. Verify second placement is normal.
- [ ] **Snapshot under load.** 200 mixed place/cancel ops, snapshot at the end matches a manually-walked book.
- [ ] **Re-entry guard (negative test, structural).** Calling `Place` from inside `Place` is impossible because the engine takes its own mutex. Demonstrate: write a contrived test that asserts via the lock-discipline contract — or skip; this is enforced by code review.

### Fuzz-style invariant test (required even though formal property tests are deferred to T-011)

```go
func TestEngine_RandomSequence_Invariants(t *testing.T) {
    // Seed-stable RNG (math/rand with fixed seed). Generate 5 000 ops
    // mixing Place(limit), Place(market), Place(stop), Cancel of random
    // existing IDs, with multiple users (for STP coverage). After each op:
    //   - assert e.openOrders == len(e.byID)
    //   - assert e.armedStops == e.stops.Len()
    //   - assert no order in byID has Status == Filled / Cancelled / Rejected
    //   - assert e.openOrders <= e.maxOpenOrders (non-cascade path)
    //     (If a cascade overshoots, document the test allowance.)
    //   - assert all trades published have Price.Cmp(decimal.Zero) > 0
}
```

This catches the class of bugs that show up only under sequence pressure and is the primary canary before T-011's full property suite lands.

### Test file size limit

If `engine_test.go` exceeds ~600 lines, QA may split into:

- `engine_match_test.go` — limit/market/STP/FIFO
- `engine_stop_test.go` — arming, firing, cascade
- `engine_cap_test.go` — caps and adversarial cap cases
- `engine_invariant_test.go` — counter consistency, fuzz-style

All with `package engine` (not `engine_test`) so QA can inspect unexported state via test-only helpers.

---

## 10. Done criteria for the batch

The batch is **green** when ALL of these are true:

- [ ] OPEN QUESTION on partial-fill-then-cap-hit resolved per §1 (decision (b)).
- [ ] `engine.go`, `match.go`, `errors.go`, `engine_test.go` all present and committed.
- [ ] `go vet ./internal/engine/...` clean, exit 0.
- [ ] `go test ./internal/engine/... -race -count=10` green. (`-count=10` smokes deterministic-replay-style flakes that one run wouldn't catch.)
- [ ] `go build ./...` clean.
- [ ] No new module dependencies (only `shopspring/decimal` and `google/btree`, both already in `go.mod`).
- [ ] No edits in `docs/system_design/` (the design is locked).
- [ ] Top-of-file lock-discipline comment block present in `engine.go` per §5.
- [ ] No `time.Now()`, no `go func`, no `==` on `decimal.Decimal` anywhere in `internal/engine/*.go` (grep proof acceptable in final review).
- [ ] Counter invariants `openOrders == len(byID)` and `armedStops == stops.Len()` hold at the end of every test; the fuzz-style invariant test in §9 passes.
- [ ] Imports limited to: stdlib, `internal/domain`, `internal/domain/decimal`, `internal/ports`, `internal/engine/book`, `internal/engine/stops`. Nothing else.

A merges first (engine.go + errors.go) so B has the struct shape to compile against; B then merges match.go; QA writes engine_test.go last. **Recommend a single PR with all four files for atomic review**, but if the team prefers stacked PRs, the order above is the sequence.

---

## 11. Lead's flagged ambiguities

These are real ambiguities I found while reading the spec + B2 code that aren't the OPEN QUESTION. Each gets a default; implementers should NOT escalate, just follow the default and surface the case in code review notes.

### 11.1 Trigger-already-satisfied for sell stops on a fresh engine

The §05 rule "if Side == Sell && trigger >= lastTradePrice → reject" combined with `lastTradePrice = decimal.Zero` on engine init means **every** sell stop placed before the first trade is rejected (any positive trigger >= 0). This contradicts user intent (a fresh exchange should accept stops).

**Default:** implement the rule as written. The spec is explicit. The "no trades yet" startup state is a non-issue in production (the engine boots warm from snapshot in real ops). For tests: arrange a first trade before placing sell stops, OR initialise `lastTradePrice` via a test helper if the QA test suite needs it. Do **not** change the rule.

**Surface to architect** in PR description so they can confirm or open a follow-up ticket.

### 11.2 `Cancel` on a maker that is partially filled

A resting maker that's been partial-filled has `Status=PartiallyFilled` and is still in `e.byID` and the book. The §04 state machine includes `PartiallyFilled → Cancelled: DELETE`. Acceptance criteria covers "cancel resting" but not explicitly "cancel partially-filled-maker".

**Default:** the engine's `Cancel` does not branch on `Status`; it cancels any order in `e.byID` (which by invariant is non-terminal). `PartiallyFilled` makers are in `e.byID` and so are cancellable; the cancel sets `Status=Cancelled`, removes from book, decrements `openOrders`. QA: add this as an explicit test case (already in §9 "Cancel during partial-fill remainder").

### 11.3 Setting `lastTradePrice` to `decimal.Zero` at init

Documented above (§11.1). The init value affects which stops are rejectable. The spec doesn't say what `lastTradePrice` is before any trade, but `decimal.Zero` is the obvious default (no other sane value exists without a reference price).

**Default:** `lastTradePrice = decimal.Zero` at `New`. Document in `engine.go` New() with a comment.

### 11.4 `Trades(limit)` clamp behaviour for negative limits

Spec says clamp at 1000. Doesn't specify behaviour for negative. The publisher's `Recent(limit)` already returns `[]*domain.Trade{}` for `limit <= 0`.

**Default:** `Trades(-5)` returns `[]`. No error. Inherit publisher's behaviour — don't add a wrapper layer of validation. Document with a one-liner comment.

### 11.5 `Snapshot(depth)` clamp

`book.OrderBook.Snapshot` already clamps internally to `[0, 1000]`. The engine just delegates.

**Default:** engine's `Snapshot` calls book directly, no engine-level clamp.

### 11.6 Should `byID` include the `Order` for a triggered-into-Limit-that-fully-filled?

A stop-limit that triggers, has its `Type` rewritten to `Limit`, runs `match` and fully fills as a taker — does it ever land in `e.byID`? **No.** It was never resting (taker that filled). The cascade removed it from `stops.byID` before re-entry; the match never inserts it. Counter accounting confirms: `armedStops--`, no `openOrders` change.

**Default:** as written. No test needed beyond confirmation in §9 cascade tests.

---

End of plan.
