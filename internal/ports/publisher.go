package ports

import "matching-engine/internal/domain"

// EventPublisher receives trades produced by the engine. The v1 in-memory
// adapter keeps the last 10,000 trades; v2 may route to Kafka or WebSocket.
//
// Publish is fire-and-forget; the engine does not handle failures. Recent
// returns the most recent up to `limit` trades, NEWEST FIRST.
type EventPublisher interface {
	// Publish records a trade. It is fire-and-forget: the engine does not
	// inspect or handle any failure.
	Publish(trade *domain.Trade)
	// Recent returns up to `limit` of the most recently published trades,
	// ordered NEWEST FIRST.
	Recent(limit int) []*domain.Trade
}
