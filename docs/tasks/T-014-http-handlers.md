# T-014 — HTTP handlers + validation pipeline + router

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 1.25 h (±25%) |
| Owner | unassigned |
| Parallel batch | B5 |
| Blocks | T-015, T-016 |
| Blocked by | T-012, T-013 |
| Touches files | `internal/adapters/transport/http/handlers.go`, `internal/adapters/transport/http/router.go` |

## Goal

Implement the four HTTP handlers (`POST /orders`, `DELETE /orders/{id}`, `GET /orderbook`, `GET /trades`), the validation pipeline, the router that wires them with body-limit middleware, and the engine-error → HTTP-status mapping. Specified in [§08 HTTP API](../system_design/08-http-api.md).

## Context

This is the wire-format glue. Handlers parse JSON outside the engine lock, call `app.Service`, marshal the response. The HTTP layer never imports `internal/engine` — it imports `internal/app` and `internal/domain`. The engine errors that reach this layer arrive via `app.Service.Place` returning whatever the engine returned.

### Validation pipeline ([§08 Validation pipeline](../system_design/08-http-api.md#validation-pipeline))

In order, on `POST /orders`:

0a. `client_order_id` precondition — present, length 1..64, ASCII printable (0x20..0x7E). Error → 400 `validation` with the specific message.
0b. `user_id` precondition — present, length 1..128, ASCII printable.
0c. Body size — handled by the middleware (T-013). Decoder returns `*http.MaxBytesError` → 413.
1. Decode JSON into `PlaceOrderRequest`. JSON syntax error or unknown field → 400.
2. Parse decimals (`Quantity`, `Price` if present, `TriggerPrice` if present) via `decimal.NewFromString`.
3. Validate enums: `side ∈ {buy, sell}`; `type ∈ {limit, market, stop, stop_limit}`.
4. Type-specific field constraints:
    - `limit`: `price > 0`, `trigger_price` absent
    - `market`: `price` and `trigger_price` both absent
    - `stop`: `trigger_price > 0`, `price` absent
    - `stop_limit`: `price > 0`, `trigger_price > 0`
5. Numeric upper bounds: `quantity > 0` and `<= 10^15`; `price <= 10^15` (when present); `trigger_price <= 10^15` (when present). Failures → 400 with the specific message from [§08](../system_design/08-http-api.md#validation-pipeline).
6. Decimal precision ≤ 18 places ([§07](../system_design/07-decimal-arithmetic.md#validation-rules-at-the-api-boundary)). Use `d.Exponent() < -18` test (negative exponent = decimals).
7. Convert to `app.PlaceCommand` and pass to `service.Place(cmd)`.

### Engine-error → HTTP mapping

| From `service.Place` | HTTP |
|---|---|
| `nil` (success), `Order.Status == Rejected` | 201, `PlaceOrderResponse` with `trades: []` |
| `nil` (success), `Order.Status` other | 201, `PlaceOrderResponse` |
| `engine.ErrTooManyOrders` | 429 `too_many_orders` |
| `engine.ErrTooManyStops` | 429 `too_many_stops` |
| any other error | 500 `internal` (logged via `log.Printf`) |

For `service.Cancel(id)`:

| From `service.Cancel` | HTTP |
|---|---|
| `nil` | 200, `CancelOrderResponse` with `Order.Status == Cancelled` |
| `engine.ErrOrderNotFound` | 404 `not_found` |
| `engine.ErrAlreadyTerminal` | 409 `conflict` |
| any other error | 500 `internal` |

`GET /orderbook?depth=N` and `GET /trades?limit=N`:
- Default `depth=10`, `limit=50`. Cap both at 1000. Negative or non-integer → 400 `validation`.
- Always 200 on success (empty book / no trades returns `{"bids":[],"asks":[]}` or `{"trades":[]}`).

### Router

```go
func NewRouter(svc *app.Service) http.Handler {
    mux := http.NewServeMux()
    mux.Handle("POST /orders",          BodyLimit(http.HandlerFunc(handlePlace(svc))))
    mux.HandleFunc("DELETE /orders/{id}", handleCancel(svc))
    mux.HandleFunc("GET /orderbook",    handleSnapshot(svc))
    mux.HandleFunc("GET /trades",       handleTrades(svc))
    return mux
}
```

Go 1.22+ patterns supply `r.PathValue("id")` for the cancel handler.

### `client_order_id` echo on the response

`OrderDTO.ClientOrderID` must echo the value the client sent. The engine's `*Order` doesn't carry it. Options:

- A: have `app.Service.Place` return both the `engine.PlaceResult` and the `clientOrderID` (since the service has it). Cleanest. Requires augmenting the service return type, e.g. `func (s *Service) Place(cmd PlaceCommand) (PlaceResult, error)` where `app.PlaceResult` wraps `engine.PlaceResult` plus the client_order_id.
- B: have the handler thread `clientOrderID` from the request DTO directly into the response DTO (since the handler knows it). Even simpler — no service-side change. Drawback: on dedup hit, the handler returns the cached `engine.PlaceResult` plus the **new** request's `clientOrderID`, which by definition equals the cached one (or it wouldn't have been a dedup hit). So this is also correct.

**Recommended: Option B.** It avoids enlarging the service return type. The handler holds the `clientOrderID` and stamps it into `OrderDTO.ClientOrderID` after receiving the result.

### Concurrency at the HTTP layer ([§08 Concurrency at the HTTP layer](../system_design/08-http-api.md#concurrency-at-the-http-layer))

`http.Server` is goroutine-per-request. Decode happens before the engine call; encode after. The dedup mutex (in `app.Service`) and engine mutex serialise the actual state mutation. Handlers do **not** spawn fire-and-forget goroutines.

## Acceptance criteria

- [ ] `handlers.go` defines four handler functions, each closing over a `*app.Service`
- [ ] `router.go` exports `NewRouter(svc *app.Service) http.Handler` returning a `*http.ServeMux` with the four routes wired and body-limit middleware on `POST /orders`
- [ ] `POST /orders` runs the validation pipeline above; on validation failure, calls `WriteError(w, 400, CodeValidation, ...)` with the specific message from [§08](../system_design/08-http-api.md#validation-pipeline)
- [ ] On body-too-large (`*http.MaxBytesError`), `WriteError(w, 413, CodeRequestTooLarge, "request body exceeds 65536 bytes")`
- [ ] On `service.Place` returning `engine.ErrTooManyOrders` or `engine.ErrTooManyStops`, status 429 with the specific code (`too_many_orders` / `too_many_stops`)
- [ ] On business reject (`Order.Status == Rejected`), status 201 with the order in body and empty `trades`
- [ ] `OrderDTO.ClientOrderID` is populated from the request DTO (or via `app.Service` if Option A was chosen)
- [ ] `DELETE /orders/{id}` returns 200 + canceled order, 404, or 409 per the table above
- [ ] `GET /orderbook?depth=N` defaults to 10, caps at 1000. Returns `SnapshotResponse{}`. Empty book returns `{"bids":[],"asks":[]}` (not `null`)
- [ ] `GET /trades?limit=N` defaults to 50, caps at 1000. Returns `TradesResponse{}`
- [ ] Decimal-to-string conversions use `decimal.Decimal.String()`. Time-to-string uses `time.Time.UTC().Format(time.RFC3339Nano)` (or `time.RFC3339`; pick one and apply consistently across `OrderDTO.CreatedAt` and `TradeDTO.CreatedAt`)
- [ ] Handlers do not import `internal/engine`. They import `internal/app`, `internal/domain`, `internal/domain/decimal`. Engine errors are imported by name from the `engine` package — that one import is necessary for the `errors.Is` checks
- [ ] JSON decode uses `json.Decoder` with `decoder.DisallowUnknownFields()` so unknown fields produce 400 (defensive — required for the dedup contract: a retry that adds an extra field should still hit cache, but the validation says no, so 400 instead, which is a correct strict behaviour)
- [ ] `go vet ./internal/adapters/transport/http/...` clean

## Implementation notes

- Handler skeleton:
    ```go
    func handlePlace(svc *app.Service) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
            var req PlaceOrderRequest
            dec := json.NewDecoder(r.Body)
            dec.DisallowUnknownFields()
            if err := dec.Decode(&req); err != nil {
                var maxErr *http.MaxBytesError
                if errors.As(err, &maxErr) {
                    WriteError(w, 413, CodeRequestTooLarge, "request body exceeds 65536 bytes")
                    return
                }
                WriteError(w, 400, CodeValidation, err.Error())
                return
            }
            cmd, msg, ok := validatePlaceRequest(req) // returns parsed app.PlaceCommand
            if !ok {
                WriteError(w, 400, CodeValidation, msg)
                return
            }
            result, err := svc.Place(cmd)
            if err != nil {
                switch {
                case errors.Is(err, engine.ErrTooManyOrders):
                    WriteError(w, 429, CodeTooManyOrders, err.Error())
                case errors.Is(err, engine.ErrTooManyStops):
                    WriteError(w, 429, CodeTooManyStops, err.Error())
                default:
                    log.Printf("place: %v", err)
                    WriteError(w, 500, CodeInternal, "internal error")
                }
                return
            }
            resp := PlaceOrderResponse{
                Order:  orderToDTO(result.Order, req.ClientOrderID),
                Trades: tradesToDTO(result.Trades),
            }
            writeJSON(w, 201, resp)
        }
    }
    ```
- `validatePlaceRequest(req PlaceOrderRequest) (app.PlaceCommand, string, bool)` is the pipeline. Keep it in `handlers.go` for v1; refactor to a separate `validate.go` if it grows past ~120 lines.
- ASCII-printable check: `for _, b := range s { if b < 0x20 || b > 0x7E { return false } }`. (range over a `string` yields runes, but we want bytes — use `range []byte(s)` or index `s[i]`.)
- Decimal precision check: `d.Exponent() < -18` indicates more than 18 fractional digits. (Trailing zeros after canonicalisation may already be stripped; verify with the chosen decimal library.)
- `client_order_id` validation runs **before** JSON parse can fully complete? No — JSON parse must complete first to access the field. Order: decode → check `ClientOrderID` field on the parsed struct. The validation message in [§08 step 0a](../system_design/08-http-api.md#validation-pipeline) implies this ordering: "missing or empty" is checked after decode.
- Empty `[]LevelDTO` and `[]TradeDTO` must serialise as `[]`, not `null`. In Go, an explicitly-allocated empty slice (`make([]LevelDTO, 0)`) serialises as `[]`; a `nil` slice serialises as `null`. Initialise empty slices in `orderbookToDTO` / `tradesToDTO`.
- `time.Time.Format(time.RFC3339)` truncates sub-second precision. The test clock returns the same instant repeatedly; if you want fractional precision in the output, use `time.RFC3339Nano`. Either is fine as long as one is chosen consistently. Recommend `time.RFC3339` for the human-readable test output unless something downstream depends on nanos.
- Don't apply `BodyLimit` to GET / DELETE — those don't have request bodies. Apply only to `POST /orders`.

## Out of scope

- The integration test (T-015).
- Composition root / `cmd/server` (T-016).
- Cache-control or rate-limit headers (deferred; not in v1).
- TLS, auth, observability (deferred per [`ARCHITECT_PLAN.md` §2](../system_design/ARCHITECT_PLAN.md#2-constraints-and-explicit-non-goals)).

## Tests required

This ticket does **not** ship handler tests. The integration test in T-015 covers the handler contracts end-to-end. Unit tests for `validatePlaceRequest` are acceptable but optional — if you write them, put them in `handlers_validate_test.go` to keep them out of T-015's `handlers_test.go` (parallel-safety).

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/adapters/transport/http/...` clean
- [ ] `go build ./internal/adapters/transport/http/...` clean
- [ ] No imports outside stdlib + `internal/app`, `internal/engine` (errors only), `internal/domain`, `internal/domain/decimal`
- [ ] Lock-discipline / engine-mutex exposure: handlers do **not** acquire any engine mutex directly; only via `svc.Place / Cancel / Snapshot / Trades`
