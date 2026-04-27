// Package decimal is a thin alias wrapper over github.com/shopspring/decimal.
//
// The rest of the codebase imports this package instead of shopspring/decimal
// directly so the underlying library is swappable in one file (e.g. to
// cockroachdb/apd or integer minor units behind a Decimal facade).
//
// See docs/system_design/07-decimal-arithmetic.md for the rationale.
package decimal

import sd "github.com/shopspring/decimal"

// Decimal is a type alias (not a defined type) so all methods on the
// underlying shopspring/decimal.Decimal are preserved without forwarding shims.
type Decimal = sd.Decimal

// Re-exported values and constructors. These are values / function values,
// not type aliases.
var (
	Zero          = sd.Zero
	NewFromString = sd.NewFromString
	NewFromInt    = sd.NewFromInt
)
