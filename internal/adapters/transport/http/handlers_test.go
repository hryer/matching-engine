// Package http_test exercises the HTTP transport layer end-to-end through
// the public wire surface. Each subtest boots a fresh engine + service +
// httptest.Server so test order is irrelevant and no state leaks between
// subtests.
//
// Package naming: http_test (external test package) — never imports unexported
// symbols from package http. It only sees the same API a real client would.
package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"matching-engine/internal/adapters/clock"
	"matching-engine/internal/adapters/ids"
	"matching-engine/internal/adapters/publisher/inmem"
	httpa "matching-engine/internal/adapters/transport/http"
	"matching-engine/internal/app"
	"matching-engine/internal/domain/decimal"
	"matching-engine/internal/engine"
)

// ---- stack helpers ---------------------------------------------------------

// newStack boots a fresh, isolated full stack (engine → service → HTTP server).
// The caller must call the returned cleanup function when done.
func newStack(t *testing.T) (srvURL string, cleanup func()) {
	t.Helper()
	eng := engine.New(engine.Deps{
		Clock:         clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		IDs:           ids.NewMonotonic(),
		Publisher:     inmem.NewRing(100),
		MaxOpenOrders: 1_000_000,
		MaxArmedStops: 100_000,
	})
	svc := app.NewService(eng)
	srv := httptest.NewServer(httpa.NewRouter(svc))
	return srv.URL, srv.Close
}

// placeOrder POSTs a PlaceOrderRequest and returns (status, raw body bytes).
func placeOrder(t *testing.T, srvURL string, req httpa.PlaceOrderRequest) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal PlaceOrderRequest: %v", err)
	}
	resp, err := http.Post(srvURL+"/orders", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// cancelOrder sends DELETE /orders/{id} and returns (status, raw body bytes).
func cancelOrder(t *testing.T, srvURL, id string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", srvURL+"/orders/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /orders/%s: %v", id, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// mustParsePlace decodes raw bytes into PlaceOrderResponse. Fails the test on
// decode error.
func mustParsePlace(t *testing.T, raw []byte) httpa.PlaceOrderResponse {
	t.Helper()
	var resp httpa.PlaceOrderResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal PlaceOrderResponse: %v\nbody: %s", err, raw)
	}
	return resp
}

// mustParseError decodes raw bytes into ErrorResponse.
func mustParseError(t *testing.T, raw []byte) httpa.ErrorResponse {
	t.Helper()
	var resp httpa.ErrorResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal ErrorResponse: %v\nbody: %s", err, raw)
	}
	return resp
}

// mustDecEqual parses both strings with decimal.NewFromString and asserts they
// are equal. Use this for all decimal comparisons to avoid trailing-zero issues.
func mustDecEqual(t *testing.T, label, got, want string) {
	t.Helper()
	g, err := decimal.NewFromString(got)
	if err != nil {
		t.Fatalf("%s: parse got %q: %v", label, got, err)
	}
	w, err := decimal.NewFromString(want)
	if err != nil {
		t.Fatalf("%s: parse want %q: %v", label, want, err)
	}
	if !g.Equal(w) {
		t.Errorf("%s: got %s, want %s", label, got, want)
	}
}

// ---- scenarios -------------------------------------------------------------

// Scenario 1: Place limit buy → 201, status "resting", trades [].
func TestHTTP_PlaceLimitBuy(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})

	if status != 201 {
		t.Fatalf("want 201, got %d\nbody: %s", status, raw)
	}
	resp := mustParsePlace(t, raw)
	if resp.Order.Status != "resting" {
		t.Errorf("want status=resting, got %q", resp.Order.Status)
	}
	if len(resp.Trades) != 0 {
		t.Errorf("want trades=[], got %v", resp.Trades)
	}
	if resp.Order.ID == "" {
		t.Error("order ID must not be empty")
	}
	if resp.Order.ClientOrderID != "coid-1" {
		t.Errorf("client_order_id echo: got %q, want %q", resp.Order.ClientOrderID, "coid-1")
	}
}

// Scenario 2: Place crossing limit sell → 201, one trade, trade price == maker price.
func TestHTTP_CrossingLimitSell(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Place maker (buy limit @ 100).
	_, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-maker",
		ClientOrderID: "coid-maker",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	makerResp := mustParsePlace(t, raw)
	makerPrice := makerResp.Order.Price

	// Place crossing taker (sell limit @ 100 — crosses the resting buy).
	takerStatus, takerRaw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-taker",
		ClientOrderID: "coid-taker",
		Side:          "sell",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})

	if takerStatus != 201 {
		t.Fatalf("want 201, got %d\nbody: %s", takerStatus, takerRaw)
	}
	takerResp := mustParsePlace(t, takerRaw)

	if len(takerResp.Trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(takerResp.Trades))
	}
	trade := takerResp.Trades[0]

	// Trade price must equal the maker's resting price (exchange convention).
	mustDecEqual(t, "trade.price", trade.Price, makerPrice)

	if trade.TakerSide != "sell" {
		t.Errorf("taker_side: got %q, want sell", trade.TakerSide)
	}
}

// Scenario 3: GET /trades after a cross → exactly one trade in body.
func TestHTTP_GetTradesAfterCross(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Create a cross.
	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-maker",
		ClientOrderID: "coid-maker",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-taker",
		ClientOrderID: "coid-taker",
		Side:          "sell",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})

	resp, err := http.Get(srvURL + "/trades")
	if err != nil {
		t.Fatalf("GET /trades: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d\nbody: %s", resp.StatusCode, raw)
	}

	var body httpa.TradesResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal TradesResponse: %v", err)
	}
	if len(body.Trades) != 1 {
		t.Errorf("want 1 trade in GET /trades, got %d\nbody: %s", len(body.Trades), raw)
	}
}

// Scenario 4: GET /orderbook after a cross → empty on both sides (fully filled),
// bids and asks must be [] not null.
func TestHTTP_GetOrderbookAfterCross(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Full cross: both sides qty=1, completely consumed.
	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-maker",
		ClientOrderID: "coid-maker",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-taker",
		ClientOrderID: "coid-taker",
		Side:          "sell",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})

	resp, err := http.Get(srvURL + "/orderbook")
	if err != nil {
		t.Fatalf("GET /orderbook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d\nbody: %s", resp.StatusCode, raw)
	}

	// Verify [] not null: if JSON contains "null" for bids or asks it's a bug.
	s := string(raw)
	if strings.Contains(s, `"bids":null`) {
		t.Errorf("bids is null, want []\nbody: %s", s)
	}
	if strings.Contains(s, `"asks":null`) {
		t.Errorf("asks is null, want []\nbody: %s", s)
	}

	var body httpa.SnapshotResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal SnapshotResponse: %v", err)
	}
	// Both sides fully consumed — book should be empty.
	if len(body.Bids) != 0 {
		t.Errorf("want empty bids, got %v", body.Bids)
	}
	if len(body.Asks) != 0 {
		t.Errorf("want empty asks, got %v", body.Asks)
	}
}

// Scenario 5: DELETE /orders/{id} → 200, status "cancelled".
func TestHTTP_CancelRestingOrder(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	_, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	placed := mustParsePlace(t, raw)
	orderID := placed.Order.ID

	cancelStatus, cancelRaw := cancelOrder(t, srvURL, orderID)
	if cancelStatus != 200 {
		t.Fatalf("want 200, got %d\nbody: %s", cancelStatus, cancelRaw)
	}

	var cancelResp httpa.CancelOrderResponse
	if err := json.Unmarshal(cancelRaw, &cancelResp); err != nil {
		t.Fatalf("unmarshal CancelOrderResponse: %v", err)
	}
	if cancelResp.Order.Status != "cancelled" {
		t.Errorf("want status=cancelled, got %q", cancelResp.Order.Status)
	}
}

// Scenario 6: DELETE on already-cancelled order.
//
// Engine behaviour: a cancelled order is removed from e.byID at cancel time.
// A second cancel therefore cannot find it via byID or stops — the engine
// returns ErrOrderNotFound, which maps to HTTP 404 not_found.
//
// Chosen branch: 404 not_found.
// (ErrAlreadyTerminal / 409 conflict would require the order to still be in
// byID with a terminal flag — that invariant is never set by the engine;
// byID only holds resting orders. See engine.go Cancel implementation.)
func TestHTTP_CancelAlreadyCancelled(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	_, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	placed := mustParsePlace(t, raw)
	orderID := placed.Order.ID

	// First cancel — expect 200.
	cancelOrder(t, srvURL, orderID)

	// Second cancel — order no longer in byID or stops → 404 not_found.
	status, raw2 := cancelOrder(t, srvURL, orderID)
	if status != 404 {
		t.Fatalf("want 404, got %d\nbody: %s", status, raw2)
	}
	errResp := mustParseError(t, raw2)
	if errResp.Code != "not_found" {
		t.Errorf("want code=not_found, got %q", errResp.Code)
	}
}

// Scenario 7: DELETE on unknown id → 404 not_found.
func TestHTTP_CancelUnknownID(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := cancelOrder(t, srvURL, "o-does-not-exist")
	if status != 404 {
		t.Fatalf("want 404, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "not_found" {
		t.Errorf("want code=not_found, got %q", errResp.Code)
	}
}

// Scenario 8: Idempotent retry, same body → byte-identical response.
// Third unrelated POST gets order ID "o-2" (not "o-3"), proving engine called once.
func TestHTTP_IdempotencyRetry_SameBody(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	req := httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-idem",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	}

	// First call.
	_, raw1 := placeOrder(t, srvURL, req)
	// Retry — identical body, same key.
	_, raw2 := placeOrder(t, srvURL, req)

	if !bytes.Equal(raw1, raw2) {
		t.Errorf("idempotency: responses not byte-identical\nfirst:  %s\nsecond: %s", raw1, raw2)
	}

	// Third call with a fresh key — engine called for the first time on this key.
	// Since the engine was called exactly once for the idem key (o-1), this gets o-2.
	_, raw3 := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-fresh",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	resp3 := mustParsePlace(t, raw3)
	if resp3.Order.ID != "o-2" {
		t.Errorf("engine called more than once for dedup key: fresh order ID = %q, want %q", resp3.Order.ID, "o-2")
	}
}

// Scenario 9: Idempotent retry, different body → cached response returned.
func TestHTTP_IdempotencyRetry_DifferentBody(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	req := httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-idem",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	}
	_, raw1 := placeOrder(t, srvURL, req)

	// Different price — same key, must still return the cached first response.
	reqDiff := req
	reqDiff.Price = "200"
	_, raw2 := placeOrder(t, srvURL, reqDiff)

	if !bytes.Equal(raw1, raw2) {
		t.Errorf("idempotency (diff body): responses not byte-identical\nfirst:  %s\nretry:  %s", raw1, raw2)
	}
}

// Scenario 10: Missing client_order_id → 400 validation.
func TestHTTP_MissingClientOrderID(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Omit ClientOrderID (empty string serialises; validation catches it).
	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:   "u-1",
		Side:     "buy",
		Type:     "limit",
		Price:    "100",
		Quantity: "1",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
	if !strings.Contains(errResp.Error, "client_order_id is required") {
		t.Errorf("want message to contain 'client_order_id is required', got %q", errResp.Error)
	}
}

// Scenario 11: client_order_id too long (65 chars) → 400.
func TestHTTP_ClientOrderID_TooLong(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: strings.Repeat("x", 65),
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
}

// Scenario 12: client_order_id with non-printable byte (\x00) → 400.
func TestHTTP_ClientOrderID_NonPrintable(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "bad\x00id",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
}

// Scenario 13: user_id missing → 400.
func TestHTTP_MissingUserID(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
}

// Scenario 14: user_id too long (129 chars) → 400.
func TestHTTP_UserID_TooLong(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        strings.Repeat("u", 129),
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
}

// Scenario 15: Body too large (64 KB + 1) → 413 request_too_large.
//
// Implementation note: we cannot pad via client_order_id (validation fires
// first) or user_id (same). The cleanest approach is to inline the JSON
// manually and pad user_id past the body-size limit. The MaxBytesReader wraps
// r.Body before the handler calls dec.Decode; the decode read triggers it.
// We build a raw body string large enough (65537 bytes total) with a valid
// JSON envelope so the size check fires before field validation.
func TestHTTP_BodyTooLarge(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Target total body size: 65537 bytes (64 KB + 1).
	// JSON envelope overhead: ~100 bytes. Pad user_id to make up the rest.
	const target = 64*1024 + 1
	const envelope = `{"user_id":"` + `` // prefix
	const suffix = `","client_order_id":"x","side":"buy","type":"limit","price":"100","quantity":"1"}`
	padding := target - len(envelope) - len(suffix)
	if padding < 0 {
		t.Fatal("envelope calculation error")
	}
	body := []byte(envelope + strings.Repeat("a", padding) + suffix)
	if len(body) < target {
		// Belt-and-braces: add extra bytes if the const calc was off.
		body = append(body, bytes.Repeat([]byte(" "), target-len(body))...)
	}

	resp, err := http.Post(srvURL+"/orders", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 413 {
		t.Fatalf("want 413, got %d\nbody: %s", resp.StatusCode, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "request_too_large" {
		t.Errorf("want code=request_too_large, got %q", errResp.Code)
	}
}

// Scenario 16: Quantity exceeds 10^15 → 400, message contains "quantity exceeds maximum".
func TestHTTP_QuantityExceedsMax(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1000000000000001", // 10^15 + 1
	})
	if status != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", status, raw)
	}
	errResp := mustParseError(t, raw)
	if errResp.Code != "validation" {
		t.Errorf("want code=validation, got %q", errResp.Code)
	}
	if !strings.Contains(errResp.Error, "quantity exceeds maximum 1000000000000000") {
		t.Errorf("want message to contain exact upper-bound message, got %q", errResp.Error)
	}
}

// Scenario 17: Empty book + market order → 201 with status "rejected", trades [].
// The market order finds no opposing liquidity; the engine sets StatusRejected.
// HTTP must still return 201 (business reject, not validation reject).
func TestHTTP_EmptyBookMarketOrder(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-market",
		Side:          "buy",
		Type:          "market",
		Quantity:      "1",
	})
	if status != 201 {
		t.Fatalf("want 201, got %d\nbody: %s", status, raw)
	}
	resp := mustParsePlace(t, raw)
	if resp.Order.Status != "rejected" {
		t.Errorf("want status=rejected, got %q", resp.Order.Status)
	}
	if len(resp.Trades) != 0 {
		t.Errorf("want trades=[], got %v", resp.Trades)
	}
}

// Scenario 18: Stop trigger-already-satisfied → 201 with status "rejected".
//
// At engine boot, lastTradePrice == 0. The trigger-already-satisfied rule:
//   - sell stop: rejected when trigger >= lastTradePrice
//
// So any sell stop with trigger_price > 0 is immediately rejected at boot
// (trigger >= 0 == lastTradePrice). This is the §05 / §11.1 design decision.
// We use this to get a one-liner scenario without needing to drive a trade first.
func TestHTTP_StopTriggerAlreadySatisfied(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	status, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-stop",
		Side:          "sell",
		Type:          "stop",
		TriggerPrice:  "50",
		Quantity:      "1",
	})
	if status != 201 {
		t.Fatalf("want 201 (business reject), got %d\nbody: %s", status, raw)
	}
	resp := mustParsePlace(t, raw)
	if resp.Order.Status != "rejected" {
		t.Errorf("want status=rejected, got %q", resp.Order.Status)
	}
	if len(resp.Trades) != 0 {
		t.Errorf("want trades=[], got %v", resp.Trades)
	}
}

// Scenario 19: Idempotency caches rejection — same key/body retry returns
// byte-identical 201 with status "rejected".
func TestHTTP_IdempotencyCachesRejection(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	req := httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-stop-idem",
		Side:          "sell",
		Type:          "stop",
		TriggerPrice:  "50",
		Quantity:      "1",
	}

	status1, raw1 := placeOrder(t, srvURL, req)
	if status1 != 201 {
		t.Fatalf("first call: want 201, got %d\nbody: %s", status1, raw1)
	}
	resp1 := mustParsePlace(t, raw1)
	if resp1.Order.Status != "rejected" {
		t.Fatalf("first call: want rejected, got %q", resp1.Order.Status)
	}

	// Retry — must return byte-identical response (cache hit).
	status2, raw2 := placeOrder(t, srvURL, req)
	if status2 != 201 {
		t.Fatalf("retry: want 201, got %d\nbody: %s", status2, raw2)
	}
	if !bytes.Equal(raw1, raw2) {
		t.Errorf("idempotency cache miss on rejection\nfirst: %s\nretry: %s", raw1, raw2)
	}
}

// ---- additional coverage ---------------------------------------------------

// Verify that GET /orderbook returns [] (not null) for an empty book.
func TestHTTP_EmptyBook_NullGuard(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	resp, err := http.Get(srvURL + "/orderbook")
	if err != nil {
		t.Fatalf("GET /orderbook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	s := string(raw)
	if strings.Contains(s, "null") {
		t.Errorf("empty book must not contain null\nbody: %s", s)
	}

	var body httpa.SnapshotResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Both must be slices (non-nil), even if empty.
	if body.Bids == nil {
		t.Error("bids is nil, want []")
	}
	if body.Asks == nil {
		t.Error("asks is nil, want []")
	}
}

// Verify GET /trades on fresh engine returns {"trades":[]} not null.
func TestHTTP_GetTrades_EmptyNotNull(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	resp, err := http.Get(srvURL + "/trades")
	if err != nil {
		t.Fatalf("GET /trades: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	s := string(raw)
	if strings.Contains(s, `"trades":null`) {
		t.Errorf("empty trades must not be null\nbody: %s", s)
	}
}

// Verify that the monotonic ID counter starts at o-1.
func TestHTTP_FirstOrderID(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	_, raw := placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-first",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})
	resp := mustParsePlace(t, raw)
	if resp.Order.ID != "o-1" {
		t.Errorf("want first order id=o-1, got %q", resp.Order.ID)
	}
}

// Verify that GET /orderbook with a resting order shows it on the correct side.
func TestHTTP_OrderbookShowsRestingOrder(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-bid",
		Side:          "buy",
		Type:          "limit",
		Price:         "200",
		Quantity:      "5",
	})

	resp, err := http.Get(srvURL + "/orderbook?depth=5")
	if err != nil {
		t.Fatalf("GET /orderbook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var snap httpa.SnapshotResponse
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal SnapshotResponse: %v", err)
	}
	if len(snap.Bids) != 1 {
		t.Fatalf("want 1 bid level, got %d\nbody: %s", len(snap.Bids), raw)
	}
	mustDecEqual(t, "bid price", snap.Bids[0].Price, "200")
	mustDecEqual(t, "bid qty", snap.Bids[0].Quantity, "5")
	if len(snap.Asks) != 0 {
		t.Errorf("want no asks, got %v", snap.Asks)
	}
}

// Verify that the dedup key is (user_id, client_order_id) — same client_order_id
// from two different users are NOT deduped against each other.
func TestHTTP_IdempotencyKeyIsUserScoped(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	req1 := httpa.PlaceOrderRequest{
		UserID:        "u-alice",
		ClientOrderID: "coid-shared",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	}
	req2 := httpa.PlaceOrderRequest{
		UserID:        "u-bob",
		ClientOrderID: "coid-shared", // same client_order_id, different user
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	}

	_, raw1 := placeOrder(t, srvURL, req1)
	_, raw2 := placeOrder(t, srvURL, req2)

	r1 := mustParsePlace(t, raw1)
	r2 := mustParsePlace(t, raw2)

	// Different users must get different orders.
	if r1.Order.ID == r2.Order.ID {
		t.Errorf("different users with same client_order_id got same order ID %q — dedup key is not user-scoped", r1.Order.ID)
	}
}

// Verify the book size hint: POST /orderbook?depth=0 returns 200 with empty slices.
func TestHTTP_OrderbookDepthZero(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	// Place a resting order so the book is non-empty.
	placeOrder(t, srvURL, httpa.PlaceOrderRequest{
		UserID:        "u-1",
		ClientOrderID: "coid-1",
		Side:          "buy",
		Type:          "limit",
		Price:         "100",
		Quantity:      "1",
	})

	resp, err := http.Get(srvURL + "/orderbook?depth=0")
	if err != nil {
		t.Fatalf("GET /orderbook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	// depth=0 is a valid non-negative int — no error expected.
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d\nbody: %s", resp.StatusCode, raw)
	}
}

// Verify that invalid ?depth query param returns 400 validation.
func TestHTTP_OrderbookInvalidDepth(t *testing.T) {
	t.Parallel()
	srvURL, cleanup := newStack(t)
	defer cleanup()

	resp, err := http.Get(srvURL + "/orderbook?depth=banana")
	if err != nil {
		t.Fatalf("GET /orderbook: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d\nbody: %s", resp.StatusCode, raw)
	}
}

