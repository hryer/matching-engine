package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnumSideIntegerValues(t *testing.T) {
	if uint8(Buy) != 0 {
		t.Errorf("Buy: want 0, got %d", uint8(Buy))
	}
	if uint8(Sell) != 1 {
		t.Errorf("Sell: want 1, got %d", uint8(Sell))
	}
}

func TestEnumTypeIntegerValues(t *testing.T) {
	cases := []struct {
		v    Type
		want uint8
	}{
		{Limit, 0},
		{Market, 1},
		{Stop, 2},
		{StopLimit, 3},
	}
	for _, c := range cases {
		if uint8(c.v) != c.want {
			t.Errorf("%s: want %d, got %d", c.v, c.want, uint8(c.v))
		}
	}
}

func TestEnumStatusIntegerValues(t *testing.T) {
	cases := []struct {
		v    Status
		want uint8
	}{
		{StatusArmed, 0},
		{StatusResting, 1},
		{StatusPartiallyFilled, 2},
		{StatusFilled, 3},
		{StatusCancelled, 4},
		{StatusRejected, 5},
	}
	for _, c := range cases {
		if uint8(c.v) != c.want {
			t.Errorf("%s: want %d, got %d", c.v, c.want, uint8(c.v))
		}
	}
}

func TestEnumSideRoundTrip(t *testing.T) {
	cases := []struct {
		v    Side
		wire string
	}{
		{Buy, `"buy"`},
		{Sell, `"sell"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("Marshal(%s): unexpected error: %v", c.v, err)
		}
		if string(got) != c.wire {
			t.Errorf("Marshal(%s): want %s, got %s", c.v, c.wire, string(got))
		}
		var out Side
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", string(got), err)
		}
		if out != c.v {
			t.Errorf("round-trip Side: want %s, got %s", c.v, out)
		}
	}
}

func TestEnumTypeRoundTrip(t *testing.T) {
	cases := []struct {
		v    Type
		wire string
	}{
		{Limit, `"limit"`},
		{Market, `"market"`},
		{Stop, `"stop"`},
		{StopLimit, `"stop_limit"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("Marshal(%s): unexpected error: %v", c.v, err)
		}
		if string(got) != c.wire {
			t.Errorf("Marshal(%s): want %s, got %s", c.v, c.wire, string(got))
		}
		var out Type
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", string(got), err)
		}
		if out != c.v {
			t.Errorf("round-trip Type: want %s, got %s", c.v, out)
		}
	}
}

func TestEnumStatusRoundTrip(t *testing.T) {
	cases := []struct {
		v    Status
		wire string
	}{
		{StatusArmed, `"armed"`},
		{StatusResting, `"resting"`},
		{StatusPartiallyFilled, `"partially_filled"`},
		{StatusFilled, `"filled"`},
		{StatusCancelled, `"cancelled"`},
		{StatusRejected, `"rejected"`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("Marshal(%s): unexpected error: %v", c.v, err)
		}
		if string(got) != c.wire {
			t.Errorf("Marshal(%s): want %s, got %s", c.v, c.wire, string(got))
		}
		var out Status
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", string(got), err)
		}
		if out != c.v {
			t.Errorf("round-trip Status: want %s, got %s", c.v, out)
		}
	}
}

func TestEnumStructFieldRoundTrip(t *testing.T) {
	type wrapper struct {
		S  Side   `json:"s"`
		T  Type   `json:"t"`
		St Status `json:"st"`
	}

	cases := []wrapper{
		{Buy, Limit, StatusArmed},
		{Sell, Market, StatusResting},
		{Buy, Stop, StatusPartiallyFilled},
		{Sell, StopLimit, StatusFilled},
		{Buy, Limit, StatusCancelled},
		{Sell, Market, StatusRejected},
	}

	for _, in := range cases {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("Marshal(%+v): unexpected error: %v", in, err)
		}
		var out wrapper
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("Unmarshal(%s): unexpected error: %v", string(raw), err)
		}
		if out != in {
			t.Errorf("round-trip wrapper: want %+v, got %+v (raw=%s)", in, out, string(raw))
		}
	}
}

func TestEnumStructFieldWireFormat(t *testing.T) {
	type wrapper struct {
		S  Side   `json:"s"`
		T  Type   `json:"t"`
		St Status `json:"st"`
	}

	in := wrapper{S: Buy, T: StopLimit, St: StatusPartiallyFilled}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: unexpected error: %v", err)
	}
	want := `{"s":"buy","t":"stop_limit","st":"partially_filled"}`
	if string(raw) != want {
		t.Errorf("wire format: want %s, got %s", want, string(raw))
	}
}

func TestEnumSideUnmarshalErrors(t *testing.T) {
	// Cases split: valid JSON tokens that our UnmarshalJSON must reject (domain-tagged)
	// vs malformed JSON the stdlib lexer rejects before our code runs (any error).
	domainTagged := []struct{ name, raw string }{
		{"empty string", `""`},
		{"wrong case", `"BUY"`},
		{"unknown variant", `"foobar"`},
		{"number", `0`},
	}
	for _, c := range domainTagged {
		t.Run(c.name, func(t *testing.T) {
			var s Side
			err := json.Unmarshal([]byte(c.raw), &s)
			if err == nil {
				t.Fatalf("Unmarshal(%q): expected error, got nil (s=%s)", c.raw, s)
			}
			if !strings.Contains(err.Error(), "domain") {
				t.Errorf("Unmarshal(%q): error %q lacks domain tag", c.raw, err.Error())
			}
		})
	}
	stdlibRejected := []struct{ name, raw string }{
		{"malformed (no quotes)", `buy`},
		{"empty bytes", ``},
	}
	for _, c := range stdlibRejected {
		t.Run(c.name, func(t *testing.T) {
			var s Side
			err := json.Unmarshal([]byte(c.raw), &s)
			if err == nil {
				t.Fatalf("Unmarshal(%q): expected error, got nil (s=%s)", c.raw, s)
			}
		})
	}
}

func TestEnumTypeUnmarshalErrors(t *testing.T) {
	domainTagged := []struct{ name, raw string }{
		{"empty string", `""`},
		{"wrong case", `"LIMIT"`},
		{"unknown variant", `"foobar"`},
		{"hyphen instead of underscore", `"stop-limit"`},
	}
	for _, c := range domainTagged {
		t.Run(c.name, func(t *testing.T) {
			var v Type
			err := json.Unmarshal([]byte(c.raw), &v)
			if err == nil {
				t.Fatalf("Unmarshal(%q): expected error, got nil (v=%s)", c.raw, v)
			}
			if !strings.Contains(err.Error(), "domain") {
				t.Errorf("Unmarshal(%q): error %q lacks domain tag", c.raw, err.Error())
			}
		})
	}
	t.Run("malformed (no quotes)", func(t *testing.T) {
		var v Type
		if err := json.Unmarshal([]byte(`limit`), &v); err == nil {
			t.Fatalf("expected error, got nil (v=%s)", v)
		}
	})
}

func TestEnumStatusUnmarshalErrors(t *testing.T) {
	domainTagged := []struct{ name, raw string }{
		{"empty string", `""`},
		{"wrong case", `"FILLED"`},
		{"unknown variant", `"foobar"`},
		{"hyphen", `"partially-filled"`},
	}
	for _, c := range domainTagged {
		t.Run(c.name, func(t *testing.T) {
			var v Status
			err := json.Unmarshal([]byte(c.raw), &v)
			if err == nil {
				t.Fatalf("Unmarshal(%q): expected error, got nil (v=%s)", c.raw, v)
			}
			if !strings.Contains(err.Error(), "domain") {
				t.Errorf("Unmarshal(%q): error %q lacks domain tag", c.raw, err.Error())
			}
		})
	}
	t.Run("malformed (no quotes)", func(t *testing.T) {
		var v Status
		if err := json.Unmarshal([]byte(`filled`), &v); err == nil {
			t.Fatalf("expected error, got nil (v=%s)", v)
		}
	})
}

// TestEnumUnmarshalErrorMentionsOffendingInput checks that the error message
// includes the offending value, which makes debugging easier.
func TestEnumUnmarshalErrorMentionsOffendingInput(t *testing.T) {
	cases := []struct {
		name    string
		fn      func([]byte) error
		raw     string
		fragment string
	}{
		{
			name:    "Side foobar",
			fn:      func(b []byte) error { var s Side; return s.UnmarshalJSON(b) },
			raw:     `"foobar"`,
			fragment: "foobar",
		},
		{
			name:    "Type qux",
			fn:      func(b []byte) error { var v Type; return v.UnmarshalJSON(b) },
			raw:     `"qux"`,
			fragment: "qux",
		},
		{
			name:    "Status BAZ",
			fn:      func(b []byte) error { var v Status; return v.UnmarshalJSON(b) },
			raw:     `"BAZ"`,
			fragment: "BAZ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn([]byte(c.raw))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", c.raw)
			}
			if !strings.Contains(err.Error(), c.fragment) {
				t.Errorf("error %q does not mention offending input %q", err.Error(), c.fragment)
			}
		})
	}
}

func TestEnumStringer(t *testing.T) {
	if Buy.String() != "buy" {
		t.Errorf("Buy.String(): want buy, got %q", Buy.String())
	}
	if Sell.String() != "sell" {
		t.Errorf("Sell.String(): want sell, got %q", Sell.String())
	}
	if Limit.String() != "limit" {
		t.Errorf("Limit.String(): want limit, got %q", Limit.String())
	}
	if StopLimit.String() != "stop_limit" {
		t.Errorf("StopLimit.String(): want stop_limit, got %q", StopLimit.String())
	}
	if StatusPartiallyFilled.String() != "partially_filled" {
		t.Errorf("StatusPartiallyFilled.String(): want partially_filled, got %q", StatusPartiallyFilled.String())
	}
}
