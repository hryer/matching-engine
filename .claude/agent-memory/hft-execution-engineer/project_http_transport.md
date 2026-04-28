---
name: HTTP transport layer — T-013/T-014/T-015 implementation notes
description: Key decisions, invariants, and integration-test findings for the HTTP transport package
type: project
---

T-013 delivered `internal/adapters/transport/http/` (package `http`) with DTOs, error helpers, and middleware. T-014 added `handlers.go` and `router.go`. T-015 added `handlers_test.go` (package `http_test`, 30 tests, race-clean).

**Key invariants to preserve:**

- `PlaceOrderRequest.ClientOrderID` has NO `omitempty` — empty string must round-trip so the handler can detect and reject it.
- `OrderDTO.Price` and `OrderDTO.TriggerPrice` carry `omitempty` — absent for market orders (price) and non-stop orders (trigger_price).
- `WriteError` uses `json.NewEncoder(w).Encode(...)` which appends a trailing newline; callers must not double-encode.
- `BodyLimit` wraps `r.Body` with `http.MaxBytesReader` but does NOT write the 413 response — the handler checks `errors.As(err, new(*http.MaxBytesError))` after `decoder.Decode`.
- `MaxBodyBytes = 64 << 10` (65536). Error message must be `"request body exceeds 65536 bytes"`.
- `levelsToDTO` and `tradesToDTO` use `make([]T, 0, ...)` — never nil — so JSON encodes as `[]` not `null`.
- `orderToDTO` uses Option B: `clientOrderID` is threaded from the request DTO, not from the engine result.

**T-015 integration test decisions pinned:**

- Cancel of already-cancelled order → **404 not_found** (not 409 conflict). Root cause: `engine.Cancel` removes the order from `e.byID` on first cancel; the second cancel finds neither byID nor stops and returns `ErrOrderNotFound`. `ErrAlreadyTerminal` is an invariant-violation guard that can never be reached through normal use. This is the correct branch per spec §T-015 scenario 6.
- Stop trigger-already-satisfied uses the `lastTradePrice == 0` boot property: any sell stop with positive trigger is immediately rejected. No trade setup required for scenario 18 or 19.
- Body-too-large test pads `user_id` to push total JSON past 65537 bytes; the `MaxBytesReader` trips on first `dec.Decode` read before field validation runs.

**Why:** T-014 blocks on this package; keeping DTOs free of `internal/domain` imports avoids circular-dependency risk.

**How to apply:** When reviewing handler changes, verify 413 detection, null-guard on slices, and the 404-not-409 cancel semantics documented above.
