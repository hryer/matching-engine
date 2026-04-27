# T-016 — `cmd/server` composition root

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P0 |
| Estimate | 0.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B6 |
| Blocks | T-017 |
| Blocked by | T-005, T-006, T-007, T-010, T-012, T-014 |
| Touches files | `cmd/server/main.go` |

## Goal

Wire the engine, app.Service, router, adapters, and `http.Server` into a runnable binary with graceful shutdown. Specified in [§01 cmd/server composition root](../system_design/01-architecture.md#repo-layout) and [§08 Graceful shutdown](../system_design/08-http-api.md#graceful-shutdown).

## Context

The composition root is the only place that imports concrete adapters from `internal/adapters/...` and concrete `internal/engine`. Every other layer depends on interfaces or on the engine's public type. This is the point where dependency direction becomes visible.

### Wiring sequence

1. Construct `Real` clock, `Monotonic` IDs, `Ring` publisher (capacity 10,000).
2. Construct `engine.New(Deps{...})` with `MaxOpenOrders=1_000_000`, `MaxArmedStops=100_000`.
3. Construct `app.NewService(engine)`.
4. Construct `httpa.NewRouter(svc)` — returns `http.Handler`.
5. Construct `&http.Server{Addr: ":8080", Handler: router, ReadTimeout: 5s, WriteTimeout: 10s, IdleTimeout: 60s}` — modest defaults; production tuning is v2.
6. Set up signal handling: `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`.
7. Goroutine: `srv.ListenAndServe()` with error logging.
8. Block on `<-ctx.Done()`.
9. `srv.Shutdown(ctxWithTimeout(5s))` — gives in-flight requests a graceful close.

### Constants ([`ARCHITECT_PLAN.md` §7](../system_design/ARCHITECT_PLAN.md#7-open-decisions-to-lock-down-before-coding-starts))

```go
const (
    httpAddr           = ":8080"
    maxOpenOrders      = 1_000_000
    maxArmedStops      = 100_000
    tradeHistoryDepth  = 10_000
    shutdownTimeout    = 5 * time.Second
)
```

`MaxBodyBytes` lives in T-013's `middleware.go` (already imported via the router); no constant needed here.

## Acceptance criteria

- [ ] `cmd/server/main.go` declares `package main` with a `func main()`
- [ ] All four port constructors are called: `clock.NewReal()`, `ids.NewMonotonic()`, `inmem.NewRing(tradeHistoryDepth)`. The composition root is the **only** file in the codebase that imports the adapter packages
- [ ] `engine.New(...)` is called with `MaxOpenOrders` and `MaxArmedStops` set to the constants above
- [ ] `app.NewService(eng)` and `httpa.NewRouter(svc)` are called
- [ ] `http.Server` configured with the timeouts above
- [ ] Signal handling: `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` (Go 1.16+) is used; deferred `stop()`
- [ ] `srv.ListenAndServe()` runs in a goroutine; its return value is logged via `log.Printf` (a non-`http.ErrServerClosed` error is a real failure and the program should exit with non-zero status — but `os.Exit(1)` only after `Shutdown` completes)
- [ ] On `<-ctx.Done()`, `srv.Shutdown(ctxTimeout)` is called and any error is logged
- [ ] `go vet ./cmd/server/...` and `go build ./cmd/server/...` clean
- [ ] `go run ./cmd/server` starts the server and accepts a `curl -X POST localhost:8080/orders ...` per the brief example

## Implementation notes

- Skeleton:
    ```go
    package main

    import (
        "context"
        "errors"
        "log"
        "net/http"
        "os"
        "os/signal"
        "syscall"
        "time"

        "matching-engine/internal/adapters/clock"
        "matching-engine/internal/adapters/ids"
        "matching-engine/internal/adapters/publisher/inmem"
        httpa "matching-engine/internal/adapters/transport/http"
        "matching-engine/internal/app"
        "matching-engine/internal/engine"
    )

    const (
        httpAddr          = ":8080"
        maxOpenOrders     = 1_000_000
        maxArmedStops     = 100_000
        tradeHistoryDepth = 10_000
        shutdownTimeout   = 5 * time.Second
    )

    func main() {
        eng := engine.New(engine.Deps{
            Clock:         clock.NewReal(),
            IDs:           ids.NewMonotonic(),
            Publisher:     inmem.NewRing(tradeHistoryDepth),
            MaxOpenOrders: maxOpenOrders,
            MaxArmedStops: maxArmedStops,
        })
        svc := app.NewService(eng)
        router := httpa.NewRouter(svc)

        srv := &http.Server{
            Addr:         httpAddr,
            Handler:      router,
            ReadTimeout:  5 * time.Second,
            WriteTimeout: 10 * time.Second,
            IdleTimeout:  60 * time.Second,
        }

        ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer stop()

        serverErr := make(chan error, 1)
        go func() {
            log.Printf("listening on %s", httpAddr)
            if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
                serverErr <- err
            }
            close(serverErr)
        }()

        select {
        case err := <-serverErr:
            if err != nil {
                log.Printf("listen error: %v", err)
                os.Exit(1)
            }
        case <-ctx.Done():
            log.Print("shutdown signal received")
        }

        shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
        defer cancel()
        if err := srv.Shutdown(shutdownCtx); err != nil {
            log.Printf("shutdown error: %v", err)
        }
    }
    ```
- The exact import alias `httpa "matching-engine/internal/adapters/transport/http"` is used to avoid collision with stdlib `net/http`.
- Do **not** add flag parsing for port / cap configuration in v1 — constants are fine. (Reviewer feedback path: if asked, "yes, these would be flags or env in production; v1 keeps the surface minimal.")
- Do **not** add `pprof`, OpenTelemetry, or `prometheus.Register`. None of those are in scope.

## Out of scope

- Configuration files / env-var parsing.
- Health check endpoint (acceptable to add `GET /healthz` returning 200 if you have spare time, but it's not in the brief).
- Metrics / tracing.
- TLS / mTLS.
- Multiple instruments (single pair, single engine).

## Tests required

None. The composition root is glue; the integration test (T-015) exercises the wiring through `httptest.NewServer(httpa.NewRouter(svc))` which uses the **same wiring code** but with a `Fake` clock and a fresh engine. If `go vet` and `go build ./cmd/server` pass, this ticket is functionally correct.

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `go vet ./cmd/server/...` clean
- [ ] `go build ./cmd/server/...` clean
- [ ] Manual smoke: `go run ./cmd/server` starts; `curl localhost:8080/orderbook` returns `{"bids":[],"asks":[]}`; `curl -X POST localhost:8080/orders -d '{...}'` round-trips
- [ ] Ctrl-C / SIGTERM cleanly shuts down (no panic, no goroutine leak)
