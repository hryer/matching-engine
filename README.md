# Matching Engine

An in-memory, single-pair (BTC/IDR) matching engine in Go. Supports four order types — limit, market, stop, stop-limit — with FIFO price-time priority and self-trade prevention. Exposes four HTTP endpoints: `POST /orders`, `DELETE /orders/{id}`, `GET /orderbook`, `GET /trades`. State is process-local; restart wipes it. The design docs live in [`docs/system_design/README.md`](docs/system_design/README.md).

---

## Run

```sh
make run
# or
go run ./cmd/server
```

Place a limit buy order:

```sh
curl -X POST http://localhost:8080/orders \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "u-123",
    "client_order_id": "buy-btc-2026-04-20-001",
    "side": "buy",
    "type": "limit",
    "price": "500000000",
    "quantity": "0.5"
  }'
```

`client_order_id` is required on every `POST /orders` — it is the idempotency key. Missing or duplicate keys return a cached result without re-invoking the engine. See [§08 Idempotency](docs/system_design/08-http-api.md#idempotency).

---

## Test

```sh
# full suite
go test ./...

# with race detector, 10 runs each
go test ./... -race -count=10

# determinism replay — 1000 identical replays must produce byte-identical output
go test ./internal/engine -run TestDeterministicReplay -count=1000
```

---

## Layout

```
matching-engine/
├── cmd/server/          # composition root — wires ports, adapters, engine, HTTP server
├── internal/
│   ├── domain/          # entities and value objects (Order, Trade, Side, Type, Status, decimal)
│   ├── engine/          # pure matcher — no transport, no I/O; book/ and stops/ sub-packages
│   ├── ports/           # interfaces only (Clock, IDGenerator, EventPublisher)
│   ├── adapters/        # concrete port implementations (http handlers, inmem publisher, clock, ids)
│   └── app/             # application service — idempotency dedup, thin pass-through to engine
└── docs/system_design/  # full design (§01–§11) + architect plan
```

Full file tree and dependency diagram: [§01 Architecture](docs/system_design/01-architecture.md).

---

## Decisions

- Module layout is hexagonal (ports + adapters); engine never imports a transport or `net/http`.
- Order book: `map[priceKey]*PriceLevel` + btree + `container/list` per level; O(1) lookup, O(log n) best-price.
- Stop book: twin btrees (ascending buy, descending sell) + `byID` map for O(1) cancel.
- Self-match policy: cancel-newest; preserves resting liquidity.
- Trade price is the maker's resting price, not the taker's limit.
- Stop placed with trigger already satisfied is rejected, not immediately fired.
- Stop cascade fires in ascending placement sequence (`seq`), deterministic regardless of btree iteration order.
- Concurrency: single `sync.Mutex` on `Engine`; no goroutines spawned by the engine.
- Decimal arithmetic: `github.com/shopspring/decimal`; decimal fields are JSON strings on the wire.
- Business-rejected orders (market with no liquidity, trigger already satisfied) return HTTP 201 with `status: rejected` in the body, not a 4xx.
- `POST /orders` requires `client_order_id` (1–64 ASCII printable); deduped at `app.Service`, wiped on restart.
- Trade history: ring buffer, last 10,000 trades.

Full rationale for each: [docs/system_design/README.md — Decision summary](docs/system_design/README.md#decision-summary).

---

## What's not in v1

- No persistence (no WAL, no snapshots, no replay-from-disk on startup)
- No auth, rate limiting, or per-user balances / KYC
- No multiple pairs (BTC/IDR only)
- No WebSocket, gRPC, FIX
- No Kafka, NATS, or external publisher
- No IOC / FOK / iceberg / post-only / trailing-stop / hidden / peg
- No fees, no maker/taker schedule
- No admin endpoints
- No observability beyond stdlib `log` for errors and panics
- No graceful state migration — restart wipes state

---

## Brief

The original challenge: [`docs/challenges/trading-engine.pdf`](docs/challenges/trading-engine.pdf).
