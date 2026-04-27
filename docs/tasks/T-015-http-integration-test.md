# T-015 — HTTP integration test

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.0 h (±25%) |
| Owner | unassigned |
| Parallel batch | B5 |
| Blocks | T-017 |
| Blocked by | T-014 |
| Touches files | `internal/adapters/transport/http/handlers_test.go` |

## Goal

End-to-end HTTP integration test using `net/http/httptest`. Drives the full stack — router, handlers, validation, app.Service (with idempotency dedup), engine — through the wire format. Specified in [§09 Layer 3](../system_design/09-testing.md#layer-3--http-integration-test) and required by the brief.

## Context

This is the only test in the codebase that exercises the wire format. It is also the ticket where idempotency dedup is verified end-to-end — the engine ticket (T-010) and the service ticket (T-012) verify their layers; this test verifies they compose correctly through the HTTP layer.

The test file is in `package http_test` (or `package http` if you need access to unexported helpers — but the contract should be testable through the public API only). Using `package http_test` keeps it honest: the test exercises the same surface a real client uses.

## Acceptance criteria

- [ ] `handlers_test.go` boots a full stack via:
    ```go
    eng := engine.New(engine.Deps{
        Clock: clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
        IDs:   ids.NewMonotonic(),
        Publisher: inmem.NewRing(100),
        MaxOpenOrders: 1_000_000,
        MaxArmedStops: 100_000,
    })
    svc := app.NewService(eng)
    srv := httptest.NewServer(httpa.NewRouter(svc))
    defer srv.Close()
    ```
- [ ] Test cases (recommended one `t.Run(...)` subtest per scenario for clear output):
    - **Place limit buy** — `POST /orders` returns 201, body has `Order.Status == "resting"`, `trades: []`
    - **Place crossing limit sell** — `POST /orders` returns 201 with one trade; trade price equals the maker (resting) limit's price
    - **GET /trades** after the cross — returns one trade in the body
    - **GET /orderbook** after the cross — returns the residual on the book (or empty if fully filled)
    - **DELETE /orders/{id}** — cancels a resting order; returns 200 and `Order.Status == "cancelled"`
    - **DELETE on already-cancelled** — returns 409 `conflict`
    - **DELETE on unknown id** — returns 404 `not_found`
    - **Idempotent retry** — same `(user_id, client_order_id)`, same body, returns byte-identical response (compare full JSON body); a third unrelated `POST /orders` then returns an order with the next monotonic ID — proving the engine was called only once for the duplicates
    - **Idempotent retry, different body** — same `(user_id, client_order_id)`, different price; still returns the cached response (cache hit by key)
    - **Missing client_order_id** — `POST /orders` without the field returns 400 `validation` with `"client_order_id is required"`
    - **client_order_id too long** — 65 chars returns 400 `validation`
    - **client_order_id with non-printable byte** — `\x00` returned 400 `validation`
    - **user_id missing** — 400 `validation`
    - **user_id too long** — 129 chars returns 400 `validation`
    - **Body too large** — POST a body of 64 KB + 1 returns 413 `request_too_large`
    - **Quantity exceeds 10^15** — 400 `validation`, message `"quantity exceeds maximum 1000000000000000"`
    - **Empty book + market** — `POST /orders` market with no liquidity returns 201 with `Order.Status == "rejected"`, `trades: []` (NOT a 4xx — see [§08 error model](../system_design/08-http-api.md#error-model))
    - **Stop trigger already satisfied** — POST a stop where the brief's "trigger satisfied at placement" rule fires returns 201 with `Order.Status == "rejected"`
    - **Cap-hit (optional, depends on whether it's testable here without a custom-capped engine)** — out of scope for this test if it requires re-wiring; T-010 covers this
- [ ] Each subtest is self-contained (uses its own `httptest.NewServer` + fresh engine) so subtests do not share order IDs / dedup state
- [ ] All assertions use stdlib `testing` (no `testify`)
- [ ] `go test ./internal/adapters/transport/http/... -race` clean

## Implementation notes

- Use `http.Post(srv.URL+"/orders", "application/json", bytes.NewReader(body))` for placement. For DELETE, build a `http.NewRequest("DELETE", ...)` and use `http.DefaultClient.Do`.
- For `POST /orders`, build the body with `json.Marshal(PlaceOrderRequest{...})`. Read the response with `io.ReadAll` and unmarshal into `PlaceOrderResponse` for assertions. For byte-identical idempotency comparison, keep the raw response bytes from both retries and `bytes.Equal` them.
- `handlers_test.go` is `package http_test`; use the import path `httpa "matching-engine/internal/adapters/transport/http"` to disambiguate from stdlib `net/http`.
- The `Fake` clock advances explicitly; if the test wants distinct `created_at` between events, advance between requests (`fc.Advance(time.Second)`). For idempotency byte-comparison, the timestamp must be **identical** across the retried request — easy because the engine only sets the timestamp on the first call (the cached result returns the cached `*Order` with the original timestamp).
- The cap-hit test from T-010 already covers 429 paths in unit tests. Skipping it here is acceptable; if you want HTTP-level coverage, a `t.Run` constructs an engine with `MaxOpenOrders: 1` and submits two limit buys at distinct prices.
- Watch out for the **rejection caching** semantics: a stop with trigger already satisfied is rejected, status `Rejected`, and **cached** by the dedup layer. A retry returns the same rejection. This is correct per [§08 behaviour matrix](../system_design/08-http-api.md#behaviour-matrix) and worth a subtest (`TestHTTP_IdempotencyCachesRejection`).

## Out of scope

- Production handler implementation (T-014).
- Property tests (T-011).
- WebSocket / SSE / streaming (not in v1).
- Performance / load tests (not in v1; [§10](../system_design/10-hft-considerations.md) discusses what would be measured).

## Tests required

See acceptance criteria. Single `_test.go` file with one or more `Test*` functions, ideally organised as one `TestHTTP_E2E` parent with `t.Run` subtests for each scenario above. Goal: a reviewer reading the test file can see every brief-required behaviour exercised through the public HTTP surface.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `-race` clean
- [ ] No `testify`, no third-party assertion library
- [ ] Test file is self-contained (no shared global state with other tests)
- [ ] Touches-files list matches reality (only `handlers_test.go`)
