package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestErrorResponse_JSONShape verifies that ErrorResponse serialises to the
// exact wire format mandated by §08: {"error":"...","code":"..."}.
func TestErrorResponse_JSONShape(t *testing.T) {
	t.Parallel()

	want := `{"error":"x","code":"validation"}` + "\n"

	got, err := json.Marshal(ErrorResponse{Error: "x", Code: CodeValidation})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// json.Marshal does not append a newline; compare without it.
	if string(got) != strings.TrimSuffix(want, "\n") {
		t.Errorf("got %s, want %s", got, strings.TrimSuffix(want, "\n"))
	}
}

// TestOrderDTO_OmitEmpty verifies that Price and TriggerPrice are absent from
// the JSON output when their values are the zero string — i.e. for market orders.
func TestOrderDTO_OmitEmpty(t *testing.T) {
	t.Parallel()

	dto := OrderDTO{
		ID:                "o-1",
		UserID:            "u-1",
		ClientOrderID:     "coid-1",
		Side:              "buy",
		Type:              "market",
		Price:             "", // must be omitted
		TriggerPrice:      "", // must be omitted
		Quantity:          "1.0",
		RemainingQuantity: "0",
		Status:            "filled",
		CreatedAt:         "2026-04-20T10:00:00Z",
	}

	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if strings.Contains(out, `"price"`) {
		t.Errorf(`"price" key present in market order JSON: %s`, out)
	}
	if strings.Contains(out, `"trigger_price"`) {
		t.Errorf(`"trigger_price" key present in market order JSON: %s`, out)
	}
}

// TestWriteError_StatusAndContentType verifies that WriteError sets the HTTP
// status code, Content-Type header, and JSON body correctly.
func TestWriteError_StatusAndContentType(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, CodeValidation, "bad")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if resp.Error != "bad" {
		t.Errorf("error field: got %q, want %q", resp.Error, "bad")
	}
	if resp.Code != CodeValidation {
		t.Errorf("code field: got %q, want %q", resp.Code, CodeValidation)
	}
}

// TestBodyLimit_RejectsOversizedBody verifies that a body one byte over
// MaxBodyBytes causes the downstream read to fail with *http.MaxBytesError.
// This mirrors what T-014 handlers will encounter when BodyLimit is installed.
func TestBodyLimit_RejectsOversizedBody(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, MaxBodyBytes+1)

	var readErr error
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	handler := BodyLimit(downstream)

	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(oversized))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var maxBytesErr *http.MaxBytesError
	if !errors.As(readErr, &maxBytesErr) {
		t.Errorf("expected *http.MaxBytesError, got %T: %v", readErr, readErr)
	}
}
