package ports

// IDGenerator produces formatted, monotonically-increasing identifiers
// for orders and trades. Format: "o-<n>" and "t-<n>" where <n> is a
// uint64 starting at 1 (see docs/system_design/02-data-structures.md
// "ID format").
type IDGenerator interface {
	// NextOrderID returns the next order identifier in the form "o-<n>".
	NextOrderID() string
	// NextTradeID returns the next trade identifier in the form "t-<n>".
	NextTradeID() string
}
