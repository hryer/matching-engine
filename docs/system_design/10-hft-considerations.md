# 10 — If This Were Really HFT

> Up: [README index](./README.md) | Prev: [§09 Testing](./09-testing.md) | Next: [§11 Production Evolution](./11-production-evolution.md)

This file exists for one reason: the interview's design probes will ask "what about scale / latency?" and the worst answer is to either bluff a lock-free design or freeze. The honest answer is "here is what HFT-grade looks like, here is why none of it applies at HTTP/JSON scope, here is the threshold at which I would change my mind."

The matching engine in v1 is built for the case study. It is **not** HFT-grade — and trying to make it HFT-grade behind an HTTP/JSON front-end would be an exercise in optimising the wrong layer.

---

## v1 vs HFT-grade — where each design choice lands

| Concern | v1 (this challenge) | HFT-grade (Jane Street, HRT, Citadel-class) | Why none of it matters here |
|---|---|---|---|
| **Concurrency** | Single `sync.Mutex` | Single-threaded matcher pinned to a core, lock-free SPSC ring buffer for command intake (LMAX Disruptor) | Matching itself is microseconds; HTTP+JSON adds milliseconds — the lock is not the bottleneck |
| **Order storage** | `container/list` (heap-alloc per push) | Intrusive doubly-linked list — `prev`/`next` embedded in `Order`, zero allocations | GC pressure invisible at <10k orders/sec |
| **Price representation** | `shopspring/decimal` (variable-precision big int) | `int64` ticks (price in tick units) and `int64` lots (qty in lot units) | `Decimal` operations are ~100ns; JSON parsing dwarfs that |
| **Map lookup** | `map[string]*PriceLevel` keyed by canonical decimal string | Open-addressing hash map keyed by integer price; or array-indexed when the price range is bounded (e.g. limit-up/down) | String hashing is fine when wire format is JSON anyway |
| **Tree** | `google/btree` (general-purpose B-tree) | Custom intrusive RB-tree, or array-of-pointers when price-level cardinality is known small | btree is ~200ns per op; the TCP RTT is 100,000× that |
| **Time source** | `time.Now()` via `Clock` interface | Monotonic CPU TSC read with rdtscp + frequency calibration; nanosecond granularity | Wall-clock at ms granularity is fine for a case study |
| **CPU affinity** | None — Go runtime schedules anywhere | Matcher pinned to an isolated core with kernel parameters: `isolcpus`, `nohz_full`, `rcu_nocbs` | Goroutine stealing is invisible at this throughput |
| **NUMA** | Ignored | All matcher state allocated on the local NUMA node; cross-socket access avoided | Single-pair, single-process — no NUMA story |
| **Network I/O** | Standard `net/http` (epoll, copy-in/copy-out) | Kernel-bypass NICs (Solarflare/Onload, DPDK, ef_vi); FPGA tick-to-trade for the absolute extreme | We're handling JSON on TCP; the kernel is doing 99% of the work |
| **Garbage collection** | Default GOGC | GC disabled or near-disabled (`GOGC=off`), arena-allocated state, zero-alloc hot path | Case study throughput won't generate enough garbage to GC pause |
| **Determinism source** | Single mutex + monotonic seq + injectable clock | Same, but enforced via single-writer thread on a pinned core; seq is the sequence number from the inbound ring | The mutex is the sequencer here |

---

## What an HFT-grade matcher looks like in one paragraph

A single thread, pinned to an isolated CPU core on a NUMA-local node, owns the matcher. Inbound orders are written to a ring buffer (SPSC, cache-line padded) by network-receive threads. The matcher reads in a hot loop, writes trades to an output ring, never blocks, never allocates, never crosses a NUMA boundary. Prices are integers in tick units. Orders are intrusive in their price levels. The price tree is hand-rolled to fit the cache line layout. Wall clock is read from rdtscp. Output ring is consumed by network-send threads. End-to-end tick-to-trade is sub-microsecond. Everything is determined by the single matcher thread's view of the inbound ring.

None of that is appropriate for a Go HTTP service handling JSON.

---

## When would I change my mind?

For this case study, never. For a real product, the threshold to start replacing v1 components is roughly:

| Replace | When |
|---|---|
| `sync.Mutex` → sequencer | Sustained > 100k orders/sec/pair, p99.9 latency hits HTTP timeout budget |
| `shopspring/decimal` → integer ticks | Sustained > 1M ops/sec on the matcher, profiler shows decimal arithmetic is top-3 |
| `container/list` → intrusive list | GC pause times become visible in p99.9; allocator is top-3 in pprof |
| `net/http` → custom protocol on raw TCP | JSON parsing is more than 50% of request CPU time |
| Go → C++/Rust | Sub-millisecond tick-to-trade is a hard product requirement |

Each step costs an order of magnitude more engineering for a fraction of the latency. The case study sits below the first threshold by several orders of magnitude — premature optimisation here is the wrong call.

---

## What this section is for in the interview

If the probe is "your engine doesn't scale" — point at this file. The argument:

1. v1 caps at ~100k–1M orders/sec/pair, dominated by the mutex.
2. The graceful upgrade is **shard by pair** first (`map[Instrument]*Engine`), each engine still single-mutex.
3. Past that, **sequencer-based architecture** with WAL fsync — same engine code, just driven by a single-threaded worker reading from a ring buffer.
4. Past that, **HFT-grade rewrite** in C++/Rust on pinned cores with kernel bypass — different product, not the same case study.

The architecture in [§01](./01-architecture.md) supports stages 1, 2, 3 without rewriting the matching engine. Stage 4 is a different conversation.

Next: [§11 Production Evolution →](./11-production-evolution.md)
