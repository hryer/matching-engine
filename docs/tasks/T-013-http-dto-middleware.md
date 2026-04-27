# T-013 — HTTP DTOs + body-limit middleware + error response shape

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.75 h (±25%) |
| Owner | unassigned |
| Parallel batch | B4 |
| Blocks | T-014 |
| Blocked by | T-002, T-004 |
| Touches files | `internal/adapters/transport/http/dto.go`, `internal/adapters/transport/http/errors.go`, `internal/adapters/transport/http/middleware.go` |

## Goal

Define the HTTP wire shapes (request/response DTOs and the error model), plus the `bodyLimit` middleware that enforces the 64 KB request-body cap. Specified in [§08 HTTP API](../system_design/08-http-api.md), specifically [DTOs](../system_design/08-http-api.md#dtos), [Error model](../system_design/08-http-api.md#error-model), [Validation pipeline](../system_design/08-http-api.md#validation-pipeline), and [Example payloads](../system_design/08-http-api.md#example-payloads).

## Context

The HTTP layer is the only place in the codebase that serialises domain types to JSON. Domain types have no JSON tags ([T-004](./T-004-domain-order-trade.md)); the conversion happens in DTOs.

### DTO shape ([§08 DTOs](../system_design/08-http-api.md#dtos))

```go
type PlaceOrderRequest struct {
    UserID        string `json:"user_id"`
    ClientOrderID string `json:"client_order_id"` // REQUIRED; no omitempty
    Side          string `json:"side"`
    Type          string `json:"type"`
    Price         string `json:"price,omitempty"`
    TriggerPrice  string `json:"trigger_price,omitempty"`
    Quantity      string `json:"quantity"`
}

type OrderDTO struct {
    ID                string `json:"id"`
    UserID            string `json:"user_id"`
    ClientOrderID     string `json:"client_order_id"`
    Side              string `json:"side"`
    Type              string `json:"type"`
    Price             string `json:"price,omitempty"`
    TriggerPrice      string `json:"trigger_price,omitempty"`
    Quantity          string `json:"quantity"`
    RemainingQuantity string `json:"remaining_quantity"`
    Status            string `json:"status"`
    CreatedAt         string `json:"created_at"`
}

type TradeDTO struct {
    ID           string `json:"id"`
    TakerOrderID string `json:"taker_order_id"`
    MakerOrderID string `json:"maker_order_id"`
    Price        string `json:"price"`
    Quantity     string `json:"quantity"`
    TakerSide    string `json:"taker_side"`
    CreatedAt    string `json:"created_at"`
}

type PlaceOrderResponse struct {
    Order  OrderDTO   `json:"order"`
    Trades []TradeDTO `json:"trades"`
}

type CancelOrderResponse struct {
    Order OrderDTO `json:"order"`
}

type SnapshotResponse struct {
    Bids []LevelDTO `json:"bids"`
    Asks []LevelDTO `json:"asks"`
}
type LevelDTO struct {
    Price    string `json:"price"`
    Quantity string `json:"quantity"`
}

type TradesResponse struct {
    Trades []TradeDTO `json:"trades"`
}

type ErrorResponse struct {
    Error string `json:"error"`
    Code  string `json:"code"` // "validation" | "not_found" | "conflict" | "request_too_large" | "too_many_orders" | "too_many_stops" | "internal"
}
```

`OrderDTO.ClientOrderID` is populated from the request DTO at the handler boundary (the `app.Service` layer holds it; the engine-side `*Order` does not). T-014 wires this — this ticket only declares the field.

`OrderDTO.Price`, `OrderDTO.TriggerPrice`, `Quantity`, etc., are JSON strings (the brief's wire format); the handler converts `decimal.Decimal` via `.String()`. Field absent for Market orders' `Price` and Limit/Market orders' `TriggerPrice` per [§07 validation](../system_design/07-decimal-arithmetic.md#validation-rules-at-the-api-boundary).

`OrderDTO.CreatedAt` is RFC 3339 / ISO 8601 (`time.Time.Format(time.RFC3339Nano)` or simpler `.UTC().Format(time.RFC3339)`). Match the example payload in [§08](../system_design/08-http-api.md#example-payloads).

### Error helpers ([§08 Error model](../system_design/08-http-api.md#error-model))

```go
const (
    CodeValidation       = "validation"
    CodeNotFound         = "not_found"
    CodeConflict         = "conflict"
    CodeRequestTooLarge  = "request_too_large"
    CodeTooManyOrders    = "too_many_orders"
    CodeTooManyStops     = "too_many_stops"
    CodeInternal         = "internal"
)

func WriteError(w http.ResponseWriter, status int, code, msg string)
```

`WriteError` sets `Content-Type: application/json`, writes the status, and writes `{"error":"...","code":"..."}`. Handlers (T-014) use this for every error path.

### Body-limit middleware ([§08 Validation pipeline step 0c](../system_design/08-http-api.md#validation-pipeline), [`ARCHITECT_PLAN.md` §4](../system_design/ARCHITECT_PLAN.md#4-risk-register))

```go
const MaxBodyBytes = 64 << 10 // 64 KB

func BodyLimit(next http.Handler) http.Handler
```

Wraps the request body in `http.MaxBytesReader(w, r.Body, MaxBodyBytes)`. When a downstream `json.Decode` fails with `*http.MaxBytesError`, the handler responds **413** `request_too_large` with message `"request body exceeds 65536 bytes"`. Detection of `*http.MaxBytesError` happens in T-014's handlers; this middleware just installs the reader.

The middleware is applied to POST routes only (GET / DELETE have no body). The composition is up to T-014; this ticket exposes the middleware function and the constant.

## Acceptance criteria

- [ ] `dto.go` declares the request, response, and helper structs above with the exact JSON tags shown
- [ ] `OrderDTO.Price` and `OrderDTO.TriggerPrice` use `,omitempty` so they vanish for Market / Limit-without-trigger orders
- [ ] `errors.go` declares the error code constants and the `WriteError` helper
- [ ] `WriteError` sets content type, status code, and writes the JSON body. Errors from the encoder are logged via `log.Printf` (best effort) — there's nothing else to do at that point
- [ ] `middleware.go` declares `MaxBodyBytes = 64 << 10` and `BodyLimit` middleware
- [ ] `BodyLimit` wraps `r.Body` with `http.MaxBytesReader(w, r.Body, MaxBodyBytes)` and calls `next.ServeHTTP(w, r)`
- [ ] No DTO has unknown / extra fields. Disallowing-unknown-fields on the request decoder is the handler's job (T-014); DTOs themselves are tolerant
- [ ] `go vet ./internal/adapters/transport/http/...` and `go build ./internal/adapters/transport/http/...` clean

## Implementation notes

- DTOs do **not** import `internal/domain` directly to avoid leaking JSON tags onto domain types. Instead, T-014 will write conversion functions (`func orderToDTO(o *domain.Order, clientOrderID string) OrderDTO`). This ticket can include those conversion stubs if convenient — but they belong to T-014's responsibility surface. Keep `dto.go` to type declarations only; conversions live in `handlers.go` or a small `dto_convert.go` (T-014).
- `WriteError` signature trade-off: `(w, status, code, msg string)` is simple. Alternative `(w, status, errResp ErrorResponse)` is overkill. Stick with the simple one.
- The error message shape is the brief's: a string `error` field plus a `code` enum. No nested error arrays, no `details` field. Keep it minimal.
- `BodyLimit` must be applied **before** the JSON decoder; it sets up the reader. The 413 detection happens when `decoder.Decode(&req)` returns a `*http.MaxBytesError`. T-014's handlers must check `errors.As(err, new(*http.MaxBytesError))` and call `WriteError(w, 413, CodeRequestTooLarge, "request body exceeds 65536 bytes")`.
- Do not implement the full validation pipeline here. T-014 owns validation. This ticket is shape only.
- Do not install the middleware on `http.ServeMux` here. T-014's router does that.
- `http.ServeMux` and Go 1.22+ pattern routing handle `DELETE /orders/{id}`. No third-party router needed ([§08 recommendation](../system_design/08-http-api.md)).

## Out of scope

- Validation logic (T-014).
- Handler implementations (T-014).
- Conversion functions from domain types to DTOs (T-014, though placement here is acceptable if it doesn't bloat the file).
- Integration test (T-015).

## Tests required

Light tests, since most behaviour is type declarations:

- `TestErrorResponse_JSONShape` — marshal an `ErrorResponse{Error: "x", Code: CodeValidation}`; assert the bytes match the brief's example shape (`{"error":"x","code":"validation"}`)
- `TestOrderDTO_OmitEmpty` — marshal a Market `OrderDTO` with empty `Price`; assert no `"price"` key in output
- `TestBodyLimit_RejectsOversizedBody` — `httptest.NewRecorder` with a body of 64 KB + 1; pass through middleware to a no-op handler that tries to read the body; the read returns `*http.MaxBytesError`. (This test is borderline integration; the simpler version is to assert the middleware wraps `r.Body` with a `MaxBytesReader` instance — but that's checking implementation, not behaviour. Recommended: write the integration-flavoured test even though it crosses into T-014's handler territory; it's small.)
- `TestWriteError_StatusAndContentType` — `WriteError(rec, 400, "validation", "bad")`; assert status, content-type, body

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/adapters/transport/http/...` clean
- [ ] No imports outside stdlib + `internal/domain/decimal` (used inside conversion if conversions live here; otherwise even decimal not needed)
- [ ] Touches-files list matches reality
