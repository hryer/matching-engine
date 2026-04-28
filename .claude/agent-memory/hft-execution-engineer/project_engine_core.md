---
name: Engine core — T-010 implementation notes
description: Key decisions, invariants, and edge cases from implementing engine.go and errors.go in T-010
type: project
---

Engine core (T-010) is split across two parallel implementers. A owns engine.go + errors.go; B owns match.go; QA owns engine_test.go.

**Why:** The split aligns with the spec decomposition: §04 matching kernel + §05 cascade live in match.go; public surface, locking, and resource-bound semantics live in engine.go.

**Resolved OPEN QUESTION — partial-fill-then-cap-hit → option (b):** Trades are real and already published by the time the cap-check fires. No rollback is possible without making the publisher and maker mutations transactional (explicitly disclaimed in the design). The cap is a back-pressure signal at the user-call boundary. If partial fills occurred before the cap hit, they are kept and the remainder is silently truncated (Status=PartiallyFilled, no error). This is symmetric with Market-PartiallyFilled.

**Critical match contract (A → B interface):** match does NOT set terminal status for the Limit case. It sets Filled, Cancelled (STP), Rejected (Market-no-trades), PartiallyFilled (Market-partial). Place inspects (order.Status, len(trades), order.RemainingQuantity) and decides Resting vs PartiallyFilled vs cap-rejected. Deviation from §04 pseudocode is intentional — cap-check is a Place-time concern, not a match-time concern.

**Counter accounting — A's responsibility:**
- openOrders +1: Limit rests (no fills, cap not hit); Limit partial-fill, cap not hit.
- openOrders -1: Cancel resting order.
- armedStops +1: Stop/StopLimit armed (trigger not satisfied, cap not hit).
- armedStops -1: Cancel armed stop.
- Cap errors (ErrTooManyOrders, ErrTooManyStops) return (PlaceResult{}, err) with ZERO counter mutation.
- Trigger-already-satisfied rejection returns (PlaceResult{Order: rejected}, nil) — NOT an error, dedup layer caches it.

**Flagged ambiguity §11.1 — sell stops on fresh engine:** lastTradePrice=decimal.Zero at init means every positive-trigger sell stop is rejected (trigger >= 0 fires the already-satisfied rule). Implemented as written per plan default. Surface to architect in PR.

**byID invariant:** Contains ONLY resting (non-terminal) orders. Armed stops live in stops.StopBook.byID exclusively. After every public method: openOrders == len(byID) and armedStops == stops.Len().

**Panic branch in placeMatchable:** unreachable guard at end of switch catches match contract drift (status never left at zero value for any order type). Loud failure in dev, impossible in prod given correct match implementation.

**How to apply:** When extending Place (new order types), new cap types, or touching the match contract, re-read §3 and §4 of T-010-PLAN.md. The counter accounting table in §4 is the single source of truth.
