package domain

import (
	"container/list"
	"time"

	"matching-engine/internal/domain/decimal"
)

// Order is a placed order in any of the four supported types
// (Limit, Market, Stop, StopLimit).
//
// Unexported fields seq, elem, level are mutated by the engine and
// engine/book package respectively. They are exposed via accessor
// methods so other packages can set/read them without exporting raw
// pointers.
type Order struct {
	ID                string
	UserID            string
	Side              Side
	Type              Type
	Price             decimal.Decimal // zero for Market
	TriggerPrice      decimal.Decimal // zero unless Stop / StopLimit
	Quantity          decimal.Decimal // original
	RemainingQuantity decimal.Decimal
	Status            Status
	CreatedAt         time.Time

	seq   uint64
	elem  *list.Element
	level any // back-pointer to *PriceLevel; "any" to avoid import cycle with engine/book
}

// Seq returns the monotonic placement sequence assigned by the engine.
// Used as a deterministic FIFO tiebreaker and stop cascade ordering key.
func (o *Order) Seq() uint64 { return o.seq }

// SetSeq assigns the placement sequence. Called once by the engine on Place.
func (o *Order) SetSeq(s uint64) { o.seq = s }

// Elem returns the back-pointer into the price level's FIFO list.
// nil when the order is not resting on the book.
func (o *Order) Elem() *list.Element { return o.elem }

// SetElem stores the FIFO list element pointer. Called by engine/book on insert.
func (o *Order) SetElem(e *list.Element) { o.elem = e }

// Level returns the back-pointer to the *PriceLevel this order rests in.
// Type-cast required: o.Level().(*book.PriceLevel). Nil means not resting.
func (o *Order) Level() any { return o.level }

// SetLevel stores the price level back-pointer. Called by engine/book on insert.
func (o *Order) SetLevel(l any) { o.level = l }
