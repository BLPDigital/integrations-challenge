package importer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// The encodings a manifest may declare (BUILD-SPEC 8.1). Nothing else is
// accepted: an unknown encoding is rejected rather than sniffed, because a
// mis-sniffed single-byte encoding turns "Zürich" into "ZÃ¼rich" and the
// corruption survives into the ledger.
const (
	// EncodingUTF8 is UTF-8 with no byte order mark. It is the default.
	EncodingUTF8 = "UTF-8"
	// EncodingUTF8BOM is UTF-8 introduced by a byte order mark.
	EncodingUTF8BOM = "UTF-8-BOM"
	// EncodingLatin1 is ISO 8859-1, in which every byte is the code point of
	// the same value.
	EncodingLatin1 = "ISO-8859-1"
	// EncodingCP1252 is windows-1252: ISO 8859-1 with 27 printable characters
	// in the C1 range. It is what a Swiss Windows accounting system writes, and
	// the reason typographic apostrophes, en dashes and euro signs arrive in a
	// file that claims to be Latin-1.
	EncodingCP1252 = "windows-1252"
)

// UTF8BOM is the UTF-8 byte order mark, EF BB BF.
var UTF8BOM = []byte{0xEF, 0xBB, 0xBF}

// ErrEncodingUnsupported reports a declared encoding outside the four.
var ErrEncodingUnsupported = errors.New("importer: unsupported encoding")

// An EncodingError reports a byte the declared encoding cannot represent: an
// invalid UTF-8 sequence, or one of the five undefined windows-1252 positions
// (0x81, 0x8D, 0x8F, 0x90, 0x9D).
//
// It travels as an error rather than as a [Diagnostic] because a decoder does
// not know where in a record the offending byte falls. The caller catches it,
// converts it into a [CodeEncoding] diagnostic on the record it was reading, and
// continues or stops according to its own format's rules. What a decoder must
// never do is emit U+FFFD: a replacement character in a supplier name is a
// corruption that looks like data.
type EncodingError struct {
	// Encoding is the encoding that rejected the byte.
	Encoding string
	// Offset is the 0-based byte offset of the offending byte in the source.
	Offset int64
	// Byte is the offending byte.
	Byte byte
}

// Error implements error.
func (e *EncodingError) Error() string {
	return fmt.Sprintf("importer: invalid %s byte 0x%02X at offset %d", e.Encoding, e.Byte, e.Offset)
}

// Encodings returns the declarable encodings in ascending byte order.
func Encodings() []string {
	out := []string{EncodingCP1252, EncodingLatin1, EncodingUTF8, EncodingUTF8BOM}
	sort.Strings(out)
	return out
}

// KnownEncoding reports whether enc is one of the four declarable encodings. An
// empty enc is [EncodingUTF8] and is therefore known.
func KnownEncoding(enc string) bool {
	switch enc {
	case "", EncodingUTF8, EncodingUTF8BOM, EncodingLatin1, EncodingCP1252:
		return true
	}
	return false
}

// cp1252High maps the windows-1252 positions 0x80..0x9F, in order, to their code
// points. The five undefined positions hold 0, which no valid mapping uses.
//
// This is the whole table, spelled out, because the standard library has no
// character set conversions and golang.org/x/text is not available: the module
// depends on the standard library only. It is also the table the legacy
// KRED-EXP importer needs, which is why it lives in the framework and not in one
// format.
var cp1252High = [32]rune{
	'€', // 0x80 EURO SIGN
	0,   // 0x81 undefined
	'‚', // 0x82 SINGLE LOW-9 QUOTATION MARK
	'ƒ', // 0x83 LATIN SMALL LETTER F WITH HOOK
	'„', // 0x84 DOUBLE LOW-9 QUOTATION MARK
	'…', // 0x85 HORIZONTAL ELLIPSIS
	'†', // 0x86 DAGGER
	'‡', // 0x87 DOUBLE DAGGER
	'ˆ', // 0x88 MODIFIER LETTER CIRCUMFLEX ACCENT
	'‰', // 0x89 PER MILLE SIGN
	'Š', // 0x8A LATIN CAPITAL LETTER S WITH CARON
	'‹', // 0x8B SINGLE LEFT-POINTING ANGLE QUOTATION MARK
	'Œ', // 0x8C LATIN CAPITAL LIGATURE OE
	0,   // 0x8D undefined
	'Ž', // 0x8E LATIN CAPITAL LETTER Z WITH CARON
	0,   // 0x8F undefined
	0,   // 0x90 undefined
	'‘', // 0x91 LEFT SINGLE QUOTATION MARK
	'’', // 0x92 RIGHT SINGLE QUOTATION MARK, the typographic apostrophe
	'“', // 0x93 LEFT DOUBLE QUOTATION MARK
	'”', // 0x94 RIGHT DOUBLE QUOTATION MARK
	'•', // 0x95 BULLET
	'–', // 0x96 EN DASH
	'—', // 0x97 EM DASH
	'˜', // 0x98 SMALL TILDE
	'™', // 0x99 TRADE MARK SIGN
	'š', // 0x9A LATIN SMALL LETTER S WITH CARON
	'›', // 0x9B SINGLE RIGHT-POINTING ANGLE QUOTATION MARK
	'œ', // 0x9C LATIN SMALL LIGATURE OE
	0,   // 0x9D undefined
	'ž', // 0x9E LATIN SMALL LETTER Z WITH CARON
	'Ÿ', // 0x9F LATIN CAPITAL LETTER Y WITH DIAERESIS
}

// CP1252Rune returns the code point windows-1252 assigns to b, and whether the
// position is defined. Bytes 0x00..0x7F and 0xA0..0xFF map to the code point of
// the same value, exactly as in ISO 8859-1; bytes 0x80..0x9F come from the table
// above, of which five positions are undefined and report false.
//
// A format decoding a legacy file byte by byte uses it directly:
//
//	r, ok := importer.CP1252Rune(b)
//	if !ok {
//	    // report this format's own encoding diagnostic at this record
//	}
func CP1252Rune(b byte) (rune, bool) {
	if b < 0x80 || b > 0x9F {
		return rune(b), true
	}
	if r := cp1252High[b-0x80]; r != 0 {
		return r, true
	}
	return 0, false
}

// Latin1Rune returns the code point ISO 8859-1 assigns to b, which is b itself.
// It has no ok result because ISO 8859-1 defines every byte, which is exactly
// why a sender who writes windows-1252 can declare Latin-1 and nobody notices
// until a euro sign arrives.
func Latin1Rune(b byte) rune { return rune(b) }

// NewDecoder wraps r in a reader that yields UTF-8 for the declared encoding.
// An empty enc is [EncodingUTF8]; an unknown one is [ErrEncodingUnsupported].
//
// For [EncodingUTF8] and [EncodingUTF8BOM] a leading byte order mark is consumed
// and the rest is validated, never repaired: an invalid sequence is an
// [EncodingError] carrying the byte and its offset, so the caller can place a
// [CodeEncoding] diagnostic on the record it was reading. The two spellings
// differ only in intent, and a BOM is consumed for both, because a format whose
// specification forbids a BOM outright - the legacy export declares "no byte
// order mark" - inspects the first three bytes with [StripBOM] before decoding
// and reports its own fatal diagnostic.
//
// A single-byte encoding never has a BOM: EF BB BF in ISO 8859-1 is the three
// perfectly valid characters "ï»¿", so those bytes are decoded, not consumed.
//
// The returned reader never owns r and never closes it. Reads are bounded: the
// decoder holds one source chunk plus at most one incomplete rune.
func NewDecoder(enc string, r io.Reader) (io.Reader, error) {
	switch enc {
	case "", EncodingUTF8, EncodingUTF8BOM:
		return &decoder{src: r, enc: EncodingUTF8}, nil
	case EncodingLatin1:
		return &decoder{src: r, enc: EncodingLatin1, single: latin1Defined, bomDone: true}, nil
	case EncodingCP1252:
		return &decoder{src: r, enc: EncodingCP1252, single: CP1252Rune, bomDone: true}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrEncodingUnsupported, enc)
	}
}

// latin1Defined adapts [Latin1Rune] to the decoder's signature.
func latin1Defined(b byte) (rune, bool) { return Latin1Rune(b), true }

// DecodeString decodes b from the declared encoding into a UTF-8 string. It is
// the whole-slice form of [NewDecoder], for a caller that already holds the
// bytes: a small file, a header line, a test. A caller reading a file uses
// NewDecoder, because a 20 MB legacy export should not be held twice.
//
// On an [EncodingError] it returns the prefix decoded so far together with the
// error, so a caller can report the finding and still show what came before it.
func DecodeString(enc string, b []byte) (string, error) {
	dec, err := NewDecoder(enc, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(dec); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// decoder converts a source stream into UTF-8. For a single-byte encoding,
// single maps one byte to one rune; for UTF-8 it is nil and the stream is
// validated instead.
type decoder struct {
	src    io.Reader
	enc    string
	single func(byte) (rune, bool)

	in      []byte // source bytes read but not yet decoded
	out     []byte // decoded UTF-8 not yet delivered
	offset  int64  // source offset of in[0]
	srcErr  error  // sticky error from src, io.EOF included
	failed  error  // sticky decode error, delivered once out has drained
	bomDone bool
	scratch [utf8.UTFMax]byte
}

// Read implements io.Reader. It delivers decoded bytes first and the sticky
// decode or source error only once nothing decoded is left, so a caller always
// sees every byte that was valid before the offending one.
func (d *decoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(d.out) == 0 {
		if d.failed != nil {
			return 0, d.failed
		}
		if d.srcErr != nil && len(d.in) == 0 {
			return 0, d.srcErr
		}
		d.step()
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	if len(d.out) == 0 {
		d.out = nil
	}
	return n, nil
}

// step reads more source when it needs to and decodes as much of what it holds
// as it can. It makes progress on every call: either it reads, or it decodes, or
// it sets a sticky error.
func (d *decoder) step() {
	if d.srcErr == nil && len(d.in) < utf8.UTFMax {
		buf := make([]byte, 4096)
		n, err := d.src.Read(buf)
		if n > 0 {
			d.in = append(d.in, buf[:n]...)
		}
		if err != nil {
			d.srcErr = err
		}
		if n == 0 && err == nil {
			return
		}
	}
	if !d.bomDone {
		if len(d.in) < len(UTF8BOM) && d.srcErr == nil {
			return
		}
		if trimmed, had := StripBOM(d.in); had {
			d.in = trimmed
			d.offset += int64(len(UTF8BOM))
		}
		d.bomDone = true
	}
	if d.single != nil {
		d.decodeSingle()
		return
	}
	d.decodeUTF8()
}

// decodeSingle decodes every buffered byte through the single-byte table,
// stopping at the first undefined position.
func (d *decoder) decodeSingle() {
	out := make([]byte, 0, len(d.in)*2)
	for i, b := range d.in {
		r, ok := d.single(b)
		if !ok {
			d.failed = &EncodingError{Encoding: d.enc, Offset: d.offset + int64(i), Byte: b}
			d.in = nil
			d.out = out
			return
		}
		n := utf8.EncodeRune(d.scratch[:], r)
		out = append(out, d.scratch[:n]...)
	}
	d.offset += int64(len(d.in))
	d.in = nil
	d.out = out
}

// decodeUTF8 validates the buffered bytes and passes the valid prefix through. A
// sequence that is merely incomplete is left in the buffer for the next source
// read, unless the source has ended, in which case it is invalid.
func (d *decoder) decodeUTF8() {
	atEOF := d.srcErr != nil
	i := 0
	for i < len(d.in) {
		c := d.in[i]
		if c < utf8.RuneSelf {
			i++
			continue
		}
		if !atEOF && !utf8.FullRune(d.in[i:]) {
			break
		}
		r, size := utf8.DecodeRune(d.in[i:])
		if r == utf8.RuneError && size <= 1 {
			d.failed = &EncodingError{Encoding: d.enc, Offset: d.offset + int64(i), Byte: c}
			break
		}
		i += size
	}
	if i > 0 {
		d.out = append([]byte(nil), d.in[:i]...)
		d.in = d.in[i:]
		d.offset += int64(i)
	}
	if d.failed != nil {
		d.in = nil
	}
}

// StripBOM returns b without a leading UTF-8 byte order mark, and whether one
// was present. A format whose specification forbids a BOM checks for it before
// decoding, so it can report its own fatal diagnostic rather than parse a header
// field that begins with three invisible bytes.
func StripBOM(b []byte) ([]byte, bool) {
	if len(b) >= 3 && b[0] == UTF8BOM[0] && b[1] == UTF8BOM[1] && b[2] == UTF8BOM[2] {
		return b[3:], true
	}
	return b, false
}
