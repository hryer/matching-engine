# T-005 — Clock adapter (real + fake)

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
| Touches files | `internal/adapters/clock/real.go`, `internal/adapters/clock/fake.go`, `internal/adapters/clock/clock_test.go` |

## Goal

Implement two `ports.Clock` adapters: `Real` (wraps `time.Now()`) for production and `Fake` (advances under test control) for tests. Specified in [§01 Architecture](../system_design/01-architecture.md#repo-layout) and [§06 Determinism](../system_design/06-concurrency-and-determinism.md#sources-and-fixes).

## Context

The engine never calls `time.Now()` directly; it goes through `ports.Clock`. Tests need `Fake` to drive deterministic timestamps in trades and orders so the replay test in T-011 can byte-compare JSON output.

`Fake` semantics:

- `Now()` returns the currently-set instant
- `Advance(d time.Duration)` adds `d` to the current instant
- `Set(t time.Time)` overwrites the current instant
- `Now()` does not auto-advance; tests are explicit about clock motion

## Acceptance criteria

- [ ] `internal/adapters/clock/real.go` defines `type Real struct{}` with `func (Real) Now() time.Time { return time.Now() }`. A constructor `func NewReal() Real` is acceptable
- [ ] `internal/adapters/clock/fake.go` defines `type Fake struct{ ... }` with `Now`, `Advance`, `Set` methods. `Fake` is **not** required to be goroutine-safe (engine tests are single-threaded under the engine mutex; concurrency tests use `Real` if any)
- [ ] Both implement `ports.Clock` (compile-time check with `var _ ports.Clock = (*Real)(nil)` and same for `Fake`)
- [ ] `clock_test.go` covers: `Real.Now()` returns a recent time; `Fake.Now()` returns the set instant; `Fake.Advance(time.Second).Now()` advances by exactly 1s; `Set` overwrites
- [ ] `go vet ./internal/adapters/clock/...` clean, `go test ./internal/adapters/clock/...` green

## Implementation notes

- `Fake` may be a value type (`Fake struct{ now time.Time }`) but then `Advance` / `Set` need a pointer receiver. Prefer `*Fake` returned from a constructor: `NewFake(initial time.Time) *Fake`.
- Recommend constructor for `Fake` that takes the initial instant: `NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))`. The replay test seeds from a fixed instant so output is reproducible.
- No need for monotonic-clock semantics; `time.Time` already carries a monotonic reading from `time.Now()`. `Fake` does not (it's a constructed instant), which is fine — tests only compare exact equality.
- Don't add a `NowMonotonic() int64` method or a `Sleep()` method. The engine doesn't use them.

## Out of scope

- `time.Tick` or auto-advancing clocks (not needed; tests advance explicitly).
- Goroutine-safe `Fake` (the engine's mutex serialises all calls).
- Wiring into the engine (T-010) or the composition root (T-016).

## Tests required

- `TestReal_NowMovesForward`: take two readings, second is `>=` first.
- `TestFake_NowReturnsSetInstant`: construct with a known instant, assert equality.
- `TestFake_Advance`: advance by 250ms, 750ms, assert cumulative result.
- `TestFake_Set`: set to a new instant, assert.
- Compile-time interface checks via `var _ ports.Clock = (*Real)(nil)` etc.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] No imports outside stdlib + `matching-engine/internal/ports`
- [ ] Touches-files list matches reality
