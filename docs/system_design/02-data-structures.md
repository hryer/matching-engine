# 02 — Data Structures

> Up: [README index](./README.md) | Prev: [§01 Architecture](./01-architecture.md) | Next: [§03 Order Book](./03-order-book.md)

**Recommendation.** Plain structs in `internal/domain`. `decimal.Decimal` from `github.com/shopspring/decimal` (see [§07](./07-decimal-arithmetic.md)). IDs are strings shaped `"o-<n>"` and `"t-<n>"` from a monotonic counter.

**Why this is the boring choice.** Matches the brief's example payload literally. Avoids reinventing decimal. Stays JSON-friendly without per-field marshaller noise.

---

## Enums

`uint8` for cache friendliness; custom JSON marshalling for the wire format the brief shows.

```go
// internal/domain/enums.go
package domain

type Side uint8
const (
    Buy  Side = iota // 0
    Sell             // 1
)

type Type uint8
const (
    Limit Type = iota
    Market
    Stop
    StopLimit
)

type Status uint8
const (
    StatusArmed Status = iota
    StatusResting
    StatusPartiallyFilled
    StatusFilled
    StatusCancelled
    StatusRejected
)
```

Each enum has `MarshalJSON` / `UnmarshalJSON` returning the strings the brief uses (`"buy"`, `"limit"`, `"partially_filled"`, `"stop_limit"`, etc.).

---

## Order

```go
// internal/domain/order.go
type Order struct {
    ID                string
    UserID            string
    Side              Side
    Type              Type
    Price             decimal.Decimal // zero for Market
    TriggerPrice      decimal.Decimal // zero unless Stop / StopLimit
    Quantity          decimal.Decimal // original
    RemainingQuantity decimal.Decimal
    Status            Status
    CreatedAt         time.Time

    // Internal — not serialized:
    seq   uint64        // monotonic placement seq, FIFO tie-breaker
    elem  *list.Element // back-pointer into PriceLevel.Orders (nil if not resting)
    level *PriceLevel   // back-pointer for Total maintenance and removal
}
```

Why each unexported field exists:

| Field | Purpose |
|---|---|
| `seq` | Two orders sharing the same `CreatedAt` (fake clock returning identical instants in tests) still need a deterministic FIFO order. `seq` is the tie-breaker. Also drives stop-cascade ordering — see [§05](./05-stop-orders.md). |
| `elem` | Back-pointer into `container/list.List` so `Cancel` is O(1) list removal, not O(n) scan. |
| `level` | Back-pointer to `PriceLevel` so `Cancel` can decrement `level.Total` in O(1). |

---

## Trade

```go
// internal/domain/trade.go
type Trade struct {
    ID           string
    TakerOrderID string
    MakerOrderID string
    Price        decimal.Decimal
    Quantity     decimal.Decimal
    TakerSide    Side
    CreatedAt    time.Time
}
```

Trade price is the **maker's resting price** — see [§04](./04-matching-algorithm.md#trade-price-decision).

---

## OrderBook (sketch — full in [§03](./03-order-book.md))

```go
type OrderBook struct {
    bids *side // descending by price (best bid first)
    asks *side // ascending by price (best ask first)
}

type side struct {
    levels map[string]*PriceLevel // key = canonical decimal string
    index  *btree.BTreeG[*PriceLevel]
    isBid  bool
}

type PriceLevel struct {
    Price  decimal.Decimal
    Orders *list.List      // FIFO of *Order
    Total  decimal.Decimal // running sum of RemainingQuantity (snapshot O(1) per level)
}
```

`Total` is maintained incrementally on every insert / fill / cancel so a depth-N snapshot is O(N), not O(N · level_size).

---

## StopBook (sketch — full in [§05](./05-stop-orders.md))

```go
type StopBook struct {
    buys  *btree.BTreeG[*Order] // ascending by TriggerPrice
    sells *btree.BTreeG[*Order] // descending by TriggerPrice
    byID  map[string]*Order     // O(1) cancel
}
```

---

## Trade history

A bounded ring buffer holding the last **10,000** trades. Older trades are silently dropped. The brief is in-memory only; an unbounded slice grows linearly with traffic.

The buffer lives behind `ports.EventPublisher` — the engine `Publish(trade)`s and the in-memory adapter holds the ring. See [§01](./01-architecture.md). Swapping for Kafka in v2 is a no-engine-change adapter swap.

---

## ID format

| Entity | Format | Source |
|---|---|---|
| Order | `"o-<n>"` | monotonic `uint64` counter, increments on every `Place` |
| Trade | `"t-<n>"` | separate monotonic `uint64` counter |

Both counters live behind `ports.IDGenerator` and start at 1 on every server start. There is no persistence in v1, so collisions across restarts are not a concern.

uint64 wraps after ~1.8 × 10¹⁹ — never in practice. Documented; no code guard.

---

## Instrument

```go
// internal/domain/instrument.go
type Instrument string

const BTCIDR Instrument = "BTC/IDR"
```

A type, not a routing key. Multi-pair routing in v2 turns `app.Service` into `map[Instrument]*engine.Engine` — see [§11](./11-production-evolution.md). v1 holds a single engine; the `Instrument` type just keeps the future seam visible.

Next: [§03 Order Book →](./03-order-book.md)
