# T-003 — Port interfaces (Clock, IDGenerator, EventPublisher)

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B1 |
| Blocks | T-005, T-006, T-007, T-010 |
| Blocked by | none |
| Touches files | `internal/ports/clock.go`, `internal/ports/ids.go`, `internal/ports/publisher.go` |

## Goal

Define the three port interfaces the engine depends on: `Clock`, `IDGenerator`, `EventPublisher`. Specified in [§01 Architecture](../system_design/01-architecture.md#repo-layout) and the [v1 component diagram](../system_design/01-architecture.md#component-view-v1).

## Context

The engine never imports `time.Now()` or instantiates concrete adapters; it consumes these interfaces through a `Deps` struct passed at construction. The interfaces are tiny; they exist to make tests deterministic and to keep adapter implementations swappable.

`EventPublisher` carries `*domain.Trade`. The v1 in-memory adapter (T-007) keeps the last 10,000 trades; in v2 the same port routes to Kafka or WebSocket.

## Acceptance criteria

- [ ] `internal/ports/clock.go` defines `type Clock interface { Now() time.Time }` (single method)
- [ ] `internal/ports/ids.go` defines `type IDGenerator interface { NextOrderID() string; NextTradeID() string }` (returns the formatted strings `"o-<n>"`, `"t-<n>"` per [§02 ID format](../system_design/02-data-structures.md#id-format))
- [ ] `internal/ports/publisher.go` defines `type EventPublisher interface { Publish(trade *domain.Trade); Recent(limit int) []*domain.Trade }`
- [ ] All three files declare `package ports`
- [ ] No file in `internal/ports/` imports anything outside stdlib + `internal/domain` (specifically: no `internal/engine`, no `internal/adapters`)
- [ ] `go vet ./internal/ports/...` clean
- [ ] `go build ./internal/ports/...` clean

## Implementation notes

- Each interface is in its own file, even though the files are tiny — symmetric with the adapter package layout.
- `Publish` is fire-and-forget from the engine's perspective (no error return). The in-memory adapter cannot fail; if a future Kafka adapter can fail, that's its own buffering problem behind the interface.
- `Recent(limit int) []*domain.Trade` returns the most recent up to `limit` trades, newest-first or oldest-first — pick one convention here and document in the godoc comment. Recommend **newest-first** (matches the typical exchange `GET /trades` UX). The HTTP layer (T-014) reads this verbatim.
- No `context.Context` parameter on `Publish`; engine work is single-threaded under the mutex and cannot meaningfully cancel.
- Do **not** add `ports.RiskGateway`, `ports.Journal`, or any v2 hook. The seam is the directory; the implementation arrives only when the consumer arrives.

## Out of scope

- Concrete adapter implementations (T-005, T-006, T-007).
- Subscribe / fan-out semantics (deferred to v2 per [§11](../system_design/11-production-evolution.md)).
- Mock implementations for tests (each adapter ticket carries its own test impl if needed; the engine tests use the real adapters).

## Tests required

None. Interfaces have no behaviour. Tests live with the adapters.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] Godoc comments on every exported type and method
- [ ] No file imports outside stdlib + `matching-engine/internal/domain`
