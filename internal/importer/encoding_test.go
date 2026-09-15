package importer

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCP1252Rune(t *testing.T) {
	tests := []struct {
		name string
		b    byte
		want rune
		ok   bool
	}{
		{name: "ASCII", b: 'A', want: 'A', ok: true},
		{name: "NUL", b: 0x00, want: 0x00, ok: true},
		{name: "euro sign", b: 0x80, want: '€', ok: true},
		{name: "typographic apostrophe", b: 0x92, want: '’', ok: true},
		{name: "en dash", b: 0x96, want: '–', ok: true},
		{name: "em dash", b: 0x97, want: '—', ok: true},
		{name: "capital S with caron", b: 0x8A, want: 'Š', ok: true},
		{name: "capital Y with diaeresis", b: 0x9F, want: 'Ÿ', ok: true},
		{name: "no-break space", b: 0xA0, want: 0xA0, ok: true},
		{name: "u umlaut", b: 0xFC, want: 'ü', ok: true},
		{name: "a with grave", b: 0xE0, want: 'à', ok: true},

		{name: "undefined 0x81", b: 0x81},
		{name: "undefined 0x8D", b: 0x8D},
		{name: "undefined 0x8F", b: 0x8F},
		{name: "undefined 0x90", b: 0x90},
		{name: "undefined 0x9D", b: 0x9D},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CP1252Rune(tc.b)
			if ok != tc.ok {
				t.Fatalf("CP1252Rune(0x%02X) ok = %v, want %v", tc.b, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("CP1252Rune(0x%02X) = %q (U+%04X), want %q", tc.b, got, got, tc.want)
			}
		})
	}
}

func TestCP1252TableIsComplete(t *testing.T) {
	// windows-1252 defines 27 of the 32 C1 positions. A table with 26 or 28
	// entries is a transcription mistake, and the way it shows up in production
	// is one supplier name in a thousand that will not decode.
	defined := 0
	for b := 0x80; b <= 0x9F; b++ {
		if _, ok := CP1252Rune(byte(b)); ok {
			defined++
		}
	}
	if defined != 27 {
		t.Fatalf("windows-1252 defines %d of the 32 C1 positions, want 27", defined)
	}
	// Every defined mapping must be distinct: two positions decoding to the
	// same character would silently merge two different source bytes.
	seen := map[rune]int{}
	for b := 0x00; b <= 0xFF; b++ {
		r, ok := CP1252Rune(byte(b))
		if !ok {
			continue
		}
		if prev, dup := seen[r]; dup {
			t.Fatalf("bytes 0x%02X and 0x%02X both decode to %q", prev, b, r)
		}
		seen[r] = b
	}
}

func TestDecodeString(t *testing.T) {
	tests := []struct {
		name string
		enc  string
		in   []byte
		want string
	}{
		{name: "utf8", enc: EncodingUTF8, in: []byte("Zürcher"), want: "Zürcher"},
		{name: "utf8 default", enc: "", in: []byte("Zürcher"), want: "Zürcher"},
		{name: "utf8 with a bom", enc: EncodingUTF8BOM, in: append(append([]byte{}, UTF8BOM...), []byte("ok")...), want: "ok"},
		{name: "utf8 declared without a bom but carrying one", enc: EncodingUTF8, in: append(append([]byte{}, UTF8BOM...), []byte("ok")...), want: "ok"},
		{name: "latin1", enc: EncodingLatin1, in: []byte{0x5A, 0xFC, 0x72, 0x69}, want: "Züri"},
		{name: "latin1 decodes the bom bytes as characters", enc: EncodingLatin1, in: UTF8BOM, want: "ï»¿"},
		{name: "cp1252 euro", enc: EncodingCP1252, in: []byte{0x80, 0x20, 0x31}, want: "€ 1"},
		{name: "cp1252 apostrophe grouping", enc: EncodingCP1252, in: []byte{0x31, 0x92, 0x32, 0x33, 0x34}, want: "1’234"},
		{name: "cp1252 umlaut", enc: EncodingCP1252, in: []byte{0x5A, 0xFC, 0x72}, want: "Zür"},
		{name: "empty", enc: EncodingCP1252, in: nil, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeString(tc.enc, tc.in)
			if err != nil {
				t.Fatalf("DecodeString error = %v", err)
			}
			if got != tc.want {
				t.Errorf("DecodeString = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeStringReportsInvalidBytes(t *testing.T) {
	tests := []struct {
		name       string
		enc        string
		in         []byte
		wantPrefix string
		wantOffset int64
		wantByte   byte
	}{
		{name: "invalid utf8 continuation", enc: EncodingUTF8, in: []byte{'o', 'k', 0xC3, 0x28}, wantPrefix: "ok", wantOffset: 2, wantByte: 0xC3},
		{name: "lone utf8 continuation", enc: EncodingUTF8, in: []byte{'a', 0x80}, wantPrefix: "a", wantOffset: 1, wantByte: 0x80},
		{name: "truncated utf8 at eof", enc: EncodingUTF8, in: []byte{'a', 0xE2, 0x82}, wantPrefix: "a", wantOffset: 1, wantByte: 0xE2},
		{name: "undefined cp1252 position", enc: EncodingCP1252, in: []byte{'a', 0x81, 'b'}, wantPrefix: "a", wantOffset: 1, wantByte: 0x81},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeString(tc.enc, tc.in)
			var enc *EncodingError
			if !errors.As(err, &enc) {
				t.Fatalf("error = %v, want an *EncodingError", err)
			}
			if enc.Offset != tc.wantOffset || enc.Byte != tc.wantByte {
				t.Errorf("EncodingError = offset %d byte 0x%02X, want offset %d byte 0x%02X",
					enc.Offset, enc.Byte, tc.wantOffset, tc.wantByte)
			}
			if got != tc.wantPrefix {
				t.Errorf("decoded prefix = %q, want %q", got, tc.wantPrefix)
			}
			if strings.ContainsRune(got, '�') {
				t.Error("the decoder emitted U+FFFD; an invalid byte is reported, never replaced")
			}
		})
	}
}

func TestNewDecoderRejectsUnknownEncoding(t *testing.T) {
	if _, err := NewDecoder("EBCDIC", bytes.NewReader(nil)); !errors.Is(err, ErrEncodingUnsupported) {
		t.Fatalf("error = %v, want ErrEncodingUnsupported", err)
	}
	if !KnownEncoding("") || !KnownEncoding(EncodingCP1252) || KnownEncoding("EBCDIC") {
		t.Error("KnownEncoding disagrees with NewDecoder")
	}
	if len(Encodings()) != 4 {
		t.Errorf("Encodings() = %v, want the four declarable encodings", Encodings())
	}
}

// oneByteReader hands over a single byte per Read, so a multi-byte character and
// a byte order mark are always split across reads.
type oneByteReader struct{ rest []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.rest[0]
	r.rest = r.rest[1:]
	return 1, nil
}

func TestDecoderHandlesSplitReads(t *testing.T) {
	// A rune split across two source reads is not an invalid rune, and a byte
	// order mark split across three is still a byte order mark. A decoder that
	// gets this wrong works on every test fixture and fails on the first file
	// that arrives over a socket.
	for _, tc := range []struct {
		name string
		enc  string
		in   []byte
		want string
	}{
		{name: "utf8 multi-byte", enc: EncodingUTF8, in: []byte("Zürich – 1’234 €"), want: "Zürich – 1’234 €"},
		{name: "utf8 bom", enc: EncodingUTF8BOM, in: append(append([]byte{}, UTF8BOM...), []byte("Zürich")...), want: "Zürich"},
		{name: "cp1252", enc: EncodingCP1252, in: []byte{0x5A, 0xFC, 0x72, 0x69, 0x92, 0x73}, want: "Züri’s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := NewDecoder(tc.enc, &oneByteReader{rest: tc.in})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := out.ReadFrom(dec); err != nil {
				t.Fatalf("ReadFrom error = %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("decoded %q, want %q", out.String(), tc.want)
			}
		})
	}
}

func TestStripBOM(t *testing.T) {
	in := append(append([]byte{}, UTF8BOM...), 'V')
	out, had := StripBOM(in)
	if !had || string(out) != "V" {
		t.Fatalf("StripBOM = %q, %v; want \"V\", true", out, had)
	}
	out, had = StripBOM([]byte("VORLAUF"))
	if had || string(out) != "VORLAUF" {
		t.Fatalf("StripBOM = %q, %v; want \"VORLAUF\", false", out, had)
	}
}

func TestLatin1RuneIsIdentity(t *testing.T) {
	for b := 0x00; b <= 0xFF; b++ {
		if got := Latin1Rune(byte(b)); got != rune(b) {
			t.Fatalf("Latin1Rune(0x%02X) = U+%04X, want U+%04X", b, got, b)
		}
	}
}

func TestEncodingErrorMessage(t *testing.T) {
	e := &EncodingError{Encoding: EncodingCP1252, Offset: 4117, Byte: 0x81}
	got := e.Error()
	for _, must := range []string{EncodingCP1252, "0x81", "4117"} {
		if !strings.Contains(got, must) {
			t.Errorf("EncodingError.Error() = %q, want it to name %q", got, must)
		}
	}
}
