package engine

import "errors"

// ErrTooManyOrders is returned by Place when adding a new resting limit or
// stop-limit order would exceed the engine's maxOpenOrders cap and no partial
// fill has yet occurred (pre-mutation path). It maps to HTTP 429 in T-014.
var ErrTooManyOrders = errors.New("engine: too many open orders")

// ErrTooManyStops is returned by Place when arming a new stop or stop-limit
// order would exceed the engine's maxArmedStops cap. It maps to HTTP 429 in
// T-014.
var ErrTooManyStops = errors.New("engine: too many armed stops")

// ErrOrderNotFound is returned by Cancel when the supplied orderID is not
// present in either the resting order book (e.byID) or the armed stop book
// (e.stops). This includes the case where the order previously existed but has
// since been fully filled and removed from e.byID.
var ErrOrderNotFound = errors.New("engine: order not found")

// ErrAlreadyTerminal is returned by Cancel if an order found in e.byID already
// carries a terminal status (Filled, Cancelled, or Rejected). By engine
// invariant this should never happen — resting orders in e.byID are always
// non-terminal — so this error signals a programmer error / invariant
// violation. It is kept here as a defensive guard.
var ErrAlreadyTerminal = errors.New("engine: order is already in a terminal state")
