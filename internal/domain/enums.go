package domain

import (
	"fmt"
	"strconv"
)

// Side represents the direction of an order. Stored as uint8 for cache
// friendliness; serialized as the wire strings "buy" / "sell".
type Side uint8

const (
	Buy  Side = iota // 0 -> "buy"
	Sell             // 1 -> "sell"
)

// String returns the wire-format representation of s. It is intended for
// readable test failures and log messages.
func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return fmt.Sprintf("Side(%d)", uint8(s))
	}
}

// MarshalJSON serializes a Side using the wire-format string.
func (s Side) MarshalJSON() ([]byte, error) {
	switch s {
	case Buy:
		return []byte(`"buy"`), nil
	case Sell:
		return []byte(`"sell"`), nil
	default:
		return nil, fmt.Errorf("domain: cannot marshal unknown Side value %d", uint8(s))
	}
}

// UnmarshalJSON parses a Side from the JSON wire-format string.
func (s *Side) UnmarshalJSON(data []byte) error {
	str, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("domain: invalid JSON for Side: %q", string(data))
	}
	switch str {
	case "buy":
		*s = Buy
	case "sell":
		*s = Sell
	default:
		return fmt.Errorf("domain: unknown Side %q", str)
	}
	return nil
}

// Type represents the order type. Stored as uint8; serialized as the wire
// strings "limit", "market", "stop", "stop_limit".
type Type uint8

const (
	Limit     Type = iota // 0 -> "limit"
	Market                // 1 -> "market"
	Stop                  // 2 -> "stop"
	StopLimit             // 3 -> "stop_limit"
)

// String returns the wire-format representation of t.
func (t Type) String() string {
	switch t {
	case Limit:
		return "limit"
	case Market:
		return "market"
	case Stop:
		return "stop"
	case StopLimit:
		return "stop_limit"
	default:
		return fmt.Sprintf("Type(%d)", uint8(t))
	}
}

// MarshalJSON serializes a Type using the wire-format string.
func (t Type) MarshalJSON() ([]byte, error) {
	switch t {
	case Limit:
		return []byte(`"limit"`), nil
	case Market:
		return []byte(`"market"`), nil
	case Stop:
		return []byte(`"stop"`), nil
	case StopLimit:
		return []byte(`"stop_limit"`), nil
	default:
		return nil, fmt.Errorf("domain: cannot marshal unknown Type value %d", uint8(t))
	}
}

// UnmarshalJSON parses a Type from the JSON wire-format string.
func (t *Type) UnmarshalJSON(data []byte) error {
	str, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("domain: invalid JSON for Type: %q", string(data))
	}
	switch str {
	case "limit":
		*t = Limit
	case "market":
		*t = Market
	case "stop":
		*t = Stop
	case "stop_limit":
		*t = StopLimit
	default:
		return fmt.Errorf("domain: unknown Type %q", str)
	}
	return nil
}

// Status represents the lifecycle state of an order. Stored as uint8;
// serialized as the wire strings "armed", "resting", "partially_filled",
// "filled", "cancelled", "rejected".
type Status uint8

const (
	StatusArmed           Status = iota // 0 -> "armed"
	StatusResting                       // 1 -> "resting"
	StatusPartiallyFilled               // 2 -> "partially_filled"
	StatusFilled                        // 3 -> "filled"
	StatusCancelled                     // 4 -> "cancelled"
	StatusRejected                      // 5 -> "rejected"
)

// String returns the wire-format representation of st.
func (st Status) String() string {
	switch st {
	case StatusArmed:
		return "armed"
	case StatusResting:
		return "resting"
	case StatusPartiallyFilled:
		return "partially_filled"
	case StatusFilled:
		return "filled"
	case StatusCancelled:
		return "cancelled"
	case StatusRejected:
		return "rejected"
	default:
		return fmt.Sprintf("Status(%d)", uint8(st))
	}
}

// MarshalJSON serializes a Status using the wire-format string.
func (st Status) MarshalJSON() ([]byte, error) {
	switch st {
	case StatusArmed:
		return []byte(`"armed"`), nil
	case StatusResting:
		return []byte(`"resting"`), nil
	case StatusPartiallyFilled:
		return []byte(`"partially_filled"`), nil
	case StatusFilled:
		return []byte(`"filled"`), nil
	case StatusCancelled:
		return []byte(`"cancelled"`), nil
	case StatusRejected:
		return []byte(`"rejected"`), nil
	default:
		return nil, fmt.Errorf("domain: cannot marshal unknown Status value %d", uint8(st))
	}
}

// UnmarshalJSON parses a Status from the JSON wire-format string.
func (st *Status) UnmarshalJSON(data []byte) error {
	str, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("domain: invalid JSON for Status: %q", string(data))
	}
	switch str {
	case "armed":
		*st = StatusArmed
	case "resting":
		*st = StatusResting
	case "partially_filled":
		*st = StatusPartiallyFilled
	case "filled":
		*st = StatusFilled
	case "cancelled":
		*st = StatusCancelled
	case "rejected":
		*st = StatusRejected
	default:
		return fmt.Errorf("domain: unknown Status %q", str)
	}
	return nil
}
