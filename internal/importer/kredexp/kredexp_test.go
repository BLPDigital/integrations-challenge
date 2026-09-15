package kredexp

import (
	"context"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
)

// The tests in this file assert the harness contract and nothing about the
// format. Every one of them has to keep passing once the importer is
// implemented: they are here so the contract is checked from the first commit,
// and so there is somewhere obvious to add the format's own tests.
//
// The format's behavior is tested by the golden suite in internal/importer.

func TestIDIsFixed(t *testing.T) {
	// The grader looks the format up by this string, and a manifest names it in
	// its profile field. Changing it makes the importer unreachable.
	if got := New().ID(); got != ID {
		t.Fatalf("ID() = %q, want %q", got, ID)
	}
	if ID != "kredexp-2.1" {
		t.Fatalf("ID = %q, want kredexp-2.1", ID)
	}
	f, ok := importer.Lookup(ID)
	if !ok {
		t.Fatal("the format did not register itself; init has to call importer.Register(New())")
	}
	if f.ID() != ID {
		t.Fatalf("the registry holds %q under %q", f.ID(), ID)
	}
}

func TestParseRejectsANilContext(t *testing.T) {
	// A nil context is a programming fault, and the error return exists for
	// exactly this class of problem.
	//lint:ignore SA1012 passing a nil context is what is under test.
	if _, err := New().Parse(nil, []byte("VORLAUF;2.1"), importer.Options{}); err == nil {
		t.Fatal("Parse accepted a nil context")
	}
}

func TestParseNeverReturnsAnErrorForBadData(t *testing.T) {
	// The load-bearing rule. A defect in the file is a diagnostic on the
	// Result, never an error: a bad file must not be able to take a batch down,
	// and the caller always needs something to put in the receipt.
	inputs := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: nil},
		{name: "not the format at all", body: []byte("{\"supplier_number\":\"0000417\"}\n")},
		{name: "a byte order mark", body: append(append([]byte{}, importer.UTF8BOM...), []byte("VORLAUF;2.1;0100\r\n")...)},
		{name: "an undefined windows-1252 byte", body: []byte("VORLAUF;2.1;\x81\r\n")},
		{name: "an unterminated quote", body: []byte("KOPF;0004711;\"never closed\r\n")},
		{name: "only a trailer", body: []byte("NACHLAUF;1;3;1'345.63;1'244.80\r\n")},
		{name: "binary", body: []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			res, err := New().Parse(context.Background(), tc.body, importer.Options{
				Filename: "KRED_0100_20260131_001.txt",
				Encoding: importer.EncodingCP1252,
			})
			if err != nil {
				t.Fatalf("Parse returned an error for a data defect: %v", err)
			}
			if res == nil {
				t.Fatal("Parse returned a nil Result and a nil error")
			}
			if res.Documents == nil || res.Diagnostics == nil {
				t.Error("Documents and Diagnostics are always non-nil, so they encode as [] and never as null")
			}
			if res.Format != ID {
				t.Errorf("Result.Format = %q, want %q", res.Format, ID)
			}
			if res.Accepted {
				t.Error("a file this broken must not be Accepted")
			}
			if len(res.Diagnostics) == 0 {
				t.Error("a rejected file has to say why")
			}
			if _, ok := res.FirstFatal(); !ok {
				t.Error("a file that cannot be read carries a fatal diagnostic")
			}
		})
	}
}

func TestDiagnosticCodesAreThePublishedSet(t *testing.T) {
	// BUILD-SPEC 10 fixes this list. A code outside it cannot be graded,
	// because it is not published; a code missing from it cannot be asserted on.
	fatal := []string{
		CodeEncodingBOMPresent, CodeEncodingInvalidByte, CodeMalformedQuoting,
		CodeVorlaufMissing, CodeFormatVersionUnsupported, CodeTrailerMissing,
	}
	reject := []string{
		CodeUnknownRecordType, CodeFieldCountMismatch, CodeMissingRequiredField,
		CodeInvalidDate, CodeInvalidAmount, CodeOrphanLine,
	}
	other := []string{
		CodeTrailerCountMismatch, CodeTrailerSumMismatch, CodeTrailerNotVerified,
		CodeAmbiguousFieldSemantics, CodeDiagnosticsTruncated,
	}
	seen := map[string]bool{}
	for _, group := range [][]string{fatal, reject, other} {
		for _, c := range group {
			if c == "" {
				t.Error("a published code is the empty string")
			}
			if seen[c] {
				t.Errorf("code %q appears in two groups", c)
			}
			seen[c] = true
			if strings.ToLower(c) != c {
				t.Errorf("code %q is not lowercase like the rest of the set", c)
			}
		}
	}
	if len(seen) != 17 {
		t.Errorf("the published set has %d codes, want 17: 6 fatal, 6 reject, 2 trailer, 3 warnings with trailer_not_verified counted once", len(seen))
	}
	if CodeNotImplemented == "" {
		t.Error("the stub's own code is empty")
	}
	if seen[CodeNotImplemented] {
		t.Error("not_implemented is in the published set; it is the stub's marker and no fixture expects it")
	}
}

func TestFormatVersionIsTheOnlyAcceptedOne(t *testing.T) {
	if FormatVersion != "2.1" {
		t.Fatalf("FormatVersion = %q, want 2.1", FormatVersion)
	}
}

func TestDetectDoesNotClaimOtherFormatsFiles(t *testing.T) {
	// Whatever Detect ends up doing, it must not claim a canonical file: a
	// legacy export and a semicolon-separated canonical CSV both begin with
	// digits and semicolons, and only one of them has a header row.
	f := New()
	for _, body := range []string{
		"supplier_number;name;currency\n0000417;Steinbach;CHF\n",
		`[{"supplier_number":"0000417"}]`,
		`<records><record><supplier_number>0000417</supplier_number></record></records>`,
	} {
		if f.Detect(importer.Head([]byte(body)), importer.Options{}) {
			t.Errorf("Detect claimed a file of another format:\n%s", body)
		}
	}
}
