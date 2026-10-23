package model

import (
	"strings"
	"unicode/utf8"
)

// Combining marks this package composes. These are the marks that occur in the
// data of this landscape: Swiss, German, French and Nordic supplier and city
// names delivered from CP1252 or ISO-8859-1 sources.
const (
	combAcute      = '\u0301'
	combGrave      = '\u0300'
	combCircumflex = '\u0302'
	combTilde      = '\u0303'
	combDiaeresis  = '\u0308'
	combCedilla    = '\u0327'
	combRingAbove  = '\u030a'
)

// nfcTable maps a base letter and a following combining mark to the precomposed
// character. It is deliberately small: see NormalizeNFC for the exact scope.
var nfcTable = map[[2]rune]rune{
	// Combining grave accent.
	{'A', combGrave}: 'À', {'E', combGrave}: 'È', {'I', combGrave}: 'Ì',
	{'O', combGrave}: 'Ò', {'U', combGrave}: 'Ù',
	{'a', combGrave}: 'à', {'e', combGrave}: 'è', {'i', combGrave}: 'ì',
	{'o', combGrave}: 'ò', {'u', combGrave}: 'ù',

	// Combining acute accent.
	{'A', combAcute}: 'Á', {'E', combAcute}: 'É', {'I', combAcute}: 'Í',
	{'O', combAcute}: 'Ó', {'U', combAcute}: 'Ú', {'Y', combAcute}: 'Ý',
	{'C', combAcute}: 'Ć', {'L', combAcute}: 'Ĺ', {'N', combAcute}: 'Ń',
	{'R', combAcute}: 'Ŕ', {'S', combAcute}: 'Ś', {'Z', combAcute}: 'Ź',
	{'a', combAcute}: 'á', {'e', combAcute}: 'é', {'i', combAcute}: 'í',
	{'o', combAcute}: 'ó', {'u', combAcute}: 'ú', {'y', combAcute}: 'ý',
	{'c', combAcute}: 'ć', {'l', combAcute}: 'ĺ', {'n', combAcute}: 'ń',
	{'r', combAcute}: 'ŕ', {'s', combAcute}: 'ś', {'z', combAcute}: 'ź',

	// Combining circumflex accent.
	{'A', combCircumflex}: 'Â', {'E', combCircumflex}: 'Ê', {'I', combCircumflex}: 'Î',
	{'O', combCircumflex}: 'Ô', {'U', combCircumflex}: 'Û', {'C', combCircumflex}: 'Ĉ',
	{'G', combCircumflex}: 'Ĝ', {'H', combCircumflex}: 'Ĥ', {'J', combCircumflex}: 'Ĵ',
	{'S', combCircumflex}: 'Ŝ', {'W', combCircumflex}: 'Ŵ', {'Y', combCircumflex}: 'Ŷ',
	{'a', combCircumflex}: 'â', {'e', combCircumflex}: 'ê', {'i', combCircumflex}: 'î',
	{'o', combCircumflex}: 'ô', {'u', combCircumflex}: 'û', {'c', combCircumflex}: 'ĉ',
	{'g', combCircumflex}: 'ĝ', {'h', combCircumflex}: 'ĥ', {'j', combCircumflex}: 'ĵ',
	{'s', combCircumflex}: 'ŝ', {'w', combCircumflex}: 'ŵ', {'y', combCircumflex}: 'ŷ',

	// Combining tilde.
	{'A', combTilde}: 'Ã', {'I', combTilde}: 'Ĩ', {'N', combTilde}: 'Ñ',
	{'O', combTilde}: 'Õ', {'U', combTilde}: 'Ũ',
	{'a', combTilde}: 'ã', {'i', combTilde}: 'ĩ', {'n', combTilde}: 'ñ',
	{'o', combTilde}: 'õ', {'u', combTilde}: 'ũ',

	// Combining diaeresis.
	{'A', combDiaeresis}: 'Ä', {'E', combDiaeresis}: 'Ë', {'I', combDiaeresis}: 'Ï',
	{'O', combDiaeresis}: 'Ö', {'U', combDiaeresis}: 'Ü', {'Y', combDiaeresis}: 'Ÿ',
	{'a', combDiaeresis}: 'ä', {'e', combDiaeresis}: 'ë', {'i', combDiaeresis}: 'ï',
	{'o', combDiaeresis}: 'ö', {'u', combDiaeresis}: 'ü', {'y', combDiaeresis}: 'ÿ',

	// Combining cedilla.
	{'C', combCedilla}: 'Ç', {'G', combCedilla}: 'Ģ', {'K', combCedilla}: 'Ķ',
	{'L', combCedilla}: 'Ļ', {'N', combCedilla}: 'Ņ', {'R', combCedilla}: 'Ŗ',
	{'S', combCedilla}: 'Ş', {'T', combCedilla}: 'Ţ',
	{'c', combCedilla}: 'ç', {'g', combCedilla}: 'ģ', {'k', combCedilla}: 'ķ',
	{'l', combCedilla}: 'ļ', {'n', combCedilla}: 'ņ', {'r', combCedilla}: 'ŗ',
	{'s', combCedilla}: 'ş', {'t', combCedilla}: 'ţ',

	// Combining ring above.
	{'A', combRingAbove}: 'Å', {'U', combRingAbove}: 'Ů',
	{'a', combRingAbove}: 'å', {'u', combRingAbove}: 'ů',
}

// NormalizeNFC composes the decomposed accented letters of the Latin-1 range and
// its immediate neighbors: an ASCII letter followed by one combining acute,
// grave, circumflex, tilde, diaeresis, cedilla or ring above becomes the single
// precomposed character, so "Zürcher" and "Zürcher" hash identically.
//
// This is a deliberate subset of Unicode NFC, not an implementation of it. The
// module depends on the standard library only, so golang.org/x/text is not
// available and the full composition and canonical-ordering tables are not
// reproduced here. Specifically, it does NOT:
//
//   - reorder combining marks by canonical combining class,
//   - compose sequences of more than one mark per base letter beyond applying
//     them left to right (the second mark is left as-is),
//   - handle non-ASCII base letters, Hangul, or singleton and compatibility
//     mappings (NFKC is out of scope entirely),
//   - decompose anything: already-composed text is returned unchanged.
//
// Invalid UTF-8 is passed through byte for byte; the canonical JSON writer is
// where it is replaced. Text with no composable sequence is returned unchanged
// with no allocation.
func NormalizeNFC(s string) string {
	if !hasCombiningMark(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var pending rune = -1 // last emitted base character, or -1 if none
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if pending >= 0 {
				b.WriteRune(pending)
				pending = -1
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		if pending >= 0 && isComposableMark(r) {
			if composed, ok := nfcTable[[2]rune{pending, r}]; ok {
				pending = composed
				i += size
				continue
			}
		}
		if pending >= 0 {
			b.WriteRune(pending)
		}
		pending = r
		i += size
	}
	if pending >= 0 {
		b.WriteRune(pending)
	}
	return b.String()
}

// isComposableMark reports whether r is one of the combining marks this package
// composes.
func isComposableMark(r rune) bool {
	switch r {
	case combAcute, combGrave, combCircumflex, combTilde, combDiaeresis, combCedilla, combRingAbove:
		return true
	}
	return false
}

// hasCombiningMark reports whether s contains any composable combining mark. All
// of them encode to two bytes starting with 0xCC or 0xCD, so the scan is a byte
// scan and the common all-ASCII case costs one pass.
func hasCombiningMark(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != 0xcc && s[i] != 0xcd {
			continue
		}
		r, _ := utf8.DecodeRuneInString(s[i:])
		if isComposableMark(r) {
			return true
		}
	}
	return false
}
