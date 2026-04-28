package http

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"matching-engine/internal/app"
	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
	"matching-engine/internal/engine"
	"matching-engine/internal/engine/book"
)

// maxDecimalValue is 10^15, the upper bound for quantity, price, and trigger_price.
var maxDecimalValue = decimal.NewFromInt(1_000_000_000_000_000)

// writeJSON writes a JSON-encoded response with the given HTTP status code.
// It sets Content-Type to application/json before writing the header.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("http: writeJSON encode failed: %v", err)
	}
}

// isASCIIPrintable returns true when every byte of s is in the ASCII printable
// range 0x20–0x7E. We iterate over []byte (not range s) so we examine raw
// bytes, not decoded runes — the spec requires byte-level enforcement.
func isASCIIPrintable(s string) bool {
	for _, b := range []byte(s) {
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
}

// validatePlaceRequest runs the validation pipeline (steps 0a–8 from the spec)
// after JSON decoding. It returns the parsed app.PlaceCommand, an error message
// (non-empty only on failure), and a bool indicating success.
func validatePlaceRequest(req PlaceOrderRequest) (app.PlaceCommand, string, bool) {
	// Step 0a: client_order_id
	switch {
	case req.ClientOrderID == "":
		return app.PlaceCommand{}, "client_order_id is required", false
	case len(req.ClientOrderID) > 64:
		return app.PlaceCommand{}, "client_order_id must be 1..64 chars", false
	case !isASCIIPrintable(req.ClientOrderID):
		return app.PlaceCommand{}, "client_order_id must be ASCII printable", false
	}

	// Step 0b: user_id
	switch {
	case req.UserID == "":
		return app.PlaceCommand{}, "user_id must be 1..128 ASCII printable chars", false
	case len(req.UserID) > 128:
		return app.PlaceCommand{}, "user_id must be 1..128 ASCII printable chars", false
	case !isASCIIPrintable(req.UserID):
		return app.PlaceCommand{}, "user_id must be 1..128 ASCII printable chars", false
	}

	// Step 2: Parse decimals — quantity is always required.
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		return app.PlaceCommand{}, "quantity is not a valid decimal: " + err.Error(), false
	}

	var price decimal.Decimal
	if req.Price != "" {
		price, err = decimal.NewFromString(req.Price)
		if err != nil {
			return app.PlaceCommand{}, "price is not a valid decimal: " + err.Error(), false
		}
	}

	var triggerPrice decimal.Decimal
	if req.TriggerPrice != "" {
		triggerPrice, err = decimal.NewFromString(req.TriggerPrice)
		if err != nil {
			return app.PlaceCommand{}, "trigger_price is not a valid decimal: " + err.Error(), false
		}
	}

	// Step 3: Validate enums — side.
	var side domain.Side
	switch req.Side {
	case "buy":
		side = domain.Buy
	case "sell":
		side = domain.Sell
	default:
		return app.PlaceCommand{}, "side must be one of: buy, sell", false
	}

	// Step 3: Validate enums — type.
	var orderType domain.Type
	switch req.Type {
	case "limit":
		orderType = domain.Limit
	case "market":
		orderType = domain.Market
	case "stop":
		orderType = domain.Stop
	case "stop_limit":
		orderType = domain.StopLimit
	default:
		return app.PlaceCommand{}, "type must be one of: limit, market, stop, stop_limit", false
	}

	// Step 4: Type-specific field constraints.
	switch orderType {
	case domain.Limit:
		if req.TriggerPrice != "" {
			return app.PlaceCommand{}, "trigger_price must not be set for limit orders", false
		}
		if !price.IsPositive() {
			return app.PlaceCommand{}, "price must be > 0 for limit orders", false
		}
	case domain.Market:
		if req.Price != "" {
			return app.PlaceCommand{}, "price must not be set for market orders", false
		}
		if req.TriggerPrice != "" {
			return app.PlaceCommand{}, "trigger_price must not be set for market orders", false
		}
	case domain.Stop:
		if req.Price != "" {
			return app.PlaceCommand{}, "price must not be set for stop orders", false
		}
		if !triggerPrice.IsPositive() {
			return app.PlaceCommand{}, "trigger_price must be > 0 for stop orders", false
		}
	case domain.StopLimit:
		if !price.IsPositive() {
			return app.PlaceCommand{}, "price must be > 0 for stop_limit orders", false
		}
		if !triggerPrice.IsPositive() {
			return app.PlaceCommand{}, "trigger_price must be > 0 for stop_limit orders", false
		}
	}

	// Step 5: Numeric upper bounds (quantity must also be > 0).
	if !qty.IsPositive() {
		return app.PlaceCommand{}, "quantity must be > 0", false
	}
	if qty.GreaterThan(maxDecimalValue) {
		return app.PlaceCommand{}, "quantity exceeds maximum 1000000000000000", false
	}
	if req.Price != "" && price.GreaterThan(maxDecimalValue) {
		return app.PlaceCommand{}, "price exceeds maximum 1000000000000000", false
	}
	if req.TriggerPrice != "" && triggerPrice.GreaterThan(maxDecimalValue) {
		return app.PlaceCommand{}, "trigger_price exceeds maximum 1000000000000000", false
	}

	// Step 6: Decimal precision ≤ 18 places. A negative exponent whose absolute
	// value exceeds 18 means the value has more than 18 fractional digits.
	if qty.Exponent() < -18 {
		return app.PlaceCommand{}, "quantity precision exceeds 18 decimal places", false
	}
	if req.Price != "" && price.Exponent() < -18 {
		return app.PlaceCommand{}, "price precision exceeds 18 decimal places", false
	}
	if req.TriggerPrice != "" && triggerPrice.Exponent() < -18 {
		return app.PlaceCommand{}, "trigger_price precision exceeds 18 decimal places", false
	}

	// Step 7: Build app.PlaceCommand.
	cmd := app.PlaceCommand{
		PlaceCommand: engine.PlaceCommand{
			UserID:       req.UserID,
			Side:         side,
			Type:         orderType,
			Price:        price,
			TriggerPrice: triggerPrice,
			Quantity:     qty,
		},
		ClientOrderID: req.ClientOrderID,
	}
	return cmd, "", true
}

// orderToDTO converts a domain.Order to an OrderDTO wire representation.
// clientOrderID is stamped in from the request (Option B); the engine's Order
// does not carry this field.
func orderToDTO(o *domain.Order, clientOrderID string) OrderDTO {
	dto := OrderDTO{
		ID:                o.ID,
		UserID:            o.UserID,
		ClientOrderID:     clientOrderID,
		Side:              o.Side.String(),
		Type:              o.Type.String(),
		Quantity:          o.Quantity.String(),
		RemainingQuantity: o.RemainingQuantity.String(),
		Status:            o.Status.String(),
		CreatedAt:         o.CreatedAt.UTC().Format(time.RFC3339),
	}
	// Only include price/trigger_price when non-zero (mirrors omitempty on the DTO).
	if !o.Price.IsZero() {
		dto.Price = o.Price.String()
	}
	if !o.TriggerPrice.IsZero() {
		dto.TriggerPrice = o.TriggerPrice.String()
	}
	return dto
}

// tradesToDTO converts a slice of domain.Trade pointers to a []TradeDTO.
// The result is always a non-nil slice so that JSON serialises as [] not null.
func tradesToDTO(in []*domain.Trade) []TradeDTO {
	out := make([]TradeDTO, 0, len(in))
	for _, t := range in {
		out = append(out, TradeDTO{
			ID:           t.ID,
			TakerOrderID: t.TakerOrderID,
			MakerOrderID: t.MakerOrderID,
			Price:        t.Price.String(),
			Quantity:     t.Quantity.String(),
			TakerSide:    t.TakerSide.String(),
			CreatedAt:    t.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// levelsToDTO converts a slice of book.LevelSnapshot to a []LevelDTO.
// The result is always a non-nil slice so that JSON serialises as [] not null.
func levelsToDTO(in []book.LevelSnapshot) []LevelDTO {
	out := make([]LevelDTO, 0, len(in))
	for _, l := range in {
		out = append(out, LevelDTO{
			Price:    l.Price.String(),
			Quantity: l.Quantity.String(),
		})
	}
	return out
}

// handlePlace returns an http.HandlerFunc that processes POST /orders.
func handlePlace(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Step 1: Decode JSON. DisallowUnknownFields rejects unknown keys.
		var req PlaceOrderRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				WriteError(w, http.StatusRequestEntityTooLarge, CodeRequestTooLarge, "request body exceeds 65536 bytes")
				return
			}
			WriteError(w, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}

		// Steps 0a–8: validation pipeline.
		cmd, msg, ok := validatePlaceRequest(req)
		if !ok {
			WriteError(w, http.StatusBadRequest, CodeValidation, msg)
			return
		}

		// Step 9: forward to app service.
		result, err := svc.Place(cmd)
		if err != nil {
			switch {
			case errors.Is(err, engine.ErrTooManyOrders):
				WriteError(w, http.StatusTooManyRequests, CodeTooManyOrders, err.Error())
			case errors.Is(err, engine.ErrTooManyStops):
				WriteError(w, http.StatusTooManyRequests, CodeTooManyStops, err.Error())
			default:
				log.Printf("place: %v", err)
				WriteError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			}
			return
		}

		// Option B: stamp client_order_id from the validated request into the DTO.
		resp := PlaceOrderResponse{
			Order:  orderToDTO(result.Order, req.ClientOrderID),
			Trades: tradesToDTO(result.Trades),
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// handleCancel returns an http.HandlerFunc that processes DELETE /orders/{id}.
func handleCancel(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		order, err := svc.Cancel(id)
		if err != nil {
			switch {
			case errors.Is(err, engine.ErrOrderNotFound):
				WriteError(w, http.StatusNotFound, CodeNotFound, err.Error())
			case errors.Is(err, engine.ErrAlreadyTerminal):
				WriteError(w, http.StatusConflict, CodeConflict, err.Error())
			default:
				log.Printf("cancel: %v", err)
				WriteError(w, http.StatusInternalServerError, CodeInternal, "internal error")
			}
			return
		}
		// Cancelled orders have no client_order_id at the engine layer; empty
		// string is acceptable here — the cancel response does not require it.
		resp := CancelOrderResponse{Order: orderToDTO(order, "")}
		writeJSON(w, http.StatusOK, resp)
	}
}

// parseIntParam parses the named query parameter. Returns defaultVal when the
// parameter is absent. Returns -1 and writes a 400 response when the value is
// not a valid non-negative integer, or exceeds cap.
func parseIntParam(w http.ResponseWriter, r *http.Request, name string, defaultVal, capVal int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return defaultVal, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		WriteError(w, http.StatusBadRequest, CodeValidation, name+" must be a non-negative integer")
		return 0, false
	}
	if n > capVal {
		n = capVal
	}
	return n, true
}

// handleSnapshot returns an http.HandlerFunc that processes GET /orderbook.
func handleSnapshot(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depth, ok := parseIntParam(w, r, "depth", 10, 1000)
		if !ok {
			return
		}
		bids, asks := svc.Snapshot(depth)
		resp := SnapshotResponse{
			Bids: levelsToDTO(bids),
			Asks: levelsToDTO(asks),
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleTrades returns an http.HandlerFunc that processes GET /trades.
func handleTrades(svc *app.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, ok := parseIntParam(w, r, "limit", 50, 1000)
		if !ok {
			return
		}
		trades := svc.Trades(limit)
		resp := TradesResponse{Trades: tradesToDTO(trades)}
		writeJSON(w, http.StatusOK, resp)
	}
}
