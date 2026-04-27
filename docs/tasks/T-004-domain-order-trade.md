# T-004 — Order / Trade / Instrument domain types

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.75 h (±25%) |
| Owner | unassigned |
| Parallel batch | B1 |
| Blocks | T-008, T-009, T-010, T-013 |
| Blocked by | none |
| Touches files | `internal/domain/order.go`, `internal/domain/trade.go`, `internal/domain/instrument.go` |

## Goal

Implement the three core domain structs: `Order`, `Trade`, `Instrument`. Field-for-field as specified in [§02 Data Structures](../system_design/02-data-structures.md).

## Context

`Order` carries unexported fields (`seq`, `elem`, `level`) referenced by the order book. The `level *PriceLevel` back-pointer creates a circular reference between `domain` and `engine/book` if defined naively. Resolution: declare `level` as `any` (or `interface{}`) typed in the `domain` package — the order book casts back when accessing it. This keeps domain free of upstream imports. Same approach for `elem *list.Element`: import `container/list` only.

Documented field semantics in [§02](../system_design/02-data-structures.md#order):

| Field | Notes |
|---|---|
| `seq uint64` | monotonic placement sequence; FIFO tiebreaker; cascade ordering ([§05](../system_design/05-stop-orders.md)) |
| `elem *list.Element` | back-pointer for O(1) cancel from `container/list` |
| `level any` | back-pointer to the `*PriceLevel`; `any` to avoid import cycle. Engine/book casts on access |

## Acceptance criteria

- [ ] `internal/domain/order.go` declares `Order` struct with all exported fields per [§02](../system_design/02-data-structures.md#order) using `decimal.Decimal` from `internal/domain/decimal` and `Side`, `Type`, `Status` from `internal/domain` (T-001)
- [ ] `Order` carries unexported fields `seq uint64`, `elem *list.Element`, `level any`. Document in godoc that `level` is set by `engine/book` and back-cast there
- [ ] Provide accessor methods `Seq() uint64`, `Elem() *list.Element`, `Level() any`, plus setters `SetSeq(uint64)`, `SetElem(*list.Element)`, `SetLevel(any)` so other packages can set/read these without exposing the fields. Alternative acceptable design: keep them exported with leading capital but documented as "internal use." Pick one and apply consistently
- [ ] `internal/domain/trade.go` declares `Trade` struct per [§02](../system_design/02-data-structures.md#trade)
- [ ] `internal/domain/instrument.go` declares `type Instrument string` and the constant `BTCIDR Instrument = "BTC/IDR"`
- [ ] None of these files import any package from `internal/engine`, `internal/adapters`, or `internal/ports`
- [ ] `go vet ./internal/domain/...` and `go build ./internal/domain/...` clean

## Implementation notes

- For `level any`, the cast on the engine/book side will be `o.Level().(*PriceLevel)`. Document in a comment on the field/accessor that an `any` typed nil is acceptable when the order is not resting.
- Decimal fields default to the zero `Decimal` value — `decimal.Zero`. Both `Price` and `TriggerPrice` are zero unless the order type requires them ([§02](../system_design/02-data-structures.md#order) leaves Market with zero `Price`, Limit/Stop with zero `TriggerPrice`).
- `RemainingQuantity` starts equal to `Quantity`. The matcher (T-010) decrements it.
- `CreatedAt` is `time.Time`; populated by the engine via the `Clock` port (T-003). Domain does not call `time.Now()`.
- `Trade.TakerSide` echoes the taker's `Side` for downstream consumers; the brief shows it in the example payload ([§08 example](../system_design/08-http-api.md#example-payloads)).
- No JSON tags on `Order` or `Trade` — wire DTOs are separate types in `internal/adapters/transport/http/dto.go` (T-013). Domain types stay JSON-agnostic.

## Out of scope

- `OrderBook`, `PriceLevel`, `StopBook` (T-008, T-009).
- DTO/JSON shape for HTTP responses (T-013).
- The `client_order_id` field on `Order` — it's not part of the engine's domain model; it lives only in the request DTO and the `app.Service` dedup map ([§08 Idempotency](../system_design/08-http-api.md#idempotency)).

## Tests required

None at this layer (no behaviour). Compile-time correctness is the test.

A smoke test of struct literal construction is acceptable but not required:

```go
func TestOrderConstructionCompiles(t *testing.T) {
    var _ = domain.Order{ID: "o-1", Side: domain.Buy, Type: domain.Limit, ...}
}
```

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/domain/...` clean
- [ ] No imports outside stdlib + `internal/domain/decimal`
- [ ] Field comments cite [§02](../system_design/02-data-structures.md) for any non-obvious field
