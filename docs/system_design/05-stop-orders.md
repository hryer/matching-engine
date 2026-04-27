# 05 — Stop Orders & Cascade

> Up: [README index](./README.md) | Prev: [§04 Matching Algorithm](./04-matching-algorithm.md) | Next: [§06 Concurrency & Determinism](./06-concurrency-and-determinism.md)

**Recommendation.** Two btree-backed indices and a flat `byID` map.

- `buyStops`: ordered **ascending** by `TriggerPrice`. Buy stops fire when `lastTradePrice ≥ trigger`, so the **smallest** triggers fire first → they sit at the head.
- `sellStops`: ordered **descending** by `TriggerPrice`. Sell stops fire when `lastTradePrice ≤ trigger`, so the **largest** triggers fire first → they sit at the head.
- `byID map[string]*Order`: O(1) cancel of armed orders.

**Why this is the boring choice.** Symmetric with the order book (same dep, same iteration pattern). "Head of structure = next to fire" makes the trigger sweep a trivial peek-pop loop with no scan-the-whole-book work.

---

## Structure

```mermaid
flowchart LR
    SB[StopBook]
    SB --> BUY[buys btree<br/>ascending by trigger]
    SB --> SELL[sells btree<br/>descending by trigger]
    SB --> BYID[byID map<br/>orderID → *Order]

    BUY --> B1["@99 (head — fires first)"]
    BUY --> B2[@100]
    BUY --> B3[@101]

    SELL --> S1["@103 (head — fires first)"]
    SELL --> S2[@102]
    SELL --> S3[@101]

    classDef structure fill:#e8f4ff,stroke:#2563eb,stroke-width:2px;
    classDef armed fill:#fff8e1,stroke:#d97706,stroke-width:1px;
    class SB,BUY,SELL,BYID structure;
    class B1,B2,B3,S1,S2,S3 armed;
```

The `byID` map references the same `*Order` as the btree entry. Cancel = delete from both in the same critical section.

---

## Trigger algorithm

`Engine.match` calls `updateLastTradePrice(p)` after every produced trade. That call drains any newly-triggered stops, in two stages: collect them deterministically, then re-submit them through `match`.

```text
func (e *Engine) updateLastTradePrice(p decimal.Decimal) {
    e.lastTradePrice = p
    e.drainTriggeredStops()
}

func (e *Engine) drainTriggeredStops() {
    var triggered []*Order

    for {
        if h := e.stops.buys.Min(); h != nil && h.TriggerPrice.LessOrEqual(e.lastTradePrice) {
            e.stops.buys.Delete(h); delete(e.stops.byID, h.ID)
            triggered = append(triggered, h); continue
        }
        if h := e.stops.sells.Min(); h != nil && h.TriggerPrice.GreaterOrEqual(e.lastTradePrice) {
            e.stops.sells.Delete(h); delete(e.stops.byID, h.ID)
            triggered = append(triggered, h); continue
        }
        break
    }

    // When multiple stops fire on the same lastTradePrice, fire by placement seq.
    sort.SliceStable(triggered, func(i, j int) bool { return triggered[i].seq < triggered[j].seq })

    for _, ord := range triggered {
        if ord.Type == Stop {
            ord.Type = Market   // stop becomes market
        } else {
            ord.Type = Limit    // stop_limit becomes limit
        }
        // Re-enter match. Each produced trade calls updateLastTradePrice, which
        // may drain more stops — the cascade unfolds naturally and terminates
        // because each stop is removed from byID before being re-submitted.
        e.appendTrades(e.match(ord))
    }
}
```

---

## Cascade termination

Each trade produced inside `match` calls `updateLastTradePrice`. That call drains any stops whose triggers the new price now satisfies; those stops are submitted through `match`, producing more trades, which call `updateLastTradePrice` again, and so on.

**Termination invariant:** each armed stop is removed from `byID` *before* it is re-submitted, so it can fire at most once. The total work in a cascade is bounded by `(trades produced) + (stops fired)`.

**Stack-depth caveat:** `match` is called recursively (well, indirectly, via `updateLastTradePrice → drainTriggeredStops → match`). Go stacks grow dynamically up to ~1 GB, so this is not a v1 risk. If a property test ever generates an adversarial cascade with 10⁵+ chained stops, convert `drainTriggeredStops` to an iterative engine-level FIFO queue: `match` pushes triggered stops, the outermost call drains them. Same algorithm, no stack growth. Stay with re-entry until measured otherwise.

---

## Stop-limit lifecycle (sequence diagram)

End-to-end for a stop-limit buy at trigger=101, limit=102:

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as HTTP
    participant E as Engine
    participant S as StopBook
    participant B as OrderBook
    participant T as TradeHistory

    C->>H: POST /orders {type:stop_limit, side:buy, trigger:101, price:102, qty:1}
    H->>E: Place(order)
    E->>E: trigger > lastTradePrice? yes
    E->>S: insert (status=Armed)
    E-->>H: order(armed), trades=[]
    H-->>C: 201 {order: armed}

    Note over E: time passes; another order trades at 101.5

    C->>H: POST /orders {type:limit, side:buy, price:101.5, qty:0.5}
    H->>E: Place(order)
    E->>B: match → trade @ 101.5
    E->>T: append trade
    E->>E: lastTradePrice = 101.5
    E->>S: drainTriggered() → buy stop @101 fires
    E->>S: pop, set type=Limit, status=Resting
    E->>B: match the now-limit order @ price=102
    B-->>E: 0 trades (no matching ask) → rests
    E->>B: insert (resting at 102 buy)

    Note over E: later, an ask appears at 102

    C->>H: POST /orders {type:limit, side:sell, price:102, qty:0.4}
    H->>E: Place(order)
    E->>B: match → trade @ 102 qty=0.4 (partial of the 1)
    E->>T: append trade
    E-->>H: order(filled), trades=[t]
    H-->>C: 201
    Note over B: original stop_limit now status=PartiallyFilled, 0.6 still resting
```

---

## Trigger-already-satisfied at placement

Per the brief: **reject**. Status `Rejected`, no trades, not stored in StopBook.

```text
on Place(stop / stop_limit):
    if Side == Buy  && trigger <= lastTradePrice → reject
    if Side == Sell && trigger >= lastTradePrice → reject
    else                                        → status = Armed, insert into StopBook
```

The brief permits an alternative — "treat as immediate market on placement" — but it's explicitly not chosen. Rationale: stops are protective by intent; silently crossing the spread surprises users.

---

## Cancel of armed stop

Look up `byID[orderID]`. Delete from the matching btree. Set `Status = Cancelled`. O(log n).

Critical: both deletions happen in the **same critical section**. If only `byID` were cleared, the btree entry would still fire when the trigger condition met. Property test: place, cancel, drive `lastTradePrice` past trigger, assert no fire.

---

## Why btree + map and not just a slice

Sorted slice + binary search would also work for stops (insert/delete shift cost is O(n) but stop counts are tiny). The btree was chosen for symmetry with [§03 OrderBook](./03-order-book.md) — same dependency, same iteration pattern, same cancel-by-ID indirection. One mental model for both books.

Next: [§06 Concurrency & Determinism →](./06-concurrency-and-determinism.md)
