# T-002 — Decimal wrapper

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B1 |
| Blocks | T-008, T-009, T-010, T-013 |
| Blocked by | none |
| Touches files | `internal/domain/decimal/decimal.go` |

## Goal

Provide a thin alias wrapper over `github.com/shopspring/decimal` so the rest of the codebase imports `internal/domain/decimal` and the underlying library is swappable in one file. Specified in [§07 Decimal Arithmetic](../system_design/07-decimal-arithmetic.md#wrapper-sketch).

## Context

The brief bans `float64`. We use `shopspring/decimal` for all monetary and quantity arithmetic. Wrapping it lets us swap to `cockroachdb/apd` or integer minor units later by editing one file.

## Acceptance criteria

- [ ] `internal/domain/decimal/decimal.go` defines `type Decimal = sd.Decimal` (type alias, not a new type — preserves all method calls)
- [ ] Re-exports the symbols actually used by the rest of the codebase: at minimum `Zero`, `NewFromString`, `NewFromInt`. Add others lazily as needed
- [ ] `go.mod` is updated to require `github.com/shopspring/decimal` v1.4+ (run `go get github.com/shopspring/decimal`)
- [ ] `go vet ./internal/domain/decimal/...` clean
- [ ] `go build ./internal/domain/decimal/...` clean

## Implementation notes

- Use a **type alias** (`type Decimal = sd.Decimal`), not a defined type (`type Decimal sd.Decimal`). The alias preserves method calls without forwarding shims.
- The variable-style re-exports look like `var Zero = sd.Zero` and `var NewFromString = sd.NewFromString`. They're values/function values, not type aliases.
- No tests on this file directly — the wrapper has zero logic. Decimal correctness is exercised wherever `Decimal` is used (book, stops, match).
- Do not write a `priceKey` helper here. That helper canonicalises trailing zeros for use as a map key and lives next to the order book where it's used (T-008).

## Out of scope

- `priceKey` canonicalisation (T-008).
- Integer-minor-unit alternative (deferred per [§07](../system_design/07-decimal-arithmetic.md#honest-take-on-integer-minor-units)).
- Validation of decimal precision at the HTTP boundary (T-014).

## Tests required

None. The wrapper has no behaviour to test directly.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go.mod` and `go.sum` reflect the new dependency
- [ ] No imports of `shopspring/decimal` outside `internal/domain/decimal/decimal.go` — the rest of the codebase imports the wrapper. (This is enforced by code review; can be checked with `grep -r 'shopspring/decimal' --include='*.go'` after later tickets land — only the wrapper file should appear.)
