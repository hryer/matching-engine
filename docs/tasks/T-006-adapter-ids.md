# T-006 — Monotonic ID adapter

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B2 |
| Blocks | T-016 |
| Blocked by | T-003 |
| Touches files | `internal/adapters/ids/monotonic.go`, `internal/adapters/ids/monotonic_test.go` |

## Goal

Implement `ports.IDGenerator` with a monotonic `uint64` counter per ID kind, formatted as `"o-<n>"` and `"t-<n>"`. Specified in [§02 ID format](../system_design/02-data-structures.md#id-format).

## Context

Both counters start at 1 on every server start. There is no persistence in v1 — restarts wipe state, so cross-restart collisions are not a concern ([§02](../system_design/02-data-structures.md#id-format) and [`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md)).

The generator is called only from inside the engine mutex ([§06](../system_design/06-concurrency-and-determinism.md#sources-and-fixes)), so atomics are unnecessary. Using a plain `uint64` field with no synchronisation is correct **provided** the documentation is explicit that this adapter is not safe for concurrent use outside the engine's lock.

## Acceptance criteria

- [ ] `internal/adapters/ids/monotonic.go` defines `type Monotonic struct{ orderN, tradeN uint64 }`
- [ ] `func NewMonotonic() *Monotonic` returns a pointer with both counters at 0; the first call to `NextOrderID` returns `"o-1"`
- [ ] `NextOrderID()` and `NextTradeID()` increment their respective counter and return the formatted string
- [ ] Compile-time check: `var _ ports.IDGenerator = (*Monotonic)(nil)`
- [ ] Godoc on the type clearly states: "Not safe for concurrent use; callers must serialise access (the engine mutex serves this purpose)."
- [ ] `monotonic_test.go` asserts the first three IDs of each kind and that the two counters are independent (`NextOrderID` does not advance `NextTradeID` and vice versa)
- [ ] `go vet ./internal/adapters/ids/...` clean, `go test ./internal/adapters/ids/...` green

## Implementation notes

- Use `fmt.Sprintf("o-%d", n)` or `"o-" + strconv.FormatUint(n, 10)`. Either is fine; the latter avoids the `fmt` reflection cost.
- Increment **then** format, so the first ID is `"o-1"`, not `"o-0"`. (`++n` in-line.)
- No `sync/atomic` — the package godoc explicitly assumes serialised access. If a reviewer asks about lock-free counters, the answer is "callers serialise via the engine mutex; atomics here would be defensive coding for a contract that holds today and is invariant 12 in [`ARCHITECT_PLAN.md` §3](../system_design/ARCHITECT_PLAN.md)."

## Out of scope

- Snowflake / KSUID / UUID variants (deferred to v2 per [§11](../system_design/11-production-evolution.md)).
- Persistence of counters across restart (not in v1; counters reset to 0).
- Concurrent-safe variant. Add only if a future caller breaks the contract.

## Tests required

- `TestMonotonic_OrderIDStartsAtOne`
- `TestMonotonic_TradeIDStartsAtOne`
- `TestMonotonic_CountersIndependent` — interleave `NextOrderID` and `NextTradeID`, assert each yields the expected sequence
- `TestMonotonic_FormatString` — assert the prefix character (`"o-"` vs `"t-"`)

## Definition of done

- [ ] All acceptance criteria checked
- [ ] No imports outside stdlib + `matching-engine/internal/ports`
