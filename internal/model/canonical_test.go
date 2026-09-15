package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Test fixtures built from code points so this file stays pure ASCII.
var (
	uUmlaut     = string(rune(0x00FC))       // LATIN SMALL LETTER U WITH DIAERESIS
	uCombining  = "u" + string(rune(0x0308)) // 'u' plus COMBINING DIAERESIS
	lineSep     = string(rune(0x2028))       // LINE SEPARATOR
	paraSep     = string(rune(0x2029))       // PARAGRAPH SEPARATOR
	replacement = string(rune(0xFFFD))       // REPLACEMENT CHARACTER
)

func TestCanonicalJSONShape(t *testing.T) {
	type nested struct {
		Zebra string `json:"zebra"`
		Alpha string `json:"alpha"`
	}
	type payload struct {
		Zulu    string         `json:"zulu"`
		Alpha   int            `json:"alpha"`
		Nested  nested         `json:"nested"`
		List    []string       `json:"list"`
		NilList []string       `json:"nil_list"`
		NilMap  map[string]int `json:"nil_map"`
		Map     map[string]int `json:"map"`
		Amount  Decimal        `json:"amount"`
		Money   Money          `json:"money"`
		Ptr     *nested        `json:"ptr"`
		Skipped string         `json:"-"`
		Omitted string         `json:"omitted,omitempty"`
		Flag    bool           `json:"flag"`
	}
	got, err := CanonicalJSON(payload{
		Zulu:    "z",
		Alpha:   1,
		Nested:  nested{Zebra: "z", Alpha: "a"},
		List:    []string{"b", "a"},
		Map:     map[string]int{"b": 2, "a": 1},
		Amount:  MustDecimal("45.5000"),
		Money:   Money{AmountMinor: 10823, Currency: "CHF", Scale: 2},
		Skipped: "never",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"alpha":1,"amount":"45.5000","flag":false,"list":["b","a"],` +
		`"map":{"a":1,"b":2},"money":{"amount_minor":10823,"currency":"CHF","scale":2},` +
		`"nested":{"alpha":"a","zebra":"z"},"nil_list":[],"nil_map":{},"ptr":null,"zulu":"z"}`
	if string(got) != want {
		t.Fatalf("CanonicalJSON =\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalJSONNoHTMLEscaping(t *testing.T) {
	got, err := CanonicalJSON(map[string]string{"k": `a<b>c&d"e\f`})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"k":"a<b>c&d\"e\\f"}`
	if string(got) != want {
		t.Fatalf("CanonicalJSON = %s, want %s", got, want)
	}
	// The standard library escapes the angle brackets and the ampersand; the
	// canonical form must not, or the bytes would not be comparable across
	// producers.
	std, err := json.Marshal(map[string]string{"k": "a<b>c&d"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(std), "<") {
		t.Fatalf("standard library no longer HTML-escapes: %s", std)
	}
}

func TestCanonicalJSONStrings(t *testing.T) {
	composed := "Z" + uUmlaut + "rich"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trimmed", "  " + composed + "  ", `"` + composed + `"`},
		{"whitespace trimmed", "\t\n" + composed + "\r\n", `"` + composed + `"`},
		{"decomposed is composed", "Z" + uCombining + "rich", `"` + composed + `"`},
		{"already composed", composed, `"` + composed + `"`},
		{"inner control escaped", "a\nb", `"a\nb"`},
		{"line separator escaped", "a" + lineSep + "b", "\"a\\u2028b\""},
		{"paragraph separator escaped", "a" + paraSep + "b", "\"a\\u2029b\""},
		{"invalid utf8 replaced", "a\xffb", `"a` + replacement + `b"`},
		{"unit separator escaped", "a\x1fb", "\"a\\u001fb\""},
		{"empty", "", `""`},
		{"whitespace only", "   ", `""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalJSON(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("CanonicalJSON(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalJSONNFCEqualsHash(t *testing.T) {
	decomposed := Supplier{SupplierNumber: "0000417",
		Name: "Z" + uCombining + "rcher Kantonalbank", Currency: "CHF", Country: "CH"}
	composed := Supplier{SupplierNumber: "0000417",
		Name: "Z" + uUmlaut + "rcher Kantonalbank", Currency: "CHF", Country: "CH"}
	if a, b := ContentHash(decomposed), ContentHash(composed); a != b {
		t.Fatalf("hashes differ: %s vs %s", a, b)
	}
	padded := composed
	padded.Name = "  " + padded.Name + "  "
	if a, b := ContentHash(padded), ContentHash(composed); a != b {
		t.Fatalf("trimming is not applied: %s vs %s", a, b)
	}
}

func TestCanonicalJSONRejectsFloats(t *testing.T) {
	for _, v := range []any{
		1.5,
		float32(1.5),
		map[string]float64{"a": 1},
		struct {
			A float64 `json:"a"`
		}{1},
		[]float64{1},
	} {
		if _, err := CanonicalJSON(v); !errors.Is(err, ErrFloatUnsupported) {
			t.Fatalf("CanonicalJSON(%v) = %v, want ErrFloatUnsupported", v, err)
		}
	}
}

func TestCanonicalJSONDuplicateKeys(t *testing.T) {
	// The two keys collide only after NFC normalization.
	in := map[string]int{"Z" + uUmlaut + "rich": 1, "Z" + uCombining + "rich": 2}
	if _, err := CanonicalJSON(in); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("CanonicalJSON = %v, want ErrDuplicateKey", err)
	}
	// And after trimming.
	if _, err := CanonicalJSON(map[string]int{"a": 1, " a ": 2}); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("CanonicalJSON = %v, want ErrDuplicateKey", err)
	}
}

func TestCanonicalJSONUnsupported(t *testing.T) {
	if _, err := CanonicalJSON(make(chan int)); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("chan = %v, want ErrUnsupportedType", err)
	}
	if _, err := CanonicalJSON(map[float64]int{1: 1}); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("float map key = %v, want ErrUnsupportedType", err)
	}
}

type embeddedBase struct {
	Base string `json:"base"`
}

type embedder struct {
	embeddedBase
	Own string `json:"own"`
}

type nestedNamed struct {
	Inner embeddedBase `json:"inner"`
	Own   string       `json:"own"`
}

func TestCanonicalJSONStructRules(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"embedded flattened", embedder{embeddedBase{"b"}, "o"}, `{"base":"b","own":"o"}`},
		{"named nested", nestedNamed{embeddedBase{"b"}, "o"}, `{"inner":{"base":"b"},"own":"o"}`},
		{"untagged field keeps its go name", struct {
			Field string
		}{"v"}, `{"Field":"v"}`},
		{"omitempty on zero", struct {
			A string `json:"a,omitempty"`
			B int    `json:"b,omitempty"`
			C []int  `json:"c,omitempty"`
		}{}, `{}`},
		{"json number kept verbatim", struct {
			N json.Number `json:"n"`
		}{"1.500"}, `{"n":1.500}`},
		{"raw message canonicalized", struct {
			R json.RawMessage `json:"r"`
		}{json.RawMessage(` {"b":1,  "a":2} `)}, `{"r":{"a":2,"b":1}}`},
		{"bytes are base64", struct {
			B []byte `json:"b"`
		}{[]byte("hi")}, `{"b":"aGk="}`},
		{"pointer dereferenced", &embedder{embeddedBase{"b"}, "o"}, `{"base":"b","own":"o"}`},
		{"nil interface field", struct {
			A any `json:"a"`
		}{}, `{"a":null}`},
		{"array", [2]int{1, 2}, `[1,2]`},
		{"empty slice", []int{}, `[]`},
		{"nil slice", []int(nil), `[]`},
		{"nil map", map[string]int(nil), `{}`},
		{"nil pointer", (*embedder)(nil), `null`},
		{"nil any", nil, `null`},
		{"unsigned and signed", struct {
			U uint8 `json:"u"`
			I int64 `json:"i"`
		}{255, -9223372036854775808}, `{"i":-9223372036854775808,"u":255}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalJSON(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("CanonicalJSON = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalJSONDeterministic(t *testing.T) {
	m := map[string]any{}
	for i := 0; i < 64; i++ {
		m[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	first, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		again, err := CanonicalJSON(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatal("CanonicalJSON is not stable across map iterations")
		}
	}
}

func TestContentHash(t *testing.T) {
	v := Supplier{SupplierNumber: "0000417", Name: "ACME AG", Country: "CH", Currency: "CHF"}
	b, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ContentHash(v), Sha256Hex(b); got != want {
		t.Fatalf("ContentHash = %s, want %s", got, want)
	}
	if got := ContentHash(v); len(got) != 64 || got != strings.ToLower(got) {
		t.Fatalf("ContentHash is not 64 lowercase hex characters: %s", got)
	}
	// Pinned vector, so the hash function itself cannot drift.
	if got, want := Sha256Hex(nil), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"; got != want {
		t.Fatalf("Sha256Hex(nil) = %s, want %s", got, want)
	}
	// A value that cannot be canonicalized hashes to the empty string, which no
	// real hash equals.
	if got := ContentHash(1.5); got != "" {
		t.Fatalf("ContentHash(float) = %q, want empty", got)
	}
	other := v
	other.Name = "ACME SA"
	if ContentHash(v) == ContentHash(other) {
		t.Fatal("ContentHash ignored a field change")
	}
	// Scale is part of the content: 45.5 and 45.5000 are different revisions.
	a := PurchaseOrderLine{PONumber: "PO-1", LineNo: "00010", Quantity: MustDecimal("45.5")}
	c := PurchaseOrderLine{PONumber: "PO-1", LineNo: "00010", Quantity: MustDecimal("45.5000")}
	if ContentHash(a) == ContentHash(c) {
		t.Fatal("ContentHash lost the decimal scale")
	}
}

func TestCanonicalJSONInvoiceLinesNeverNull(t *testing.T) {
	got, err := CanonicalJSON(APInvoice{SupplierNumber: "1", SupplierInvoiceNumber: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"lines":[]`) {
		t.Fatalf("nil lines encoded as null: %s", got)
	}
}

func TestDecodeJSONTree(t *testing.T) {
	v, err := DecodeJSONTree([]byte(`{"b":1.50,"a":[1,{"c":null}],"s":" x ","t":true}`))
	if err != nil {
		t.Fatal(err)
	}
	// Numbers survive as json.Number, so the canonical form keeps them verbatim
	// instead of going through a float64.
	got, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":[1,{"c":null}],"b":1.50,"s":"x","t":true}`
	if string(got) != want {
		t.Fatalf("CanonicalJSON = %s, want %s", got, want)
	}
	// The standard library's own decoding would have produced float64 values,
	// which CanonicalJSON rejects.
	var std any
	if err := json.Unmarshal([]byte(`{"b":1.50}`), &std); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalJSON(std); !errors.Is(err, ErrFloatUnsupported) {
		t.Fatalf("CanonicalJSON of a float64 tree = %v, want ErrFloatUnsupported", err)
	}
	for _, bad := range []string{``, `{`, `{} {}`, `nope`} {
		if _, err := DecodeJSONTree([]byte(bad)); err == nil {
			t.Fatalf("DecodeJSONTree(%q) = nil error", bad)
		}
	}
}
