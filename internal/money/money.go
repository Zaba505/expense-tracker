// Package money holds the integer-cents money type used throughout the
// expense tracker. All amounts are stored and computed as int64 cents to
// avoid floating-point rounding error.
//
// Cents is the only currency representation in the system: events persist
// it, projections sum it, and the web layer renders it. Nothing converts
// through float64, so a fold over years of events is exact.
//
// Parse is deliberately forgiving about presentation (a leading "$",
// thousands separators, parentheses for negatives — all shapes the
// spreadsheet export and the entry form produce) and deliberately strict
// about precision: an amount with more than two fractional digits is an
// error rather than a silent rounding.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cents is a monetary amount in whole cents. Negative values are allowed
// and meaningful: a correcting event may add a negative amount.
type Cents int64

// Errors reported by Parse. Both are wrapped, so callers that need to
// distinguish a typo from an out-of-range amount can use errors.Is; the
// importer, for instance, reports the offending row and column alongside.
var (
	// ErrSyntax means the input is not a recognizable amount.
	ErrSyntax = errors.New("money: invalid amount")

	// ErrRange means the input is a well-formed amount too large to
	// represent as int64 cents.
	ErrRange = errors.New("money: amount out of range")
)

// maxFractionDigits is the precision of a cent. Anything finer is
// rejected rather than rounded — losing a fraction of a cent silently is
// exactly the class of bug this package exists to prevent.
const maxFractionDigits = 2

// Parse converts a human-written amount to Cents.
//
// It accepts an optional sign and an optional "$", in either order, an
// optional set of comma thousands separators, and up to two fractional
// digits: "1234", "1,234.56", "$1,234.56", "-$1,234.56", "$-1,234.56".
// A parenthesized amount is negative, as the spreadsheet export writes
// it: "($1,234.56)". Surrounding whitespace is ignored.
func Parse(s string) (Cents, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, syntaxError(s)
	}

	neg := false
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		neg = true
		t = strings.TrimSpace(t[1 : len(t)-1])
	}

	// The sign and the currency symbol may appear in either order, but
	// each at most once.
	sawSign, sawSymbol := false, false
prefix:
	for t != "" {
		switch {
		case !sawSign && (t[0] == '-' || t[0] == '+'):
			neg = neg != (t[0] == '-')
			sawSign = true
		case !sawSymbol && t[0] == '$':
			sawSymbol = true
		default:
			break prefix
		}
		t = strings.TrimSpace(t[1:])
	}
	if t == "" {
		// Nothing but decoration: "$", "-", "()".
		return 0, syntaxError(s)
	}

	whole, frac, hasPoint := strings.Cut(t, ".")

	digits, ok := wholeDigits(whole)
	if !ok {
		return 0, syntaxError(s)
	}
	if hasPoint {
		if frac == "" || len(frac) > maxFractionDigits || !allDigits(frac) {
			return 0, syntaxError(s)
		}
		frac += strings.Repeat("0", maxFractionDigits-len(frac))
	} else {
		frac = strings.Repeat("0", maxFractionDigits)
	}
	digits += frac
	if neg {
		digits = "-" + digits
	}

	c, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		// digits is all digits by construction, so the only way
		// ParseInt fails here is an amount that overflows int64.
		return 0, fmt.Errorf("%w: %q", ErrRange, s)
	}
	return Cents(c), nil
}

// String renders the amount as a plain decimal with two fractional
// digits and no symbol or separators: "1234.56", "-0.05", "0.00". It is
// the canonical form and round-trips through Parse.
func (c Cents) String() string {
	neg, digits := split(c)
	out := digits[:len(digits)-maxFractionDigits] + "." + digits[len(digits)-maxFractionDigits:]
	if neg {
		return "-" + out
	}
	return out
}

// Display renders the amount for a human: "$1,234.56", "-$1,234.56",
// "$0.00". Its output also parses back to the same Cents.
func (c Cents) Display() string {
	neg, digits := split(c)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte('$')
	b.WriteString(group(digits[:len(digits)-maxFractionDigits]))
	b.WriteByte('.')
	b.WriteString(digits[len(digits)-maxFractionDigits:])
	return b.String()
}

// split returns the sign and the unsigned decimal digits of c, padded to
// at least one whole digit plus two fractional digits. It goes through
// the decimal string rather than negating c so that the most negative
// int64 has no special case.
func split(c Cents) (neg bool, digits string) {
	digits = strconv.FormatInt(int64(c), 10)
	if neg = strings.HasPrefix(digits, "-"); neg {
		digits = digits[1:]
	}
	if n := maxFractionDigits + 1 - len(digits); n > 0 {
		digits = strings.Repeat("0", n) + digits
	}
	return neg, digits
}

// group inserts comma thousands separators into a string of digits.
func group(digits string) string {
	var b strings.Builder
	for i, d := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(d)
	}
	return b.String()
}

// wholeDigits validates the integer part of an amount and returns it
// with any thousands separators removed. An empty part is "0", so
// ".50" is half a dollar. When separators are present they must group
// the digits properly: "1,234" is fine, "12,34" and ",123" are not.
func wholeDigits(whole string) (string, bool) {
	if whole == "" {
		return "0", true
	}
	if strings.Contains(whole, ",") {
		groups := strings.Split(whole, ",")
		if n := len(groups[0]); n < 1 || n > 3 {
			return "", false
		}
		for _, g := range groups[1:] {
			if len(g) != 3 {
				return "", false
			}
		}
		whole = strings.ReplaceAll(whole, ",", "")
	}
	if !allDigits(whole) {
		return "", false
	}
	return whole, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func syntaxError(s string) error {
	return fmt.Errorf("%w: %q", ErrSyntax, s)
}
