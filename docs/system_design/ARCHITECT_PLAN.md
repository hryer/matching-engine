# Architect Execution Plan — Reku Matching Engine

> Up: [README index](./README.md)

Companion to the system design files (§01–§11). The README is *what we build and why*. This document is *what I, as architect, must do to get it built within the 8-hour budget without producing a buggy or indefensible system*.

This is a working document. Tight, decision-oriented prose. No marketing.

---

## 1. Mission

Ship an in-memory, single-pair matching engine in Go that passes the case study's rubric. Concretely, the deliverable is correct on:

- (a) all four order types: limit, market, stop, stop-limit
- (b) the four HTTP endpoints: POST /orders, DELETE /orders/{id}, GET /orderbook, GET /trades
- (c) deterministic replay
- (d) decimal-correct arithmetic
- (e) thread-safe behaviour under concurrent HTTP traffic
- (f) a test pyramid of unit + property + one HTTP integration test
- (g) a top-level README that defends every non-obvious choice

### Rubric mapping

| Rubric line | What earns the point | Where it lives |
|---|---|---|
| Correctness | All matching invariants hold; tests pass; deterministic replay test passes 1000× | §3 invariants, §5 phase 2/4 tests |
| Data modeling | Defensible book/stop-book structure, decimal handling, ID model | [§02](./02-data-structures.md), [§03](./03-order-book.md), [§05](./05-stop-orders.md), [§07](./07-decimal-arithmetic.md) |
| Concurrency | One mutex on engine, lock-discipline documented and held in code | [§06](./06-concurrency-and-determinism.md) |
| Code quality | Hexagonal seams, no allocation in hot path beyond `container/list`, structured errors | [§01](./01-architecture.md) |
| Communication | README leads with v1; v2 is clearly extension; HFT sidebar shows scope awareness | [§10](./10-hft-considerations.md), [§11](./11-production-evolution.md) |

Success = a reviewer can run `go test ./...` and `go run ./cmd/server`, place an order via curl, and read the docs without being confused about what is shipped versus aspirational.

---

## 2. Constraints and explicit non-goals

Mirroring the brief — these are off the table for v1, and any drift toward them is over-engineering:

- No persistence (no WAL, no snapshots, no replay-from-disk on startup)
- No auth, rate limiting, or per-user balances / KYC
- No multiple pairs (BTC/IDR only; `Instrument` is a constant, not a routing key)
- No WebSocket, gRPC, FIX
- No Kafka, NATS, or external publisher
- No IOC / FOK / iceberg / post-only / trailing-stop / hidden / peg
- No fees, no maker/taker schedule
- No admin endpoints
- No observability beyond stdlib `log` for errors and panics
- No graceful state migration — restart wipes state

Architect rule: if a thought begins "while we're at it, let's also…", check it against this list before opening an editor.

---

## 3. Critical correctness invariants

These are the *load-bearing* properties. Every test layer ultimately tests one of these. The property-test layer (§5 phase 4) checks them automatically against random valid order streams.

### Engine-level

1. **Book never crosses.** When both sides are non-empty, `bestBid().Price < bestAsk().Price`. Equality is also a violation — equal-price crossing should have produced a trade.
2. **Sum-of-fills bound.** For any order, `sum(trade.Quantity for trade where order participated) ≤ order.Quantity` (original). Strict equality holds iff `Status ∈ {Filled}`.
3. **Conservation of quantity per trade.** For every trade `t`: `t.Quantity` is decremented from *both* taker and maker `RemainingQuantity` in the same critical section.
4. **Trade price is maker price.** Always. Validated in tests; defended in [§04](./04-matching-algorithm.md).
5. **Deterministic replay.** Two engines initialised with identical fake clock + ID generator + identical command sequence produce byte-identical trade JSON. Run 1000×.
6. **Status finality.** Once an order reaches `Filled`, `Cancelled`, or `Rejected`, it cannot transition further. `DELETE` on a terminal order returns 409.
7. **No self-trade ever.** Under cancel-newest STP, no trade has `taker.UserID == maker.UserID`.

### Stop-book-level

8. **Armed stops invisible in snapshot.** `GET /orderbook` walks `OrderBook` only. The `StopBook` is never consulted during snapshot construction.
9. **Trigger-already-satisfied → reject.** Stop placed with trigger crossing `lastTradePrice` returns `Status=Rejected`, `len(trades)==0`, not stored anywhere.
10. **Stop fires at most once per cascade.** Removed from `byID` *before* re-submitted to `match`. Termination is `O(stops + trades)`.
11. **Cascade order is deterministic.** When multiple stops trigger on the same `lastTradePrice` update, they fire in ascending `seq` order — not in btree iteration order, not in map iteration order.

### Concurrency / determinism

12. **Single-writer invariant.** Every mutation to the order book, stop book, trade history, or `lastTradePrice` happens with the engine mutex held.
13. **No lock re-entry.** No public engine method calls another public engine method. No internal helper calls a public method.
14. **No engine-spawned goroutines.** All engine work runs on the caller's (HTTP handler's) goroutine.
15. **Map iteration never affects output.** Maps are O(1) lookup only. Every ordered traversal goes through the btree or sorted slice.

### Idempotency

16. **`POST /orders` is idempotent within a process lifetime.** For any given `(user_id, client_order_id)`, the engine is invoked at most once and every subsequent call returns the cached `PlaceResult` byte-identically. The dedup map is wiped on restart (consistent with the in-memory state model).

### Resource bounds

17. **Engine-resident counts match the book + stop book at all observable points.** `engine.openOrders == count(orders resting in book)` and `engine.armedStops == count(byID in stops)` after every public engine method returns. Counters are mutated only inside the engine mutex. Property test asserts equality after each random operation.

These 17 invariants are the test plan in compressed form. Anything beyond them in v1 is gold-plating; anything missing is a bug.

---

## 4. Risk register

For each major design choice, the failure mode and the architect-level guard. This is the document I want open while writing the test plan.

| Risk | Failure mode | Guard |
|---|---|---|
| Decimal precision drift | Repeated subtraction (`remaining -= fill`) loses trailing zeros, breaks `IsZero`. | Always compare via `Cmp` not `==`. Use `.IsZero()` not `== Zero`. Property test: terminal-state orders have `RemainingQuantity.IsZero() == true`. |
| FIFO determinism under same `time.Now()` | Test clock returns identical `time.Time`; FIFO order becomes ambiguous. | `Order.seq` (uint64 monotonic) is the tie-breaker. `container/list` already preserves insertion order; `seq` exists for snapshot ordering and stop-cascade ordering. |
| Stop cascade infinite loop | Stop A triggers stop B which re-triggers A; engine spins or stack-overflows. | Each stop is `Delete`d from `byID` *before* its `match` re-entry. Property test: cascade with N stops produces ≤ N stop-fires. |
| Stop cascade re-ordering by btree iteration | Multiple stops at the same trigger fire in btree's natural order, not placement order. | Collect triggered stops into a slice, `sort.SliceStable` by `seq` before re-submit. Test exercises three same-trigger stops. |
| Mutex re-entry / deadlock | Public method calls another public method; second `Lock()` deadlocks. | Convention documented at top of `engine.go`: public methods lock; helpers assume held. Code review checklist item before merge. |
| Snapshot inconsistency under concurrent writer | Cross-side scan interleaved with a fill produces a "crossed" snapshot. | Single mutex held for the entire snapshot. Property test runs `Snapshot` concurrently with random placements and asserts `bestBid < bestAsk`. |
| `container/list` allocation per `PushBack` | Per-order GC pressure under load. | Acknowledged in [§03](./03-order-book.md). Not optimised in v1; would move to intrusive list in HFT mode ([§10](./10-hft-considerations.md)). |
| btree `Less` non-determinism for equal prices | Two orders at the same price might be ordered by pointer address. | btree stores `*PriceLevel` (one entry per price), not `*Order`. Within a level, `container/list` is FIFO. No equal-price tie in the btree. |
| `decimal.Decimal` map key non-canonical | `"500000000"` and `"500000000.0"` hash differently → two price levels. | Canonicalise key via a helper: `priceKey(d) = d.Truncate(18).String()` after stripping trailing zeros. Test the canonicalisation explicitly. |
| Market order with no liquidity returning HTTP 4xx | Caller treats business-rejected as protocol error; retry storms. | Documented in [§08](./08-http-api.md): 201 with `status: rejected`. Test asserts the status code. |
| Cancel of armed stop forgets to clean btree | Cancelled stop still in btree → fires later as ghost order. | `StopBook.Cancel(id)` deletes from both `byID` and the side-specific btree in the same critical section. Test: place, cancel, drive `lastTradePrice` past trigger, assert no fire. |
| ID counter overflow | uint64 wraps after ~10^19 — never in practice. | Document in README. No code guard. |
| Hexagonal layout misread as overbuilt | Reviewer scores down for ports/adapters at v1 scope. | [§01](./01-architecture.md) has explicit "honest caveat against the brief." Walkthrough leads with engine package, names ports as v2 seams. |
| Unbounded input → OOM / DoS | Oversized `quantity`, `price`, `user_id`, body, or unbounded order/stop accumulation crashes the process. | Hard limits at HTTP boundary (`quantity ≤ 10¹⁵`, `price ≤ 10¹⁵`, `user_id` 1..128 chars, body ≤ 64 KB). Engine-wide caps (`openOrders ≤ 10⁶`, `armedStops ≤ 10⁵`) returning sentinel errors → HTTP 429. Per [§08](./08-http-api.md) Resource Bounds. |
| Order/stop counter drifts from book reality | Increment without decrement (or vice versa) makes the cap wrong over time. | Invariant 17 in §3 — property test asserts counter equality with a fresh recount after every random op. |

---

## 5. Implementation sequence

Tests first per the brief. Phases are sized so a failing checkpoint lets us collapse scope without losing the rubric.

Total budget: **8 hours**. Numbers are estimates; reality varies ±25%.

### Phase 1 — Domain types + decimal wrapper [~1.0 h]

- [ ] `internal/domain/decimal/decimal.go` — alias over `shopspring/decimal`. One file.
- [ ] `internal/domain/enums.go` — `Side`, `Type`, `Status` as `uint8` with `MarshalJSON` / `UnmarshalJSON` matching brief wire format.
- [ ] `internal/domain/order.go` — `Order` struct including `seq`, `elem`, `level` (unexported).
- [ ] `internal/domain/trade.go` — `Trade` struct.
- [ ] `internal/domain/instrument.go` — `Instrument` type alias; v1 constant `BTCIDR`.
- [ ] Unit test for enum JSON round-trip.

Checkpoint: `go build ./...` clean, `go test ./internal/domain/...` green.

### Phase 2 — OrderBook + matching, tests-first [~2.0 h]

- [ ] `internal/engine/book/book.go` — `OrderBook`, `side`, `PriceLevel`. btree + map + `container/list`.
- [ ] `internal/engine/book/level.go` — `PriceLevel.Total` invariant on every mutation.
- [ ] `internal/engine/book/book_test.go` — table-driven: insert, best price, snapshot, cancel-by-element. Golden cases from [§09](./09-testing.md) layer 1 (the matching ones that don't involve stops).
- [ ] `internal/engine/match.go` — pure `match(book, incoming) []*Trade`. No engine dependencies; takes interfaces for ID/clock.
- [ ] `internal/engine/engine_test.go` — empty-book market reject, limit rests, limit crosses one level, multi-level cross, partial fill rests, FIFO within level, self-match cancel-newest.

Checkpoint: all matching invariants 1–6 from §3 testable and green.

### Phase 3 — StopBook + cascade [~1.0 h]

- [ ] `internal/engine/stops/stops.go` — two btrees + `byID` map, `Insert`, `Cancel`, `DrainTriggered(lastPrice) []*Order`.
- [ ] `internal/engine/stops/stops_test.go` — armed stop visible in `byID` not in book; cancel removes from both; trigger-already-satisfied path; same-trigger ordering by `seq`.
- [ ] `Engine.drainTriggeredStops` — recursive cascade with the deterministic sort.
- [ ] Cascade test: two stops, one fires, its trade triggers the other, both fire in `seq` order.

Checkpoint: invariants 8–11 green.

### Phase 4 — Engine glue + property tests [~1.0 h]

- [ ] `internal/engine/engine.go` — `Engine` struct, `Place`, `Cancel`, `Snapshot`, `Trades`. Single `sync.Mutex`. `Deps` struct for clock, IDs, publisher.
- [ ] `internal/ports/{publisher,clock,ids}.go` — interfaces only.
- [ ] `internal/adapters/{clock,ids,publisher/inmem}/...` — minimal implementations.
- [ ] `internal/engine/engine_property_test.go` — seeded random valid order stream; assert invariants 1, 2, 7, 12, 15 after each step. 100 iterations × 100 ops each.
- [ ] Determinism replay test: same seed → byte-identical trade JSON. Run with `-count=1000`.

Checkpoint: invariants 1, 2, 5, 7, 12–15 mechanically asserted.

### Phase 5 — HTTP layer + integration test [~2.5 h]

- [ ] `internal/adapters/transport/http/dto.go` — request/response types with string decimals. `PlaceOrderRequest` includes required `client_order_id` (no `omitempty`); `OrderDTO` echoes it back.
- [ ] `internal/adapters/transport/http/middleware.go` — `bodyLimit(64<<10)` using `http.MaxBytesReader`; wraps POST routes. `MaxBytesError` → HTTP 413 `request_too_large`.
- [ ] `internal/adapters/transport/http/handlers.go` — `POST /orders`, `DELETE /orders/{id}`, `GET /orderbook`, `GET /trades` using `http.ServeMux` and Go 1.22+ pattern routing.
- [ ] Validation pipeline per [§08](./08-http-api.md): step 0a `client_order_id` 1..64 ASCII printable, step 0b `user_id` 1..128 ASCII printable, plus numeric bounds (`quantity ≤ 10¹⁵`, `price ≤ 10¹⁵`, `trigger_price ≤ 10¹⁵`).
- [ ] Error model per [§08](./08-http-api.md) — 201 for business-rejected, 400 for validation, 404, 409, 413 for body too large, 429 for engine cap exhaustion.
- [ ] `internal/engine/errors.go` — `ErrTooManyOrders`, `ErrTooManyStops` sentinels.
- [ ] `internal/engine/engine.go` — `Engine` struct gains `openOrders`, `armedStops`, `maxOpenOrders`, `maxArmedStops` fields. Constructor takes caps. Increment on rest/arm, decrement on cancel/full-fill/trigger. Cap check returns sentinel.
- [ ] `internal/engine/engine_test.go` — cap-hit tests with small caps (e.g. `maxOpenOrders=2`); counter invariants after place/cancel/fill cycles.
- [ ] `internal/app/service.go` — `Place` with dedup map; pass through engine cap sentinels (do **not** cache cap errors).
- [ ] `internal/app/service_test.go` — dedup unit tests: cached on duplicate, distinct on different keys, business-rejected cached, errors (incl. cap errors) not cached, concurrent same-key serialises to one engine call.
- [ ] `internal/adapters/transport/http/handlers_test.go` — `httptest` integration: place limit, place crossing limit, GET trades, GET orderbook, duplicate `client_order_id` returns identical body, missing `client_order_id` returns 400, oversized body returns 413, oversized numeric values return 400, cap-hit returns 429.
- [ ] `cmd/server/main.go` — composition root, signal handling, `http.Server.Shutdown`. Pass cap constants (`MaxOpenOrders=1_000_000`, `MaxArmedStops=100_000`, `MaxBodyBytes=64<<10`) into `engine.New(...)` and middleware.

Checkpoint: `go run ./cmd/server` starts; `curl -X POST localhost:8080/orders ...` round-trips.

### Phase 6 — README + repo polish [~1.0 h]

- [ ] Top-level `README.md` (project root) — how to run, how to test, decisions summary cross-referencing [`docs/system_design/README.md`](./README.md).
- [ ] Cross-link from project README → `docs/system_design/README.md` and this file.
- [ ] `Makefile` or shell snippets for `make test`, `make run` (optional, only if it saves the reviewer time).
- [ ] `docker-compose.yml` (optional per brief).
- [ ] Smoke test: re-read all four endpoints' curl examples and verify payloads still match the docs.

Checkpoint: a fresh reader can clone, `go test ./...`, `go run ./cmd/server`, and place an order in under 5 minutes.

### Slack budget [~0.5 h]

Reserved for: a flaky property test seed, a JSON marshalling subtlety, a `time.Time` equality bug, fixing one cancel-newest edge case I missed in design.

If slack runs out: cut docker-compose, cut the ring-buffer publisher (use a plain slice on Engine), cut `app.Service` and call engine directly from handlers. Do **not** cut tests.

---

## 6. Architect ownership vs implementer ownership

Clear boundary so I don't waste time re-deciding things mid-implementation.

### Architect (decided before any code is written)

- Hexagonal layout choice and the file tree ([§01](./01-architecture.md))
- Order book data structures (map + btree + list) ([§03](./03-order-book.md))
- Stop book data structures (twin btrees + byID) ([§05](./05-stop-orders.md))
- Self-match policy: **cancel-newest** ([§04](./04-matching-algorithm.md))
- Trade price = **maker price** ([§04](./04-matching-algorithm.md))
- Trigger-already-satisfied → **reject** ([§05](./05-stop-orders.md))
- Stop cascade ordering by `seq` ([§05](./05-stop-orders.md))
- Concurrency model: **single `sync.Mutex` on Engine** ([§06](./06-concurrency-and-determinism.md))
- Determinism strategy ([§06](./06-concurrency-and-determinism.md))
- Decimal library choice: `shopspring/decimal` ([§07](./07-decimal-arithmetic.md))
- ID format: `"o-<n>"` / `"t-<n>"` from monotonic `uint64` counter
- HTTP error model: 201 for business-rejected, 400/404/409 elsewhere ([§08](./08-http-api.md))
- Idempotency: **required `client_order_id`** body field, deduped at `app.Service` ([§08](./08-http-api.md))
- Resource bounds: per-field input limits + engine-wide caps (`openOrders ≤ 10⁶`, `armedStops ≤ 10⁵`); per-user fairness deferred to v2 ([§08](./08-http-api.md))
- Test taxonomy: stdlib `testing`, no `testify`, three layers ([§09](./09-testing.md))
- The 15 invariants in §3 of this document

### Implementer (executes the spec)

- Actual Go code to satisfy the above
- Test fixtures and golden files
- Variable naming, comment density, error messages
- File splits within a package when one file gets long
- HTTP handler micro-structure (helper functions, decode helpers)
- `cmd/server` flag parsing, signal handling
- Whether to use `errors.Is` vs sentinel comparison — implementation detail

If the implementer hits a question that requires re-deciding any architect-owned bullet, that escalates back to architect. In a one-person delivery this is a 30-second context switch; document the decision in this file before changing anything.

---

## 7. Open decisions to lock down before coding starts

Ambiguities from the brief that the architect must resolve and write down. All are decided here so phase 1 can start immediately.

| Open question | Decision |
|---|---|
| Trade price = taker limit or maker resting price? | **Maker resting price.** Standard exchange convention. Tested explicitly. ([§04](./04-matching-algorithm.md)) |
| Business-rejected placement HTTP status? | **201** with `status: rejected` in body. Validation errors are 400. ([§08](./08-http-api.md)) |
| Stop trigger inclusive or exclusive? | **Inclusive.** Buy fires when `last >= trigger`; sell fires when `last <= trigger`. Brief says so explicitly. |
| Trigger satisfied at placement: reject or fire immediately? | **Reject.** Brief permits either; we choose reject because stops are protective by intent. ([§05](./05-stop-orders.md)) |
| Idempotency on `POST /orders`? | **Required `client_order_id` body field.** 1..64 ASCII printable chars; missing/empty/too long → HTTP 400 `validation`. Deduped at `app.Service` via `map[(user_id, client_order_id)] → PlaceResult`, in-memory, wiped on restart. Engine never sees the field. ([§08](./08-http-api.md), invariant 16 in §3) |
| Cancel of already-cancelled order? | **HTTP 409.** Status finality is invariant 6 (§3). |
| Snapshot consistency level? | **Engine-mutex-locked, atomic across both sides.** No `RWMutex`. ([§06](./06-concurrency-and-determinism.md)) |
| Trade history bound? | **Last 10,000 trades, ring buffer.** Older trades silently dropped. Documented in top-level README. |
| ID format? | `"o-<n>"` for orders, `"t-<n>"` for trades, `n` from monotonic `uint64` starting at 1. |
| Test framework? | **stdlib `testing` only.** No `testify`. Brief evaluators value boring choices. |
| Property test framework? | **Hand-rolled seeded generator** in `engine_property_test.go`. No `gopter`. |
| Decimal max precision? | **18 decimal places.** Validated at HTTP boundary; engine assumes valid input. |
| What `user_id` validation? | **None.** Brief says "callers are trusted." Treat as opaque string. |
| Empty book + market order: 400 or 201-with-rejected? | **201 with `status: rejected`.** Same as trigger-already-satisfied. |
| Order ID collisions across server restarts? | **N/A — restart wipes state.** ID counter resets to 1 on restart. |
| Default `depth` and `limit` for snapshot/trades when query param missing? | `depth=10`, `limit=50`. Cap both at 1000 to bound response size. |
| Numeric value upper bounds? | `quantity ≤ 10¹⁵`, `price ≤ 10¹⁵`, `trigger_price ≤ 10¹⁵`. Validated at HTTP layer. Exceed → 400 `validation`. ([§08](./08-http-api.md) Resource Bounds) |
| `user_id` length limit? | 1..128 chars, ASCII printable. Brief says "trusted callers" — that justifies skipping auth, not skipping length validation. Exceed → 400 `validation`. |
| HTTP request body size? | 64 KB hard cap via `http.MaxBytesReader` middleware. Exceed → **413** `request_too_large`. |
| Engine open-orders cap? | **1,000,000** engine-wide. Counter incremented on rest, decremented on cancel/full-fill, mutated only inside engine mutex. Exceed → engine returns `ErrTooManyOrders`, surfaced as **429** `too_many_orders`. Per-user cap deferred to v2. |
| Engine armed-stops cap? | **100,000** engine-wide. Same pattern as orders. Exceed → 429 `too_many_stops`. |

If any of these surfaces a bug during interview probe, the answer is "yes, this was the decision, here's the trade-off" — never "I didn't think about that."

---

## 8. Interview readiness checklist — what to defend in the design walkthrough

The brief's Part 2 explicitly probes data structure choice, concurrency, correctness, and edge cases. For each I need a 60-second answer plus a section cross-reference.

- [ ] **Why map + btree + list and not a single structure?**
  Each structure does one thing well: O(1) lookup, O(log n) ordered iteration, O(1) FIFO. Combined cost is one extra dependency (`google/btree`) and back-pointers to remove from a level on cancel. ([§03](./03-order-book.md))
- [ ] **Why not a skiplist or hand-rolled tree?**
  Skiplist is reasonable but no stdlib version; hand-rolled red-black is code volume with no benefit at this scope. ([§03](./03-order-book.md))
- [ ] **Why `shopspring/decimal` and not integer minor units?**
  Real exchanges *do* use integer minor units. We don't, for scope: instrument-scale config is unjustified for one pair, and the matcher is not the bottleneck behind JSON. ([§07](./07-decimal-arithmetic.md), [§10](./10-hft-considerations.md))
- [ ] **Why one mutex and not RWMutex / per-side / actor?**
  Snapshot needs cross-side consistency anyway, so RWMutex helps only under unusual contention; per-side serialises the same critical sections two-phase; actor adds goroutine choreography for no parallelism win. ([§06](./06-concurrency-and-determinism.md))
- [ ] **Self-match policy?**
  Cancel-newest. Preserves resting liquidity, idempotent on retry, no mid-match book mutation. ([§04](./04-matching-algorithm.md))
- [ ] **Stop cascade — does it terminate?**
  Yes. Each stop is removed from `byID` before its match re-entry; armed-stop set strictly shrinks during a cascade; total work O(stops + trades). Stack-depth caveat acknowledged in [§05](./05-stop-orders.md).
- [ ] **Determinism — what do you mean and how do you test it?**
  Same command sequence → byte-identical trades. Achieved by: monotonic `seq`, injectable `Clock`, btree-only iteration, no engine goroutines. Tested by running a 50-order replay 1000×. ([§06](./06-concurrency-and-determinism.md))
- [ ] **What goes wrong if I send a million armed stops?**
  Cascade fires them in O(n) work. Stack-depth becomes a real concern in HFT mode → convert to explicit FIFO queue. Not v1. ([§05](./05-stop-orders.md))
- [ ] **Why hexagonal — isn't this overbuilt?**
  Honest answer: yes, slightly, against the brief's "do not overbuild" line. The trade buys cleanly-defendable v2 seams and adds ~8 files of pure interface stubs. The engine package itself is identical to what a flat layout would produce. ([§01](./01-architecture.md))
- [ ] **What's not in v1?**
  The list in §2 of this document, verbatim. Memorise the off-the-table list so probes don't catch me defensive.
- [ ] **What's the v2 punchlist?**
  The 8 bullets in [§11](./11-production-evolution.md). Reference component diagram for details.

If a probe lands outside this checklist, the safe answer is: "I haven't decided; here's the trade-off space" — then articulate the axes (latency vs. correctness, complexity vs. throughput, etc.). Avoid bluffing.

---

## 9. Self-check before declaring done

A reviewer's eye over the deliverable. Run this before submission.

- [ ] `go vet ./...` and `go test ./... -race -count=10` clean
- [ ] Replay determinism test passes 1000×
- [ ] All 15 invariants in §3 either covered by a named test or asserted in the property test
- [ ] Decision summary in [README](./README.md) matches what the code actually does
- [ ] No file imports a package outside its allowed dependency direction (engine → domain + ports only; adapters → ports + domain; cmd → everything)
- [ ] No `panic` on caller-side bad input — every panic path is engine-internal invariant violation
- [ ] No `time.Now()` call inside the engine package; all time goes through `ports.Clock`
- [ ] No `range` over a map in any code path that affects observable output
- [ ] No `float64` anywhere in the codebase
- [ ] HTTP error responses match the brief's example shape
- [ ] At least one HTTP integration test exists and runs
- [ ] Top-level `README.md` (project root) is under one screen and answers: how to run, how to test, where the design doc lives, what's not built
- [ ] The brief's example payloads (POST /orders, GET /orderbook) round-trip with the implementation

If any item fails, fix it or document it as known limitation in the top-level README. Do not ship silent gaps.
