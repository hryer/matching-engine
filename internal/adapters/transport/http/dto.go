// Package http contains the HTTP transport layer: DTOs, error helpers, and middleware.
// Conversion from domain types to DTOs lives in T-014 (handlers.go / dto_convert.go).
// This file is deliberately free of internal/domain imports — DTOs are plain JSON-tagged structs.
package http

// PlaceOrderRequest is the decoded body for POST /orders.
// ClientOrderID has no omitempty — an empty string must serialise so validation
// can reject it with a clear "client_order_id is required" message.
type PlaceOrderRequest struct {
	UserID        string `json:"user_id"`
	ClientOrderID string `json:"client_order_id"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         string `json:"price,omitempty"`
	TriggerPrice  string `json:"trigger_price,omitempty"`
	Quantity      string `json:"quantity"`
}

// OrderDTO is the wire representation of a single order.
// Price and TriggerPrice carry omitempty so they are absent for market orders
// and stop orders respectively, matching the brief's example payloads.
type OrderDTO struct {
	ID                string `json:"id"`
	UserID            string `json:"user_id"`
	ClientOrderID     string `json:"client_order_id"`
	Side              string `json:"side"`
	Type              string `json:"type"`
	Price             string `json:"price,omitempty"`
	TriggerPrice      string `json:"trigger_price,omitempty"`
	Quantity          string `json:"quantity"`
	RemainingQuantity string `json:"remaining_quantity"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
}

// TradeDTO is the wire representation of a single executed trade.
type TradeDTO struct {
	ID           string `json:"id"`
	TakerOrderID string `json:"taker_order_id"`
	MakerOrderID string `json:"maker_order_id"`
	Price        string `json:"price"`
	Quantity     string `json:"quantity"`
	TakerSide    string `json:"taker_side"`
	CreatedAt    string `json:"created_at"`
}

// LevelDTO is one price level in the order-book snapshot.
type LevelDTO struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

// PlaceOrderResponse is the body for a successful POST /orders (HTTP 201).
type PlaceOrderResponse struct {
	Order  OrderDTO   `json:"order"`
	Trades []TradeDTO `json:"trades"`
}

// CancelOrderResponse is the body for a successful DELETE /orders/{id} (HTTP 200).
type CancelOrderResponse struct {
	Order OrderDTO `json:"order"`
}

// SnapshotResponse is the body for GET /orderbook.
type SnapshotResponse struct {
	Bids []LevelDTO `json:"bids"`
	Asks []LevelDTO `json:"asks"`
}

// TradesResponse is the body for GET /trades.
type TradesResponse struct {
	Trades []TradeDTO `json:"trades"`
}

// ErrorResponse is the JSON shape for every error reply.
// The Code field is one of the Code* constants in errors.go.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}
