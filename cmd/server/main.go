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
