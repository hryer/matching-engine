# T-008 — OrderBook + PriceLevel

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B2 |
| Blocks | T-010 |
| Blocked by | T-001, T-002, T-004 |
| Touches files | `internal/engine/book/book.go`, `internal/engine/book/level.go`, `internal/engine/book/book_test.go` |

## Goal

Implement the order book per side using `map[priceKey]*PriceLevel` + `github.com/google/btree` + `container/list`. Specified in [§03 Order Book](../system_design/03-order-book.md).

## Context

The book is the single most-tested data structure in the engine. It exposes:

- `Insert(o *domain.Order)` — places at the back of the FIFO at its price level
- `Cancel(o *domain.Order)` — O(1) remove using the `elem` and `level` back-pointers; removes the level from btree+map if it becomes empty
- `BestLevel(side Side) *PriceLevel` — best bid (max price) or best ask (min price)
- `Snapshot(depth int) (bids, asks []LevelSnapshot)` — top N levels per side, ordered best to worst

`PriceLevel.Total` is maintained incrementally on every insert / fill / cancel so a depth-N snapshot is O(N), not O(N · level_size).

`priceKey` canonicalises `decimal.Decimal` so that `"500000000"` and `"500000000.0"` collide on the same key. Strip trailing zeros via the chosen normalisation.

The engine (T-010) holds a single `*OrderBook` and a separate `map[orderID]*domain.Order` for cancel-by-ID. The book itself does not maintain that map — it works at the `*Order` level and relies on the order's `elem` / `level` back-pointers.

## Acceptance criteria

- [ ] `book.go` defines `OrderBook` containing `bids *side` and `asks *side`
- [ ] `side` carries `levels map[string]*PriceLevel` and `index *btree.BTreeG[*PriceLevel]`. The btree comparator orders **descending** for bids and **ascending** for asks (one comparator factory taking an `isBid bool`)
- [ ] `level.go` defines `PriceLevel{Price decimal.Decimal; Orders *list.List; Total decimal.Decimal}` plus methods to add/remove an order while maintaining `Total`
- [ ] `OrderBook.Insert(o *domain.Order)` — O(1) at existing level, O(log n) at new level. Sets `o.elem` and `o.level` back-pointers
- [ ] `OrderBook.Cancel(o *domain.Order)` — O(1) when level remains non-empty, O(log n) when level removed. Decrements `level.Total` by `o.RemainingQuantity`. Clears `o.elem` and `o.level` back-pointers
- [ ] `OrderBook.BestLevel(side domain.Side) *PriceLevel` — returns nil on empty side
- [ ] `OrderBook.RemoveFilledMaker(level *PriceLevel, o *domain.Order)` — O(1) for the maker; level removed if it becomes empty. (Or: a single `removeOrder` private helper that both `Cancel` and the matcher use, with a public name for each.) Choose a clean factoring; the design constraint is the matcher (T-010) needs to remove a fully-filled maker from the head of FIFO without re-doing the back-pointer dance
- [ ] `OrderBook.Snapshot(depth int) (bids, asks []LevelSnapshot)` — `LevelSnapshot{Price, Quantity decimal.Decimal}`. Bids ordered descending; asks ordered ascending. Cap depth at `1000` (per [`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md#7-open-decisions-to-lock-down-before-coding-starts))
- [ ] `priceKey` canonicalisation function lives here (private or `priceKey` exported as needed by tests). Test paired inputs that should collide: `"500000000"` and `"500000000.0"` resolve to the same key
- [ ] `level.Total == sum(o.RemainingQuantity for o in level.Orders)` is invariant. A non-test helper `(*PriceLevel).validateTotal() error` is acceptable but not required; the property test in T-011 will assert it
- [ ] `book_test.go` covers all matching-relevant cases: insert builds the level; insert at existing level is FIFO; cancel on single-order level removes the level; cancel on multi-order level decrements `Total`; best bid is max, best ask is min; snapshot at depth 1, 5, 1000; canonicalisation collision; insert across multiple levels iterates in correct order via `BestLevel` then a hypothetical "next-best" walk
- [ ] `go.mod` includes `github.com/google/btree` v1.x. Run `go get github.com/google/btree`
- [ ] `go vet ./internal/engine/book/...` and `go test ./internal/engine/book/...` clean

## Implementation notes

- `btree.BTreeG[*PriceLevel]` is the generic variant in v1.x of `google/btree`. The `Less` function: `func(a, b *PriceLevel) bool { return a.Price.LessThan(b.Price) }` for asks (ascending), inverted for bids.
- Two btrees share the comparator pattern but reverse the inequality. Encapsulate in a constructor: `newSide(isBid bool) *side`.
- `priceKey` candidate implementation: `d.Coefficient()` and `d.Exponent()` to assemble a normalised key, OR `d.Truncate(maxPrecision).String()` after a no-op `.Add(decimal.Zero)` (which strips trailing zeros in some library versions). Validate either by test.
- The `level any` field on `Order` (T-004) is set to `*PriceLevel` here. The matcher (T-010) reads back via `o.Level().(*PriceLevel)`. Keep the cast inside book/match — domain remains `any`-typed.
- `Cancel` must be safe to call only when `o.elem != nil` and `o.level != nil` (i.e. the order is currently resting). Document. The engine (T-010) checks `Status == Resting || Status == PartiallyFilled` before calling `Cancel`.
- Do **not** add `Cancel(orderID string)` here. Cancel-by-ID is the engine's responsibility (T-010 keeps the `byID` map). The book takes a `*domain.Order` it's already been given.
- Do **not** add a `Walk` / iteration API just for the matcher. The matcher peeks `BestLevel` and pops `Front()` of `level.Orders` itself. Keep the surface narrow.

## Out of scope

- StopBook (T-009).
- Matching logic (T-010).
- Cancel-by-ID and the `byID` map (T-010).
- Last-trade-price tracking (T-010).
- Property test against random streams (T-011).

## Tests required

- `TestBook_InsertAtNewLevel`
- `TestBook_InsertAtExistingLevelIsFIFO` — insert A, B at same price; `level.Orders.Front()` is A
- `TestBook_BestBidIsMax` and `TestBook_BestAskIsMin`
- `TestBook_CancelLastOrderRemovesLevel`
- `TestBook_CancelMidLevelDecrementsTotal`
- `TestBook_TotalConsistencyAfterMixedOps`
- `TestBook_SnapshotDepth` — table-driven for depth 0, 1, 5, 1000
- `TestBook_PriceKeyCanonicalisation` — `"500000000"` and `"500000000.0"` map to same level
- `TestBook_RemoveFilledMaker` (or whatever the partial-fill helper is called)

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/engine/book/...` clean
- [ ] `go test ./internal/engine/book/...` green
- [ ] No imports outside stdlib + `internal/domain`, `internal/domain/decimal`, `github.com/google/btree`
- [ ] Public method godoc cites [§03](../system_design/03-order-book.md) where useful
