package http

import (
	"encoding/json"
	"log"
	"net/http"
)

// Error code constants used in ErrorResponse.Code.
// These are the exact string values sent on the wire; do not rename them.
const (
	CodeValidation      = "validation"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeRequestTooLarge = "request_too_large"
	CodeTooManyOrders   = "too_many_orders"
	CodeTooManyStops    = "too_many_stops"
	CodeInternal        = "internal"
)

// WriteError writes a JSON error response with the given HTTP status code.
// It sets Content-Type to application/json before writing the header so the
// caller does not need to do so separately. Any encoding failure is logged
// but cannot be recovered — the response header is already sent.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: msg, Code: code}); err != nil {
		log.Printf("http: WriteError encode failed: %v", err)
	}
}
