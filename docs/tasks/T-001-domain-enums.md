# T-001 — Domain enums + JSON marshalling

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.75 h (±25%) |
| Owner | unassigned |
| Parallel batch | B1 |
| Blocks | T-008, T-010, T-013 |
| Blocked by | none |
| Touches files | `internal/domain/enums.go`, `internal/domain/enums_test.go` |

## Goal

Implement the three domain enums (`Side`, `Type`, `Status`) as `uint8` types with `MarshalJSON` / `UnmarshalJSON` matching the wire format the brief uses. Specified in [§02 Data Structures — Enums](../system_design/02-data-structures.md#enums).

## Context

Every layer above domain depends on these. The `Type` enum is mutated in the cascade (a `Stop` becomes `Market`; a `StopLimit` becomes `Limit` after triggering — see [§05](../system_design/05-stop-orders.md)) so it must be a plain value, not behind any interface.

Wire strings are exactly:
- `Side`: `"buy"`, `"sell"`
- `Type`: `"limit"`, `"market"`, `"stop"`, `"stop_limit"`
- `Status`: `"armed"`, `"resting"`, `"partially_filled"`, `"filled"`, `"cancelled"`, `"rejected"`

The integer values are listed in [§02](../system_design/02-data-structures.md#enums) and must match exactly (used as `uint8` for cache friendliness; some tests compare numeric values).

## Acceptance criteria

- [ ] `internal/domain/enums.go` defines `Side`, `Type`, `Status` as `uint8` with constants in the order shown in [§02](../system_design/02-data-structures.md#enums)
- [ ] Each type has `MarshalJSON()` returning the wire string, and `UnmarshalJSON([]byte) error` parsing it
- [ ] Unknown enum string on `UnmarshalJSON` returns an error containing the offending value
- [ ] `enums_test.go` round-trips every variant of every enum through `json.Marshal` → `json.Unmarshal` and asserts equality
- [ ] `enums_test.go` includes a negative case: unmarshalling `"BUY"` (wrong case) and `"foobar"` returns an error
- [ ] `go vet ./internal/domain/...` and `go test ./internal/domain/...` are clean

## Implementation notes

- Use a `switch` in `MarshalJSON` and `UnmarshalJSON`; no `map[Side]string` lookup table needed.
- `MarshalJSON` returns bytes including the surrounding quote chars: `[]byte("\"buy\"")`.
- `UnmarshalJSON` must accept input including quotes (it gets the raw JSON token). Use `strconv.Unquote` or strip the first/last byte.
- The `Stringer` interface (`func (Side) String() string`) is optional but recommended — it makes test failures readable.
- No dependency on `decimal`, no dependency on `time` — this file is leaf-level.

## Out of scope

- `Order` and `Trade` structs (T-004).
- HTTP DTO conversions (T-013).
- Adding a new enum variant for v2 features.

## Tests required

- Round-trip every variant of `Side`, `Type`, `Status`.
- Round-trip a struct embedding all three: `struct{S Side; T Type; St Status}` to verify the marshallers work as struct field marshallers, not just at the top level.
- Negative cases: empty string, wrong case, unknown variant, malformed JSON (no quotes).

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./internal/domain/...` clean
- [ ] `go test ./internal/domain/...` green
- [ ] No imports outside stdlib
- [ ] Touches-files list matches the actual diff
