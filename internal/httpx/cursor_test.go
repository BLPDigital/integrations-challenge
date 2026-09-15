package httpx

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

var cursorSecret = []byte("scenario-secret-S1")

func TestCursorRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Cursor
	}{
		{"zero", Cursor{}},
		{"supplier", Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}},
		{"leading zeros preserved", Cursor{Dataset: "supplier", LastKey: "0000000", ChangeSeqHigh: 1}},
		{"po line composite key", Cursor{Dataset: "purchase_order_line", LastKey: "PO-004417\x1f00010", ChangeSeqHigh: 9}},
		{"unicode key", Cursor{Dataset: "supplier", LastKey: "Zürcher", ChangeSeqHigh: 2}},
		{"negative seq", Cursor{Dataset: "supplier", LastKey: "k", ChangeSeqHigh: -1}},
		{"empty key with seq", Cursor{Dataset: "fx_rate", ChangeSeqHigh: 7}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.in.Encode(cursorSecret)
			if strings.ContainsAny(s, "+/=") {
				t.Errorf("cursor %q is not url safe", s)
			}
			got, err := ParseCursor(s, cursorSecret)
			if err != nil {
				t.Fatalf("ParseCursor: %v", err)
			}
			if got != tc.in {
				t.Fatalf("round trip = %+v, want %+v", got, tc.in)
			}
		})
	}
}

func TestCursorIsStable(t *testing.T) {
	c := Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}
	first := c.Encode(cursorSecret)
	for i := 0; i < 100; i++ {
		if got := c.Encode(cursorSecret); got != first {
			t.Fatalf("iteration %d: cursor = %q, want %q", i, got, first)
		}
	}
	// The same logical position built independently encodes identically.
	same := Cursor{ChangeSeqHigh: 118422, LastKey: "0000417", Dataset: "supplier"}
	if got := same.Encode(cursorSecret); got != first {
		t.Fatalf("equal positions encode differently: %q vs %q", got, first)
	}
	// A golden value, so a change to the encoding cannot slip through: the
	// cursor of one position must stay byte-identical across releases.
	const golden = "eyJjaGFuZ2Vfc2VxX2hpZ2giOjExODQyMiwiZGF0YXNldCI6InN1cHBsaWVyIiwibGFzdF9rZXkiOiIwMDAwNDE3Iiwic2lnIjoiNjI2OTU0ZmJhZDgwMmI3YyJ9"
	if first != golden {
		t.Fatalf("cursor = %q, want the golden %q", first, golden)
	}
}

func TestCursorDistinguishesPositions(t *testing.T) {
	base := Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}
	others := []Cursor{
		{Dataset: "purchase_order", LastKey: "0000417", ChangeSeqHigh: 118422},
		{Dataset: "supplier", LastKey: "0000418", ChangeSeqHigh: 118422},
		{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118423},
		{Dataset: "supplier", LastKey: "417", ChangeSeqHigh: 118422},
		{Dataset: "supplie", LastKey: "r0000417", ChangeSeqHigh: 118422},
	}
	seen := map[string]bool{base.Encode(cursorSecret): true}
	for _, c := range others {
		s := c.Encode(cursorSecret)
		if seen[s] {
			t.Fatalf("%+v collides with an earlier cursor", c)
		}
		seen[s] = true
	}
}

func TestCursorTamperRejection(t *testing.T) {
	c := Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}
	valid := c.Encode(cursorSecret)
	raw, err := base64.RawURLEncoding.DecodeString(valid)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// A hand-crafted cursor: edit the payload and re-encode, keeping the
	// original signature.
	var env cursorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	forged := env
	forged.ChangeSeqHigh = 0
	forgedJSON, _ := json.Marshal(forged)

	forgedSig := env
	forgedSig.Sig = strings.Repeat("0", cursorSigLen)
	forgedSigJSON, _ := json.Marshal(forgedSig)

	unknownField := `{"change_seq_high":118422,"dataset":"supplier","last_key":"0000417","sig":"` + env.Sig + `","limit":5000}`
	trailing := string(raw) + `{"x":1}`

	tests := []struct {
		name string
		in   string
	}{
		{"payload edited", base64.RawURLEncoding.EncodeToString(forgedJSON)},
		{"signature zeroed", base64.RawURLEncoding.EncodeToString(forgedSigJSON)},
		{"unknown field", base64.RawURLEncoding.EncodeToString([]byte(unknownField))},
		{"trailing json", base64.RawURLEncoding.EncodeToString([]byte(trailing))},
		{"not base64", "not a cursor!"},
		{"not json", base64.RawURLEncoding.EncodeToString([]byte("just text"))},
		{"empty json", base64.RawURLEncoding.EncodeToString([]byte("{}"))},
		{"truncated", valid[:len(valid)-4]},
		{"one flipped byte", flipLastChar(valid)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCursor(tc.in, cursorSecret)
			if err == nil {
				t.Fatalf("tampered cursor accepted as %+v", got)
			}
			e := AsError(err)
			if e.Code != CodeInvalidCursor || e.Status != 400 {
				t.Fatalf("error = %+v", e)
			}
			if got != (Cursor{}) {
				t.Fatalf("rejected cursor returned %+v", got)
			}
		})
	}
}

// flipLastChar changes the final base64 character to a different one.
func flipLastChar(s string) string {
	last := s[len(s)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return s[:len(s)-1] + string(repl)
}

func TestCursorSecretIsolation(t *testing.T) {
	c := Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}
	s := c.Encode(cursorSecret)
	if _, err := ParseCursor(s, []byte("another-secret")); err == nil {
		t.Fatal("a cursor from another server was accepted")
	}
	if _, err := ParseCursor(s, nil); err == nil {
		t.Fatal("a cursor was accepted with no secret")
	}
	// Two servers with different secrets produce different cursors for the
	// same position.
	if s == c.Encode([]byte("another-secret")) {
		t.Fatal("the secret does not affect the cursor")
	}
}

func TestCursorEmptyIsTheStart(t *testing.T) {
	got, err := ParseCursor("", cursorSecret)
	if err != nil {
		t.Fatalf("empty cursor: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("empty cursor = %+v, want the zero position", got)
	}
	if (Cursor{Dataset: "x"}).IsZero() {
		t.Error("a non-zero cursor reports IsZero")
	}
}

func TestCursorAcceptsPaddedBase64(t *testing.T) {
	c := Cursor{Dataset: "supplier", LastKey: "0000417", ChangeSeqHigh: 118422}
	raw, err := base64.RawURLEncoding.DecodeString(c.Encode(cursorSecret))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	padded := base64.URLEncoding.EncodeToString(raw)
	got, err := ParseCursor(padded, cursorSecret)
	if err != nil {
		t.Fatalf("padded cursor rejected: %v", err)
	}
	if got != c {
		t.Fatalf("got %+v, want %+v", got, c)
	}
}
