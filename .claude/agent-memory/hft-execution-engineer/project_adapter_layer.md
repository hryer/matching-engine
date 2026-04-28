---
name: Adapter layer — clock, IDs, publisher
description: Design decisions and invariants for the three B2 adapter packages (T-005/006/007)
type: project
---

Three adapter packages implemented under internal/adapters/:

**Clock (internal/adapters/clock/)**
- Real wraps time.Now(); Fake is NOT goroutine-safe by design — engine mutex serialises all calls.
- Fake.Now() does not auto-advance; tests call Advance() or Set() explicitly for determinism.
- Why: replay test (T-011) seeds a fixed Fake instant so JSON output is byte-identical across runs.

**IDs (internal/adapters/ids/)**
- Monotonic struct holds two independent uint64 counters (orderN, tradeN), both start at 0, first call returns "o-1"/"t-1".
- Uses strconv.FormatUint (not fmt.Sprintf) to avoid reflection overhead.
- NOT goroutine-safe — called only inside engine.mu per §06 design invariant 12.
- Why no atomics: engine mutex is the serialisation primitive; atomics would be defensive coding against a held invariant.

**Publisher (internal/adapters/publisher/inmem/)**
- Ring struct: fixed-size buf slice allocated once at construction; next (write cursor), count, cap fields.
- Publish is O(1) — no append, no allocation in steady state; oldest slot overwritten on overflow.
- Recent(limit) walks backwards from (next-1) for limit steps; returns newest-first.
- Index arithmetic: (r.next - 1 - i + r.cap*2) % r.cap — the *2 guard handles r.next==0 without negative modulo issues in Go.
- NOT goroutine-safe — called from engine.Place (Publish) and engine.Trades (Recent), both under engine.mu.
- Panics on NewRing(0) or NewRing(negative).
- Default capacity (10_000) is set at the composition root (T-016), not in this package.

**How to apply:** When wiring in T-016, pass capacity=10_000 to NewRing. When writing engine tests (T-010/011), use clock.NewFake with a seeded fixed instant.
