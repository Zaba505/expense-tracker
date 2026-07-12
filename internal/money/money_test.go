package money

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want Cents
	}{
		{"whole dollars", "12", 1200},
		{"zero", "0", 0},
		{"zero with cents", "0.00", 0},
		{"cents only", "0.99", 99},
		{"one fractional digit", "1234.5", 123450},
		{"decimal point", "1234.56", 123456},
		{"thousands separator", "1,234.56", 123456},
		{"currency symbol", "$1,234.56", 123456},
		{"sign before symbol", "-$1,234.56", -123456},
		{"sign after symbol", "$-1,234.56", -123456},
		{"explicit plus", "+$1,234.56", 123456},
		{"parenthesized negative", "($1,234.56)", -123456},
		{"parenthesized without symbol", "(1,234.56)", -123456},
		{"surrounding whitespace", "  $1,234.56\t", 123456},
		{"space after symbol", "$ 1,234.56", 123456},
		{"no whole part", ".5", 50},
		{"negative zero", "-0.00", 0},
		{"leading zeros", "007.50", 750},
		{"millions", "$1,234,567.89", 123456789},
		{"max int64", "92233720368547758.07", math.MaxInt64},
		{"min int64", "-92233720368547758.08", math.MinInt64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v, want nil", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrSyntax},
		{"whitespace only", "   ", ErrSyntax},
		{"symbol only", "$", ErrSyntax},
		{"sign only", "-", ErrSyntax},
		{"empty parens", "()", ErrSyntax},
		{"point only", ".", ErrSyntax},
		{"trailing point", "1.", ErrSyntax},
		{"letters", "abc", ErrSyntax},
		{"trailing letters", "1.00usd", ErrSyntax},
		{"other currency", "€5.00", ErrSyntax},
		{"two symbols", "$$5", ErrSyntax},
		{"two signs", "--5", ErrSyntax},
		// Parens already say "negative"; a sign inside them is ambiguous,
		// and guessing at it would silently flip the amount.
		{"negative sign in parens", "(-5)", ErrSyntax},
		{"positive sign in parens", "(+5)", ErrSyntax},
		{"sign in parens with symbol", "(-$5.00)", ErrSyntax},
		{"two points", "1..2", ErrSyntax},
		{"embedded space", "1 234.56", ErrSyntax},
		{"three fractional digits", "1.234", ErrSyntax},
		{"short group", "12,34", ErrSyntax},
		{"long group", "1,23456.00", ErrSyntax},
		{"leading separator", ",123", ErrSyntax},
		{"trailing separator", "123,", ErrSyntax},
		{"separator in fraction", "1.2,3", ErrSyntax},
		{"unbalanced paren", "(1.00", ErrSyntax},
		{"overflows int64", "92233720368547758.08", ErrRange},
		{"underflows int64", "-92233720368547758.09", ErrRange},
		{"absurdly large", "99,999,999,999,999,999,999.99", ErrRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.in)
			if err == nil {
				t.Fatalf("Parse(%q) = %d, want error", tt.in, got)
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("Parse(%q) error = %v, want %v", tt.in, err, tt.want)
			}
			if got != 0 {
				t.Errorf("Parse(%q) = %d, want 0 on error", tt.in, got)
			}
			// The importer reports parse failures per row and column, so
			// the offending text has to survive in the message.
			if !strings.Contains(err.Error(), strconv.Quote(tt.in)) {
				t.Errorf("Parse(%q) error = %q, want it to quote the input", tt.in, err)
			}
		})
	}
}

func TestCents_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   Cents
		want string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{50, "0.50"},
		{-5, "-0.05"},
		{100, "1.00"},
		{123456, "1234.56"},
		{-123456, "-1234.56"},
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.String(); got != tt.want {
				t.Errorf("Cents(%d).String() = %q, want %q", int64(tt.in), got, tt.want)
			}
		})
	}
}

func TestCents_Display(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   Cents
		want string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{-5, "-$0.05"},
		{100, "$1.00"},
		{99999, "$999.99"},
		{100000, "$1,000.00"},
		{123456, "$1,234.56"},
		{-123456, "-$1,234.56"},
		{123456789, "$1,234,567.89"},
		{math.MaxInt64, "$92,233,720,368,547,758.07"},
		{math.MinInt64, "-$92,233,720,368,547,758.08"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.Display(); got != tt.want {
				t.Errorf("Cents(%d).Display() = %q, want %q", int64(tt.in), got, tt.want)
			}
		})
	}
}

// TestRoundTrip pins the property both rendered forms must hold: whatever
// Cents prints, Parse reads back as the same Cents. Money that survives a
// trip through the UI or a CSV export has to come home unchanged.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	values := []Cents{
		0, 1, -1, 5, -5, 99, -99, 100, -100, 123456, -123456,
		999999999, -999999999, math.MaxInt64, math.MinInt64,
	}

	for _, c := range values {
		t.Run(c.String(), func(t *testing.T) {
			t.Parallel()
			for name, s := range map[string]string{"String": c.String(), "Display": c.Display()} {
				got, err := Parse(s)
				if err != nil {
					t.Fatalf("Parse(%s() = %q) error = %v, want nil", name, s, err)
				}
				if got != c {
					t.Errorf("Parse(%s() = %q) = %d, want %d", name, s, got, c)
				}
			}
		})
	}
}

// FuzzRoundTrip extends TestRoundTrip to every int64 the fuzzer reaches.
func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []int64{0, -1, 1, 999, -100000, math.MaxInt64, math.MinInt64} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, n int64) {
		c := Cents(n)
		for _, s := range []string{c.String(), c.Display()} {
			got, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v, want nil", s, err)
			}
			if got != c {
				t.Errorf("Parse(%q) = %d, want %d", s, got, c)
			}
		}
	})
}

// FuzzParse checks the other direction: anything Parse accepts must render
// back to text that parses to the identical value, and anything it rejects
// must not panic on the way out.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{"1,234.56", "$1,234.56", "($1,234.56)", ".5", "0", "", "$", "1.234", "12,34"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		c, err := Parse(s)
		if err != nil {
			return
		}
		got, err := Parse(c.String())
		if err != nil {
			t.Fatalf("Parse(%q) = %d, but re-parsing its String() %q errored: %v", s, c, c.String(), err)
		}
		if got != c {
			t.Errorf("Parse(%q) = %d, but round-trip through %q gave %d", s, c, c.String(), got)
		}
	})
}
