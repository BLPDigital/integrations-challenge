package model

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestCurrencyScale(t *testing.T) {
	tests := []struct {
		code  string
		scale uint8
		err   error
	}{
		{"CHF", 2, nil},
		{"EUR", 2, nil},
		{"USD", 2, nil},
		{"GBP", 2, nil},
		{"SEK", 2, nil},
		{"DKK", 2, nil},
		{"NOK", 2, nil},
		{"JPY", 0, nil},
		{"HUF", 2, nil},
		{"IDR", 2, nil},
		{"chf", 0, ErrCurrencyUnknown},
		{" CHF", 0, ErrCurrencyUnknown},
		{"XXX", 0, ErrCurrencyUnknown},
		{"", 0, ErrCurrencyUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			got, err := CurrencyScale(tc.code)
			if !errors.Is(err, tc.err) {
				t.Fatalf("CurrencyScale(%q) error = %v, want %v", tc.code, err, tc.err)
			}
			if got != tc.scale {
				t.Fatalf("CurrencyScale(%q) = %d, want %d", tc.code, got, tc.scale)
			}
		})
	}
}

func TestCurrenciesIsSortedAndComplete(t *testing.T) {
	want := []string{"CHF", "DKK", "EUR", "GBP", "HUF", "IDR", "JPY", "NOK", "SEK", "USD"}
	for i := 0; i < 10; i++ { // repeated: the order must not depend on map iteration
		if got := Currencies(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Currencies() = %v, want %v", got, want)
		}
	}
}

func TestNewMoney(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		amount   string
		want     Money
		err      error
	}{
		{"chf two decimals", "CHF", "108.23", Money{AmountMinor: 10823, Currency: "CHF", Scale: 2}, nil},
		{"chf trailing zeros", "CHF", "45.5000", Money{AmountMinor: 4550, Currency: "CHF", Scale: 2}, nil},
		{"chf integer", "CHF", "45", Money{AmountMinor: 4500, Currency: "CHF", Scale: 2}, nil},
		{"jpy has no minor units", "JPY", "250000", Money{AmountMinor: 250000, Currency: "JPY", Scale: 0}, nil},
		{"jpy fraction is inexact", "JPY", "250000.50", Money{}, ErrInexact},
		{"chf third decimal is inexact", "CHF", "45.505", Money{}, ErrInexact},
		{"unknown currency", "XXX", "1.00", Money{}, ErrCurrencyUnknown},
		{"negative", "EUR", "-1.05", Money{AmountMinor: -105, Currency: "EUR", Scale: 2}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewMoney(tc.currency, MustDecimal(tc.amount))
			if !errors.Is(err, tc.err) {
				t.Fatalf("NewMoney error = %v, want %v", err, tc.err)
			}
			if got != tc.want {
				t.Fatalf("NewMoney = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMoneyJSONShape(t *testing.T) {
	m := Money{AmountMinor: 10823, Currency: "CHF", Scale: 2}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount_minor":10823,"currency":"CHF","scale":2}` {
		t.Fatalf("Marshal = %s", b)
	}
	// Zero values are present too: the shape never depends on the value.
	b, err = json.Marshal(Money{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount_minor":0,"currency":"","scale":0}` {
		t.Fatalf("Marshal(zero) = %s", b)
	}
	canon, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"amount_minor":10823,"currency":"CHF","scale":2}` {
		t.Fatalf("CanonicalJSON = %s", canon)
	}
}

func TestMoneyArithmetic(t *testing.T) {
	a := Money{AmountMinor: 10823, Currency: "CHF", Scale: 2}
	b := Money{AmountMinor: 177, Currency: "CHF", Scale: 2}
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if sum.AmountMinor != 11000 {
		t.Fatalf("Add = %d, want 11000", sum.AmountMinor)
	}
	diff, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if diff.AmountMinor != 10646 {
		t.Fatalf("Sub = %d, want 10646", diff.AmountMinor)
	}
	if _, err := a.Add(Money{AmountMinor: 1, Currency: "EUR", Scale: 2}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add across currencies = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := a.Cmp(Money{AmountMinor: 1, Currency: "CHF", Scale: 0}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Cmp across scales = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := a.Sub(Money{AmountMinor: math.MinInt64, Currency: "CHF", Scale: 2}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Sub of the unrepresentable amount = %v, want ErrOverflow", err)
	}
	big := Money{AmountMinor: math.MaxInt64, Currency: "CHF", Scale: 2}
	if _, err := big.Add(Money{AmountMinor: 1, Currency: "CHF", Scale: 2}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add overflow = %v, want ErrOverflow", err)
	}
	if c, err := a.Cmp(b); err != nil || c != 1 {
		t.Fatalf("Cmp = %d, %v", c, err)
	}
	if got := a.String(); got != "108.23 CHF" {
		t.Fatalf("String = %q", got)
	}
	if got := a.Decimal().String(); got != "108.23" {
		t.Fatalf("Decimal = %q", got)
	}
}

func TestMoneyValidate(t *testing.T) {
	tests := []struct {
		name  string
		money Money
		codes []string
	}{
		{"valid chf", Money{AmountMinor: 1, Currency: "CHF", Scale: 2}, nil},
		{"valid jpy", Money{AmountMinor: 1, Currency: "JPY", Scale: 0}, nil},
		{"unknown currency", Money{Currency: "XXX", Scale: 2}, []string{CodeCurrencyUnknown}},
		{"jpy with minor units", Money{Currency: "JPY", Scale: 2}, []string{CodeFieldInvalid}},
		{"unrepresentable amount", Money{AmountMinor: math.MinInt64, Currency: "CHF", Scale: 2},
			[]string{CodeFieldInvalid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertCodes(t, tc.money.Validate(), tc.codes)
		})
	}
}

// assertCodes compares the codes of the findings, in order.
func assertCodes(t *testing.T, errs FieldErrors, want []string) {
	t.Helper()
	got := make([]string, len(errs))
	for i, e := range errs {
		got[i] = e.Code
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %v (%v), want %v", got, errs, want)
	}
}
