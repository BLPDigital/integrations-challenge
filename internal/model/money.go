package model

import (
	"errors"
	"math"
	"sort"
	"strconv"
)

// ErrCurrencyUnknown reports a currency code outside the fixed table below.
// Record-level callers surface it as E_CURRENCY_UNKNOWN.
var ErrCurrencyUnknown = errors.New("model: unknown currency")

// currencyScales is the complete, fixed ISO 4217 minor-unit table for this
// landscape. There is no external currency data and no runtime registration:
// an unknown code is an error, never a guess with scale 2.
var currencyScales = map[string]uint8{
	"CHF": 2,
	"EUR": 2,
	"USD": 2,
	"GBP": 2,
	"SEK": 2,
	"DKK": 2,
	"NOK": 2,
	"JPY": 0,
	"HUF": 2,
	"IDR": 2,
}

// CurrencyScale returns the minor-unit scale of an ISO 4217 code, e.g. 2 for CHF
// and 0 for JPY. Lookup is exact: codes are uppercase, three letters, untrimmed.
func CurrencyScale(code string) (uint8, error) {
	s, ok := currencyScales[code]
	if !ok {
		return 0, ErrCurrencyUnknown
	}
	return s, nil
}

// IsKnownCurrency reports whether code is in the fixed currency table.
func IsKnownCurrency(code string) bool {
	_, ok := currencyScales[code]
	return ok
}

// Currencies returns every known currency code in ascending byte order. The
// slice is freshly allocated, so callers cannot mutate the table, and the order
// never depends on map iteration.
func Currencies() []string {
	out := make([]string, 0, len(currencyScales))
	for c := range currencyScales {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Money is an exact amount in the minor units of a currency. It is the wire and
// storage form for every monetary value: an integer count of minor units plus
// the currency and its scale, so no consumer has to know the scale table and no
// value can be misread as a major-unit float.
type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"` // ISO 4217, uppercase, exactly 3 letters
	Scale       uint8  `json:"scale"`    // minor-unit scale of the currency, normally 2, JPY 0
}

// NewMoney converts d into Money for the given currency. The conversion is an
// exact rescale to the currency's minor-unit scale: an amount with more fraction
// digits than the currency has minor digits is ErrInexact, never rounded.
func NewMoney(currency string, d Decimal) (Money, error) {
	scale, err := CurrencyScale(currency)
	if err != nil {
		return Money{}, err
	}
	minor, err := d.MinorUnits(scale)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: minor, Currency: currency, Scale: scale}, nil
}

// Decimal returns the amount as a Decimal with Scale fraction digits. It assumes
// a Money that NewMoney produced or that Validate accepts: a scale above
// MaxScale or the unrepresentable amount math.MinInt64 would violate the Decimal
// invariants, which is why Validate rejects both.
func (m Money) Decimal() Decimal {
	return Decimal{unscaled: m.AmountMinor, scale: m.Scale}
}

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool { return m.AmountMinor == 0 }

// Neg returns the amount with the opposite sign.
func (m Money) Neg() Money {
	m.AmountMinor = -m.AmountMinor
	return m
}

// Add returns m+o. Both operands must carry the same currency and scale, else
// ErrCurrencyMismatch; overflow is ErrOverflow.
func (m Money) Add(o Money) (Money, error) {
	if m.Currency != o.Currency || m.Scale != o.Scale {
		return Money{}, ErrCurrencyMismatch
	}
	sum := m.AmountMinor + o.AmountMinor
	if (m.AmountMinor > 0 && o.AmountMinor > 0 && sum <= 0) ||
		(m.AmountMinor < 0 && o.AmountMinor < 0 && sum >= 0) {
		return Money{}, ErrOverflow
	}
	m.AmountMinor = sum
	return m, nil
}

// Sub returns m-o under the same rules as Add.
func (m Money) Sub(o Money) (Money, error) {
	if o.AmountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return m.Add(o.Neg())
}

// ErrCurrencyMismatch reports an arithmetic operation on two different
// currencies or scales.
var ErrCurrencyMismatch = errors.New("model: currency mismatch")

// String returns the amount and currency, e.g. "108.23 CHF".
func (m Money) String() string {
	return m.Decimal().String() + " " + m.Currency
}

// Cmp returns -1, 0 or +1 as m is less than, equal to or greater than o. It
// reports ErrCurrencyMismatch for differing currencies or scales.
func (m Money) Cmp(o Money) (int, error) {
	if m.Currency != o.Currency || m.Scale != o.Scale {
		return 0, ErrCurrencyMismatch
	}
	switch {
	case m.AmountMinor < o.AmountMinor:
		return -1, nil
	case m.AmountMinor > o.AmountMinor:
		return 1, nil
	}
	return 0, nil
}

// Validate reports the currency and scale problems of m as field errors. An
// unknown currency is E_CURRENCY_UNKNOWN; a scale that contradicts the currency
// table is E_FIELD_INVALID, because a JPY amount with scale 2 is a 100x bug.
func (m Money) Validate() FieldErrors {
	var errs FieldErrors
	scale, err := CurrencyScale(m.Currency)
	if err != nil {
		return append(errs, FieldError{Code: CodeCurrencyUnknown, Field: "currency",
			Message: "unknown currency " + strconv.Quote(m.Currency)})
	}
	if m.Scale != scale {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "scale",
			Message: "scale " + strconv.Itoa(int(m.Scale)) + " contradicts currency " + m.Currency})
	}
	if m.AmountMinor == math.MinInt64 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "amount_minor",
			Message: "amount is not representable: -2^63 has no exact negation"})
	}
	return errs
}
