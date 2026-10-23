package seed

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnmappableRune reports a rune that Windows-1252 cannot represent. The
// legacy encoder returns it rather than substituting '?' or U+FFFD: silently
// mangling a supplier name is exactly the failure this challenge grades, so the
// generator refuses to produce a file it could not have produced itself.
var ErrUnmappableRune = errors.New("seed: rune has no Windows-1252 representation")

// cp1252High maps the runes of the Windows-1252 0x80..0x9F block to their bytes.
// Windows-1252 agrees with ISO-8859-1 everywhere else, so the remaining ranges
// are handled arithmetically by [EncodeCP1252]. The five positions 0x81, 0x8D,
// 0x8F, 0x90 and 0x9D are undefined in Windows-1252 and deliberately absent:
// they only ever appear in the invalid fixtures of the Go task, never in a file
// this generator writes.
//
// The map is probed by key only and never ranged over.
var cp1252High = map[rune]byte{
	'€': 0x80, // EURO SIGN
	'‚': 0x82, // SINGLE LOW-9 QUOTATION MARK
	'ƒ': 0x83, // LATIN SMALL LETTER F WITH HOOK
	'„': 0x84, // DOUBLE LOW-9 QUOTATION MARK
	'…': 0x85, // HORIZONTAL ELLIPSIS
	'†': 0x86, // DAGGER
	'‡': 0x87, // DOUBLE DAGGER
	'ˆ': 0x88, // MODIFIER LETTER CIRCUMFLEX ACCENT
	'‰': 0x89, // PER MILLE SIGN
	'Š': 0x8A, // LATIN CAPITAL LETTER S WITH CARON
	'‹': 0x8B, // SINGLE LEFT-POINTING ANGLE QUOTATION MARK
	'Œ': 0x8C, // LATIN CAPITAL LIGATURE OE
	'Ž': 0x8E, // LATIN CAPITAL LETTER Z WITH CARON
	'‘': 0x91, // LEFT SINGLE QUOTATION MARK
	'’': 0x92, // RIGHT SINGLE QUOTATION MARK (the typographic apostrophe)
	'“': 0x93, // LEFT DOUBLE QUOTATION MARK
	'”': 0x94, // RIGHT DOUBLE QUOTATION MARK
	'•': 0x95, // BULLET
	'–': 0x96, // EN DASH
	'—': 0x97, // EM DASH
	'˜': 0x98, // SMALL TILDE
	'™': 0x99, // TRADE MARK SIGN
	'š': 0x9A, // LATIN SMALL LETTER S WITH CARON
	'›': 0x9B, // SINGLE RIGHT-POINTING ANGLE QUOTATION MARK
	'œ': 0x9C, // LATIN SMALL LIGATURE OE
	'ž': 0x9E, // LATIN SMALL LETTER Z WITH CARON
	'Ÿ': 0x9F, // LATIN CAPITAL LETTER Y WITH DIAERESIS
}

// EncodeCP1252 encodes a UTF-8 string as Windows-1252 bytes.
//
// It returns [ErrUnmappableRune] wrapped with the offending rune for any rune
// outside Windows-1252, and never substitutes a replacement character. The
// output carries no byte order mark: CP1252 has none, and a BOM in a KRED-EXP
// file is the first fatal diagnostic of the importer.
func EncodeCP1252(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			b, ok := cp1252High[r]
			if !ok {
				return nil, fmt.Errorf("%w: %q (U+%04X)", ErrUnmappableRune, r, r)
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// MustEncodeCP1252 is [EncodeCP1252] for text this package controls. It panics
// on an unmappable rune, which can only be a bug in a literal in this package.
func MustEncodeCP1252(s string) []byte {
	b, err := EncodeCP1252(s)
	if err != nil {
		panic(err)
	}
	return b
}

// DecodeCP1252 decodes Windows-1252 bytes to a UTF-8 string. It exists so tests
// can assert round-tripping without a second table, and so cmd/seed can report
// a readable excerpt of the bytes it wrote. The five undefined positions decode
// to U+FFFD, which is only reachable for input this package did not produce.
func DecodeCP1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80 || c >= 0xA0:
			sb.WriteRune(rune(c))
		default:
			sb.WriteRune(cp1252Low(c))
		}
	}
	return sb.String()
}

// cp1252Low returns the rune of a byte in the 0x80..0x9F block, or U+FFFD for
// the five undefined positions. It is a linear probe of [cp1252High] built into
// a fixed array so no map is ranged over.
func cp1252Low(c byte) rune {
	// Index 0 is 0x80. Zero means "undefined in Windows-1252".
	var table = [32]rune{
		'€', 0, '‚', 'ƒ', '„', '…', '†', '‡',
		'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
		0, '‘', '’', '“', '”', '•', '–', '—',
		'˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
	}
	if r := table[c-0x80]; r != 0 {
		return r
	}
	return '�'
}
