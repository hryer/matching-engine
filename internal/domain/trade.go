package domain

import (
	"time"

	"matching-engine/internal/domain/decimal"
)

// Trade is an executed match between a taker order and a maker order.
// Trade.Price is always the MAKER's resting price (standard exchange convention).
type Trade struct {
	ID           string
	TakerOrderID string
	MakerOrderID string
	Price        decimal.Decimal
	Quantity     decimal.Decimal
	TakerSide    Side
	CreatedAt    time.Time
}
