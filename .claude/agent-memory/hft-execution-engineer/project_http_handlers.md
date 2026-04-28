---
name: HTTP handlers — T-014 implementation notes
description: Design decisions, invariants, and gotchas for handlers.go and router.go
type: project
---

T-014 shipped: `handlers.go` + `router.go` in `internal/adapters/transport/http/`.

**Key decisions locked in:**

- Time format: `time.RFC3339` (not RFC3339Nano). Applied consistently in `orderToDTO` and `tradesToDTO`.
- `client_order_id` echo: Option B — handler threads `req.ClientOrderID` directly into `orderToDTO(result.Order, req.ClientOrderID)`. No service-layer change. Correct on dedup hits because the cached result's key equals the incoming clientOrderID by construction.
- Cancel response: `orderToDTO(order, "")` — empty client_order_id is acceptable for cancel; the engine `*Order` doesn't carry it and the spec doesn't require it on DELETE.
- `parseIntParam` returns `(0, false)` on error and writes a 400 itself; callers check `ok` before using the value. Negative values and non-integer strings both produce 400.
- `maxDecimalValue` is a package-level `decimal.Decimal` initialized once via `decimal.NewFromInt(1_000_000_000_000_000)` — avoids repeated allocation on the hot path.
- ASCII printable check: `range []byte(s)`, not `range s` — byte-level enforcement per spec.
- Precision check: `d.Exponent() < -18` — shopspring's exponent is negative for fractional places.
- Empty slices: `tradesToDTO` and `levelsToDTO` both use `make([]T, 0, len(in))` — guarantees JSON `[]` not `null`.
- `BodyLimit` applied only to `POST /orders` in the router; GET/DELETE have no body.
- Imports: `internal/engine` is imported in `handlers.go` only for the four sentinel errors (`ErrTooManyOrders`, `ErrTooManyStops`, `ErrOrderNotFound`, `ErrAlreadyTerminal`) — no engine mutex or engine types are used directly.
- `validatePlaceRequest` lives in `handlers.go` (under 120 lines). T-015 owns `handlers_test.go`; optional unit tests go in `handlers_validate_test.go`.

**Why:** T-014 is the wire-format glue between the HTTP boundary and `app.Service`. Correctness over cleverness — all validation is explicit and ordered, no reflection or tag-driven magic.

**How to apply:** When T-015 or T-016 need to know handler contracts, read `handlers.go` directly. The validation order (decode → 0a/0b → parse decimals → enums → type-specific → bounds → precision) is the canonical pipeline.
