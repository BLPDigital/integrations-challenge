package seed

import (
	"bytes"
	"errors"
	"testing"
)

func TestEncodeCP1252(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []byte
	}{
		{"ascii", "KOPF;0004711", []byte("KOPF;0004711")},
		{"german umlauts", "Müller Bär Größe", []byte{
			'M', 0xFC, 'l', 'l', 'e', 'r', ' ', 'B', 0xE4, 'r', ' ', 'G', 'r', 0xF6, 0xDF, 'e'}},
		{"capital umlauts", "ÄÖÜ", []byte{0xC4, 0xD6, 0xDC}},
		{"euro sign", "€", []byte{0x80}},
		{"typographic apostrophe", "’26", []byte{0x92, '2', '6'}},
		{"en dash", "–", []byte{0x96}},
		{"em dash", "—", []byte{0x97}},
		{"non breaking space", " ", []byte{0xA0}},
		{"crlf survives", "a\r\nb", []byte{'a', 0x0D, 0x0A, 'b'}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeCP1252(tc.in)
			if err != nil {
				t.Fatalf("EncodeCP1252(%q): %v", tc.in, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("EncodeCP1252(%q) = % x, want % x", tc.in, got, tc.want)
			}
			if back := DecodeCP1252(got); back != tc.in {
				t.Errorf("round trip of %q gave %q", tc.in, back)
			}
		})
	}
}

func TestEncodeCP1252RefusesUnmappable(t *testing.T) {
	// A rune Windows-1252 cannot represent is an error, never a substituted '?'
	// or U+FFFD: silently mangling a creditor name is the failure this landscape
	// is built to catch.
	for _, in := range []string{"日本", "Ł", "→", " ", "𝄞"} {
		if _, err := EncodeCP1252(in); !errors.Is(err, ErrUnmappableRune) {
			t.Errorf("EncodeCP1252(%q) = %v, want ErrUnmappableRune", in, err)
		}
	}
}

func TestEncodeCP1252NeverEmitsUndefinedBytes(t *testing.T) {
	// The five undefined Windows-1252 positions must be unreachable: they belong
	// only to the invalid fixtures of the Go task.
	undefined := map[byte]bool{0x81: true, 0x8D: true, 0x8F: true, 0x90: true, 0x9D: true}
	for r := rune(0); r < 0x11000; r++ {
		got, err := EncodeCP1252(string(r))
		if err != nil {
			continue
		}
		for _, c := range got {
			if undefined[c] {
				t.Fatalf("U+%04X encoded to the undefined position %#x", r, c)
			}
		}
	}
}

func TestDeliveriesCarryNoBOM(t *testing.T) {
	d, err := Generate(ScenarioS0, 3)
	if err != nil {
		t.Fatal(err)
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	for _, del := range d.Deliveries {
		if bytes.HasPrefix(del.Bytes, bom) {
			t.Errorf("delivery %s starts with a UTF-8 BOM", del.Name)
		}
		if bytes.HasPrefix(del.Bytes, []byte{0xFF, 0xFE}) || bytes.HasPrefix(del.Bytes, []byte{0xFE, 0xFF}) {
			t.Errorf("delivery %s starts with a UTF-16 BOM", del.Name)
		}
	}
}

func TestMustEncodeCP1252(t *testing.T) {
	if got := string(MustEncodeCP1252("Bär")); got != "B\xe4r" {
		t.Errorf("MustEncodeCP1252 gave %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustEncodeCP1252 did not panic on an unmappable rune")
		}
	}()
	MustEncodeCP1252("日本")
}

func TestMandantMapping(t *testing.T) {
	tests := []struct{ mandant, companyCode string }{
		{MandantCH10, "CH10"},
		{MandantCH20, "CH20"},
	}
	for _, tc := range tests {
		cc, err := CompanyCodeForMandant(tc.mandant)
		if err != nil || cc != tc.companyCode {
			t.Errorf("CompanyCodeForMandant(%q) = (%q, %v)", tc.mandant, cc, err)
		}
		m, err := MandantForCompanyCode(tc.companyCode)
		if err != nil || m != tc.mandant {
			t.Errorf("MandantForCompanyCode(%q) = (%q, %v)", tc.companyCode, m, err)
		}
	}
	if _, err := CompanyCodeForMandant("0300"); err == nil {
		t.Error("an unknown Mandant was accepted")
	}
	if _, err := MandantForCompanyCode("DE10"); err == nil {
		t.Error("an unknown company code was accepted")
	}
}
