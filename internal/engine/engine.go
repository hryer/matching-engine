// Package engine is the price-time-FIFO matching engine described in
// docs/system_design/04-matching-algorithm.md, /05-stop-orders.md, and
// /06-concurrency-and-determinism.md.
//
// CONCURRENCY CONTRACT.
//
//  1. The engine is protected by a single sync.Mutex (e.mu). Every
//     public method (Place, Cancel, Snapshot, Trades) takes e.mu at
//     entry and releases it via defer at exit.
//
//  2. No public method calls another public method (no re-entry into
//     the lock). The engine never spawns a goroutine. All matching
//     work runs on the caller's goroutine, serialised by the mutex.
//
//  3. Unexported helpers (match, appendTrade, updateLastTradePrice,
//     drainTriggeredStops) ASSUME e.mu is held. They are private to
//     the engine package and must never be called from outside it.
//
//  4. Every mutation of book, stops, history, lastTradePrice, byID,
//     openOrders, armedStops, or seqCounter happens under e.mu.
//
//  5. The dedup map in app.Service has its own mutex (dedupMu)
//     acquired BEFORE e.mu, on the Place path only. See
//     docs/system_design/06-concurrency-and-determinism.md for the
//     full lock-ordering argument and why deadlock is structurally
//     impossible.
//
// DETERMINISM CONTRACT.
//
//   - Time is read only via e.clock.Now(). Never call time.Now()
//     directly anywhere in this package.
//   - IDs come only from e.ids. No counter increment outside that
//     interface (other than e.seqCounter, which is the placement seq
//     and is engine-internal).
//   - Map iteration order is never observable: e.byID is used only
//     for O(1) lookup, never ranged-over for ordered work. Ordered
//     traversal goes through book / stops btrees.
//   - Decimal comparisons use .Cmp / .Equal / .IsZero only — never ==.
//
// RESOURCE BOUNDS.
//
//	openOrders == len(e.byID) after every public method returns.
//	armedStops == e.stops.Len() after every public method returns.
//	Cap checks are at the user-call boundary in Place; a cascade
//	triggered mid-Place may transiently exceed the cap (back-pressure
//	semantics — see docs/tasks/T-010-engine-core.md).
package engine

import (
	"sync"

	"matching-engine/internal/domain"
	"matching-engine/internal/domain/decimal"
	"matching-engine/internal/engine/book"
	"matching-engine/internal/engine/stops"
	"matching-engine/internal/ports"
)

// Engine is the single-instrument matching engine. It is safe for concurrent
// use: all public methods serialise through e.mu.
type Engine struct {
	mu sync.Mutex

	book           *book.OrderBook
	stops          *stops.StopBook
	byID           map[string]*domain.Order // resting orders only — armed stops live in stops (StopBook owns its own byID)
	lastTradePrice decimal.Decimal

	history ports.EventPublisher
	clock   ports.Clock
	ids     ports.IDGenerator

	seqCounter uint64

	openOrders    int
	armedStops    int
	maxOpenOrders int
	maxArmedStops int
}

// Deps carries the external dependencies that the engine requires. The caller
// (T-016) is responsible for supplying production limits. There is no
// defaulting: a MaxOpenOrders of 0 means every Limit/StopLimit placement is
// immediately rejected.
type Deps struct {
	Clock         ports.Clock
	IDs           ports.IDGenerator
	Publisher     ports.EventPublisher
	MaxOpenOrders int
	MaxArmedStops int
}

// PlaceCommand is the input to Place. The HTTP layer (T-013/T-014) is
// responsible for input validation. The engine assumes valid inputs (positive
// quantities, type/price coherence). No engine-level validation of business
// inputs beyond what is required for state-machine correctness.
type PlaceCommand struct {
	UserID       string
	Side         domain.Side
	Type         domain.Type
	Price        decimal.Decimal // zero for Market / Stop
	TriggerPrice decimal.Decimal // zero for Limit / Market
	Quantity     decimal.Decimal
}

// PlaceResult is returned by Place on success (nil error). Order is always
// non-nil. Trades is nil (not empty slice) when no fills were produced.
type PlaceResult struct {
	Order  *domain.Order
	Trades []*domain.Trade
}

// New constructs an empty Engine. lastTradePrice is initialised to
// decimal.Zero, which means every sell stop placed before the first trade is
// rejected (any positive trigger >= 0 fires the already-satisfied rule — see
// §11.1 of T-010-PLAN.md for discussion). This is the correct behaviour per
// spec; the production engine boots warm from a snapshot.
func New(deps Deps) *Engine {
	return &Engine{
		book:           book.New(),
		stops:          stops.New(),
		byID:           make(map[string]*domain.Order),
		lastTradePrice: decimal.Zero,
		history:        deps.Publisher,
		clock:          deps.Clock,
		ids:            deps.IDs,
		maxOpenOrders:  deps.MaxOpenOrders,
		maxArmedStops:  deps.MaxArmedStops,
	}
}

// Place processes a new order placement command. It is the single entry point
// for all order types and dispatches to placeStop or placeMatchable based on
// cmd.Type.
//
// Return values:
//   - (PlaceResult{Order: o}, nil)   — successful placement or terminal rejection
//     (status = Rejected for trigger-already-satisfied stops; status = Resting,
//     PartiallyFilled, Filled, or Cancelled for limit/market paths).
//   - (PlaceResult{}, ErrTooManyOrders) — cap hit before any state mutation
//     (Limit/Market with no prior fills).
//   - (PlaceResult{}, ErrTooManyStops)  — cap hit on Stop/StopLimit arm path.
//
// ErrTooManyOrders is NOT returned when a partial fill precedes the cap-check
// (decision (b), T-010-PLAN.md §1): the partial fill is kept and the
// remainder is silently truncated.
func (e *Engine) Place(cmd PlaceCommand) (PlaceResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.seqCounter++
	order := &domain.Order{
		ID:                e.ids.NextOrderID(),
		UserID:            cmd.UserID,
		Side:              cmd.Side,
		Type:              cmd.Type,
		Price:             cmd.Price,
		TriggerPrice:      cmd.TriggerPrice,
		Quantity:          cmd.Quantity,
		RemainingQuantity: cmd.Quantity,
		CreatedAt:         e.clock.Now(),
	}
	order.SetSeq(e.seqCounter)

	switch cmd.Type {
	case domain.Stop, domain.StopLimit:
		return e.placeStop(order)
	case domain.Limit, domain.Market:
		return e.placeMatchable(order)
	default:
		// Unreachable: the HTTP layer (T-013/T-014) validates Type before
		// calling Place. Defensive: reject unknown types rather than panicking.
		order.Status = domain.StatusRejected
		return PlaceResult{Order: order}, nil
	}
}

// placeStop handles Stop and StopLimit placement. It is called with e.mu held
// and must not acquire it again.
//
// Algorithm (T-010-PLAN.md §7, Step 2a):
//  1. Reject immediately if the trigger is already satisfied at the current
//     lastTradePrice (returns PlaceResult{Order: rejected}, nil — NOT an
//     error, so the dedup layer in T-012 can cache the rejection body).
//  2. Cap-check BEFORE arming — no state has been mutated yet, so
//     ErrTooManyStops is clean.
//  3. Arm: insert into stop book, increment armedStops.
func (e *Engine) placeStop(order *domain.Order) (PlaceResult, error) {
	// Trigger-already-satisfied check.
	// Buy stop fires when lastTradePrice rises to / above TriggerPrice.
	// If trigger <= lastTradePrice it would fire immediately — reject.
	// Sell stop fires when lastTradePrice falls to / below TriggerPrice.
	// If trigger >= lastTradePrice it would fire immediately — reject.
	// NOTE: at engine init lastTradePrice == 0, so every positive-trigger
	// sell stop is rejected (design decision per §11.1 of the plan).
	if order.Side == domain.Buy && order.TriggerPrice.LessThanOrEqual(e.lastTradePrice) {
		order.Status = domain.StatusRejected
		return PlaceResult{Order: order}, nil
	}
	if order.Side == domain.Sell && order.TriggerPrice.GreaterThanOrEqual(e.lastTradePrice) {
		order.Status = domain.StatusRejected
		return PlaceResult{Order: order}, nil
	}

	// Cap-check before any state mutation.
	if e.armedStops+1 > e.maxArmedStops {
		return PlaceResult{}, ErrTooManyStops
	}

	// Arm the stop.
	order.Status = domain.StatusArmed
	e.stops.Insert(order)
	e.armedStops++ // §4: Place(stop/stop_limit) arms → armedStops +1
	return PlaceResult{Order: order}, nil
}

// placeMatchable handles Limit and Market placement. It is called with e.mu
// held and must not acquire it again.
//
// Algorithm (T-010-PLAN.md §7, Step 2b):
//  1. Run the price-time matching loop (match). match mutates
//     order.RemainingQuantity and sets terminal status for Filled,
//     Cancelled (STP), Rejected (Market-no-trades), and Market-PartiallyFilled.
//     For the Limit case, match does NOT set terminal status — that is our
//     responsibility because the cap-check is a Place-time policy concern.
//  2. Dispatch on the post-match state:
//     a. Terminal status set by match → return directly.
//     b. Limit, no fills (len(trades)==0): cap-check, then rest or reject.
//     c. Limit, partial fill: cap-check, then rest or truncate (decision (b)).
func (e *Engine) placeMatchable(order *domain.Order) (PlaceResult, error) {
	trades := e.match(order)

	switch {
	case order.Status == domain.StatusCancelled:
		// STP fired in match. trades may be non-empty (fills against other
		// makers completed before the self-match was detected).
		return PlaceResult{Order: order, Trades: trades}, nil

	case order.Status == domain.StatusFilled:
		// Limit or Market fully consumed by the book.
		return PlaceResult{Order: order, Trades: trades}, nil

	case order.Status == domain.StatusRejected:
		// Market order with no opposing liquidity; match set this.
		return PlaceResult{Order: order, Trades: trades}, nil

	case order.Status == domain.StatusPartiallyFilled:
		// Market order that consumed some but not all available liquidity;
		// remainder is dropped (Market never rests). match set this.
		return PlaceResult{Order: order, Trades: trades}, nil

	case order.Type == domain.Limit && len(trades) == 0:
		// Limit order found no crosses; would rest at original size.
		// Cap-check first: no state has been mutated, so ErrTooManyOrders is
		// safe to return without a compensating rollback.
		if e.openOrders+1 > e.maxOpenOrders {
			return PlaceResult{}, ErrTooManyOrders
		}
		order.Status = domain.StatusResting
		e.book.Insert(order)
		e.byID[order.ID] = order
		e.openOrders++ // §4: Place(limit) rests at original size → openOrders +1
		return PlaceResult{Order: order, Trades: trades}, nil

	case order.Type == domain.Limit:
		// Limit order produced at least one fill; remainder > 0 means it did
		// not fully fill and would normally rest.
		//
		// OPEN QUESTION resolution — option (b), T-010-PLAN.md §1:
		//   Trades are real: appendTrade already published them and any
		//   cascaded stops have already fired. There is no rollback. The cap
		//   is a back-pressure signal at the user-call boundary; once fills
		//   have occurred the correct behaviour is to keep them and truncate
		//   the rest rather than return an error that implies "nothing
		//   happened".
		//
		// §04 status transition table analogue: PartiallyFilled-truncated is
		// identical to Market-PartiallyFilled — trades land, remainder is
		// dropped, no resting state.
		if e.openOrders+1 > e.maxOpenOrders {
			// Cap hit after partial fill: truncate remainder, keep trades.
			order.Status = domain.StatusPartiallyFilled
			// §4: cap hit, no insert → openOrders unchanged.
			return PlaceResult{Order: order, Trades: trades}, nil
		}
		// Cap not hit: rest the remainder.
		order.Status = domain.StatusPartiallyFilled
		e.book.Insert(order)
		e.byID[order.ID] = order
		e.openOrders++ // §4: Place(limit) partial fill, cap NOT hit → openOrders +1
		return PlaceResult{Order: order, Trades: trades}, nil
	}

	// Unreachable: match sets status for every terminal path; the Limit cases
	// above are exhaustive given match's contract (does not set status for the
	// Limit rest case, leaves it at the zero value StatusArmed which cannot
	// match any case above). If this fires, match's status-setting policy has
	// drifted from its contract — catch it loudly in development.
	//
	// Status matrix that would reach here: Type==Market with status still at
	// the zero value (StatusArmed), which match must never allow.
	panic("engine: placeMatchable reached unreachable branch — match contract violated")
}

// Cancel cancels a resting or armed order by ID. It is the user-facing
// cancellation path.
//
// Lookup order: e.byID first (resting orders), then e.stops (armed stops).
// Cancelled orders are removed from the relevant data structure immediately
// so that counter invariants hold at return.
//
// Returns:
//   - (*domain.Order with Status=Cancelled, nil) on success.
//   - (nil, ErrOrderNotFound) if the ID is unknown in both e.byID and e.stops.
//   - (nil, ErrAlreadyTerminal) if the order in e.byID already carries a
//     terminal status. By engine invariant this is impossible (resting orders
//     are never terminal), but the guard defends against future bugs.
func (e *Engine) Cancel(orderID string) (*domain.Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Resting order path.
	if o, ok := e.byID[orderID]; ok {
		// Defensive: resting orders must be non-terminal. If this fires, an
		// engine invariant has been violated — surface it rather than silently
		// operating on corrupted state.
		if o.Status == domain.StatusFilled ||
			o.Status == domain.StatusCancelled ||
			o.Status == domain.StatusRejected {
			return nil, ErrAlreadyTerminal
		}
		// Remove from book and ID map.
		e.book.Cancel(o)      // decrements level.Total, prunes empty level
		delete(e.byID, o.ID)
		e.openOrders-- // §4: Cancel resting order → openOrders -1
		o.Status = domain.StatusCancelled
		return o, nil
	}

	// Armed stop path.
	if o, ok := e.stops.Get(orderID); ok {
		// stops.Cancel removes from both byID and the btree atomically.
		e.stops.Cancel(orderID)
		e.armedStops-- // §4: Cancel armed stop → armedStops -1
		o.Status = domain.StatusCancelled
		return o, nil
	}

	return nil, ErrOrderNotFound
}

// Snapshot returns the top depth levels per side from the resting order book.
// Armed stops are NOT included — Snapshot reflects only the limit order book.
// The book's Snapshot implementation clamps depth to [0, 1000] internally;
// the engine does not add a second clamp layer (§11.5 of the plan).
func (e *Engine) Snapshot(depth int) (bids, asks []book.LevelSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.book.Snapshot(depth)
}

// Trades returns up to limit of the most recently published trades, NEWEST
// FIRST. limit is clamped to [0, 1000]: negative values and zero return an
// empty slice (delegated to the publisher's Recent implementation), and values
// above 1000 are silently capped per §3 of the plan.
//
// §11.4: Trades(-5) returns [] with no error. No wrapper validation is added;
// the publisher's Recent already returns [] for limit <= 0.
func (e *Engine) Trades(limit int) []*domain.Trade {
	e.mu.Lock()
	defer e.mu.Unlock()

	if limit > 1000 {
		limit = 1000
	}
	return e.history.Recent(limit)
}
