package http

import "net/http"

// MaxBodyBytes is the maximum number of bytes accepted in any HTTP request body.
// Requests that exceed this limit are rejected before the handler decodes JSON.
// Detection of *http.MaxBytesError happens in T-014 handlers via errors.As.
const MaxBodyBytes = 64 << 10 // 64 KB

// BodyLimit wraps the request body with http.MaxBytesReader so that any
// downstream read that exceeds MaxBodyBytes returns *http.MaxBytesError.
// This middleware must be installed before the JSON decoder runs.
// The 413 response itself is written by the handler (T-014), not here.
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
