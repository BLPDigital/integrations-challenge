package model

import "testing"

// cp builds a string from code points, keeping this file pure ASCII.
func cp(points ...rune) string {
	return string(points)
}

func TestNormalizeNFC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ascii unchanged", "ACME AG", "ACME AG"},
		{"empty", "", ""},
		{"already composed", cp(0x005A, 0x00FC, 0x0072), cp(0x005A, 0x00FC, 0x0072)},
		{"u diaeresis", cp('Z', 'u', 0x0308, 'r'), cp('Z', 0x00FC, 'r')},
		{"e acute", cp('C', 'a', 'f', 'e', 0x0301), cp('C', 'a', 'f', 0x00E9)},
		{"a grave", cp('a', 0x0300), cp(0x00E0)},
		{"o circumflex", cp('o', 0x0302), cp(0x00F4)},
		{"n tilde", cp('n', 0x0303), cp(0x00F1)},
		{"c cedilla", cp('c', 0x0327), cp(0x00E7)},
		{"a ring above", cp('a', 0x030A), cp(0x00E5)},
		{"uppercase", cp('A', 0x0308, 'A', 0x0300), cp(0x00C4, 0x00C0)},
		{"unmapped uppercase pair is kept", cp('B', 0x0300), cp('B', 0x0300)},
		{"multiple marks in one string", cp('Z', 'u', 0x0308, 'r', 'i', 'c', 'h', ' ', 'G', 'e', 'n', 'e', 0x0300, 'v', 'e'),
			cp('Z', 0x00FC, 'r', 'i', 'c', 'h', ' ', 'G', 'e', 'n', 0x00E8, 'v', 'e')},
		{"mark without a base is kept", cp(0x0308, 'a'), cp(0x0308, 'a')},
		{"unmapped pair is kept", cp('q', 0x030A), cp('q', 0x030A)},
		{"unhandled mark is kept", cp('a', 0x0304), cp('a', 0x0304)},
		{"double mark composes the first only", cp('a', 0x0308, 0x0301), cp(0x00E4, 0x0301)},
		{"non latin passthrough", cp(0x03B1, 0x03B2), cp(0x03B1, 0x03B2)},
		{"mark after composed base is kept", cp(0x00FC, 0x0301), cp(0x00FC, 0x0301)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeNFC(tc.in); got != tc.want {
				t.Fatalf("NormalizeNFC(% x) = % x, want % x", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeNFCInvalidUTF8(t *testing.T) {
	// Invalid bytes pass through untouched, and a valid composition after them
	// still happens.
	in := "a\xffb" + cp('u', 0x0308)
	want := "a\xffb" + cp(0x00FC)
	if got := NormalizeNFC(in); got != want {
		t.Fatalf("NormalizeNFC(% x) = % x, want % x", in, got, want)
	}
}

func TestNormalizeNFCIsIdempotent(t *testing.T) {
	in := cp('Z', 'u', 0x0308, 'r', 'c', 'h', 'e', 'r')
	once := NormalizeNFC(in)
	if twice := NormalizeNFC(once); twice != once {
		t.Fatalf("NormalizeNFC is not idempotent: % x vs % x", once, twice)
	}
}

func TestNFCTableIsComplete(t *testing.T) {
	// Every entry composes and every composition is a single rune.
	for pair, composed := range nfcTable {
		base, mark := pair[0], pair[1]
		if base > 0x7F {
			t.Fatalf("base %U is not ASCII", base)
		}
		if !isComposableMark(mark) {
			t.Fatalf("mark %U is not in the composable set", mark)
		}
		if got := NormalizeNFC(cp(base, mark)); got != cp(composed) {
			t.Fatalf("NormalizeNFC(%U+%U) = % x, want %U", base, mark, got, composed)
		}
	}
}
