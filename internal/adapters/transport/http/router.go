package http

import (
	"net/http"

	"matching-engine/internal/app"
)

// NewRouter constructs the HTTP mux for the matching engine API.
// Routes use Go 1.22+ method+path patterns. BodyLimit middleware is applied
// only to POST /orders; GET and DELETE requests have no body to limit.
func NewRouter(svc *app.Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /orders", BodyLimit(http.HandlerFunc(handlePlace(svc))))
	mux.HandleFunc("DELETE /orders/{id}", handleCancel(svc))
	mux.HandleFunc("GET /orderbook", handleSnapshot(svc))
	mux.HandleFunc("GET /trades", handleTrades(svc))
	return mux
}
