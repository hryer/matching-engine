# 07 — Decimal Arithmetic

> Up: [README index](./README.md) | Prev: [§06 Concurrency & Determinism](./06-concurrency-and-determinism.md) | Next: [§08 HTTP API](./08-http-api.md)

**Recommendation.** `github.com/shopspring/decimal` v1.4+. Wrap with a thin alias in `internal/domain/decimal/decimal.go` so the dep is swappable.

**Why this is the boring choice.** The brief explicitly bans `float64`. `shopspring/decimal` is the de facto Go decimal library; it round-trips to/from JSON as a string (matching the brief's wire format), and its `Cmp` method makes it easy to plug into the btree's `Less`.

---

## Honest take on integer minor units

For BTC/IDR you could store quantities in satoshi and prices in IDR, both as `int64`. **Real exchanges do this** — CME, Nasdaq's matching engines, Binance's core, dYdX v4 — because integer comparison is branch-free and the matcher hot-path is sub-microsecond.

We're not choosing integer minor units for this challenge, but the rejection is for **scope reasons, not technical merit**:

| Concern with integer minor units | Why it matters here |
|---|---|
| Scale config has to be encoded somewhere | Per-instrument `priceScale` / `qtyScale` — leaks into the API boundary, has to round-trip. For one pair this is one-line constants; for many pairs it's a config service. |
| `price × quantity` overflows `int64` | Need `int128` or `big.Int` for fee math and notional. Not in v1, but adds friction. |
| String parsing | `"500000000"` for IDR vs `"0.5"` for BTC — per-field scale config or two parsers. |
| The win is measured in nanoseconds | The matcher is not the bottleneck behind JSON parsing and HTTP. |

Reject for v1. Document as the path forward if sub-microsecond matching is ever required. See [§10](./10-hft-considerations.md) for the HFT counterfactual.

---

## Wrapper sketch

```go
// internal/domain/decimal/decimal.go
package decimal

import sd "github.com/shopspring/decimal"

type Decimal = sd.Decimal

var (
    Zero          = sd.Zero
    NewFromString = sd.NewFromString
)
```

If we ever swap the underlying library — to `cockroachdb/apd`, to integer minor units behind a `Decimal` facade, or to a custom int128 — only this file changes.

---

## Validation rules at the API boundary

These checks live in `internal/adapters/transport/http`, **not the engine**. The engine assumes its inputs are valid.

| Rule | Applies to | Failure |
|---|---|---|
| `price > 0` | `limit`, `stop_limit` | HTTP 400 |
| `quantity > 0` | all | HTTP 400 |
| `trigger_price > 0` | `stop`, `stop_limit` | HTTP 400 |
| Non-numeric string | all decimal fields | HTTP 400 |
| Precision ≤ 18 decimal places | all | HTTP 400 |
| `price` absent | `market`, `stop` | HTTP 400 if present |
| `trigger_price` absent | `limit`, `market` | HTTP 400 if present |

Bounding precision at 18 decimal places limits memory per `Decimal` value and keeps the `priceKey` canonicalisation (see [§03](./03-order-book.md)) cheap.

---

## Common pitfalls in matching code

These are the bugs the property tests exist to catch:

| Pitfall | Symptom | Fix |
|---|---|---|
| Comparing decimals with `==` | Two equal values that differ in trailing zeros compare unequal | Always use `.Cmp()` or `.Equal()` |
| `IsZero` confusion | After `qty.Sub(qty)`, `.IsZero()` is true; comparing to `decimal.Zero` with `==` is not | Use `.IsZero()` exclusively |
| Map keys from `String()` | `"500000000"` vs `"500000000.0"` — same value, different keys → phantom price levels | Canonicalise via `priceKey()` helper before insert/lookup |
| btree `Less` ambiguity | Equal prices comparing equal in btree comparator → undefined ordering | Store one `*PriceLevel` per price; FIFO is in the level's `list.List`, not in the btree |

---

## Why not `big.Float` or `big.Rat`?

`big.Float` has rounding modes — wrong default for money. `big.Rat` is exact but allocations and GC pressure are worse than `shopspring/decimal`, and its display formatting is awkward for JSON wire format. Both rejected.

Next: [§08 HTTP API →](./08-http-api.md)
