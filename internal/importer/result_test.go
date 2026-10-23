package importer

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestBuilderAccepted(t *testing.T) {
	// The exact acceptance rule of BUILD-SPEC 10, one row per way it can fail.
	tests := []struct {
		name     string
		diags    []Diagnostic
		totals   Totals
		accepted bool
	}{
		{name: "clean file with no trailer contract", accepted: true},
		{name: "warnings alone do not block",
			diags:    []Diagnostic{{Code: CodeAmbiguousFieldSemantics, Severity: SeverityWarn}},
			accepted: true},
		{name: "one reject blocks",
			diags: []Diagnostic{{Code: CodeDecimalFormat, Severity: SeverityReject}}},
		{name: "one fatal blocks",
			diags: []Diagnostic{{Code: CodeMalformedDocument, Severity: SeverityFatal}}},
		{name: "a declared trailer that verified is accepted",
			totals:   Totals{TrailerDeclared: true, TrailerVerified: true},
			accepted: true},
		{name: "a declared trailer that did not verify blocks",
			totals: Totals{TrailerDeclared: true, TrailerVerified: false}},
		{name: "an undeclared trailer is satisfied vacuously",
			totals:   Totals{TrailerDeclared: false, TrailerVerified: false},
			accepted: true},
		{name: "a reject forces the trailer unverified even when the format claimed otherwise",
			diags:  []Diagnostic{{Code: CodeDecimalFormat, Severity: SeverityReject}},
			totals: Totals{TrailerDeclared: true, TrailerVerified: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder("test", Options{})
			for _, d := range tc.diags {
				d.Message = "m"
				b.Add(d)
			}
			b.SetTotals(tc.totals)
			r := b.Result()
			if r.Accepted != tc.accepted {
				t.Errorf("Accepted = %v, want %v", r.Accepted, tc.accepted)
			}
			if tc.diags != nil && len(tc.diags) > 0 && r.Accepted && r.Count(SeverityWarn) == 0 {
				t.Error("a blocking diagnostic was reported and the file was accepted anyway")
			}
		})
	}
}

func TestBuilderResultIsNeverNil(t *testing.T) {
	r := NewBuilder("test", Options{}).Result()
	if r.Documents == nil {
		t.Error("Documents is nil; an empty file must carry an empty slice so it encodes as [] and not null")
	}
	if r.Diagnostics == nil {
		t.Error("Diagnostics is nil")
	}
	enc, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"documents":[]`, `"diagnostics":[]`} {
		if !strings.Contains(string(enc), want) {
			t.Errorf("encoded Result = %s, want it to contain %s", enc, want)
		}
	}
}

func TestDiagnosticSortOrder(t *testing.T) {
	b := NewBuilder("test", Options{})
	add := func(line, record int, field, code string) {
		b.Add(Diagnostic{Code: code, Severity: SeverityWarn, Line: line, Record: record, Field: field, Message: "m"})
	}
	// Deliberately out of order in every key.
	add(9, 9, "zzz", "z_code")
	add(1, 1, "b_field", "b_code")
	add(1, 1, "b_field", "a_code")
	add(1, 1, "a_field", "z_code")
	add(1, 2, "a_field", "a_code")
	add(0, 0, "", "file_level")

	got := b.Result().Diagnostics
	want := []string{"file_level", "z_code", "a_code", "b_code", "a_code", "z_code"}
	if len(got) != len(want) {
		t.Fatalf("got %d diagnostics, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Code != want[i] {
			t.Errorf("diagnostic %d = %s (line %d, record %d, field %q), want %s",
				i, got[i].Code, got[i].Line, got[i].Record, got[i].Field, want[i])
		}
	}
}

func TestDiagnosticSortIsStableForIdenticalTuples(t *testing.T) {
	b := NewBuilder("test", Options{})
	for i := 0; i < 20; i++ {
		b.Add(Diagnostic{Code: "same", Severity: SeverityWarn, Line: 4, Record: 4, Field: "f",
			Value: strconv.Itoa(i), Message: "m"})
	}
	for i, d := range b.Result().Diagnostics {
		if d.Value != strconv.Itoa(i) {
			t.Fatalf("diagnostic %d has value %q; ties must keep emission order", i, d.Value)
		}
	}
}

func TestDiagnosticsTruncation(t *testing.T) {
	b := NewBuilder("test", Options{})
	const n = MaxDiagnostics + 250
	for i := 0; i < n; i++ {
		b.Add(Diagnostic{Code: "reject_me", Severity: SeverityReject, Line: i + 1, Message: "m"})
	}
	r := b.Result()
	if len(r.Diagnostics) != MaxDiagnostics+1 {
		t.Fatalf("kept %d diagnostics, want %d plus one truncation warning", len(r.Diagnostics), MaxDiagnostics)
	}
	last := r.Diagnostics[len(r.Diagnostics)-1]
	if last.Code != CodeDiagnosticsTruncated || last.Severity != SeverityWarn {
		t.Fatalf("last diagnostic = %s/%s, want %s/warn", last.Code, last.Severity, CodeDiagnosticsTruncated)
	}
	if !strings.Contains(last.Message, "250") {
		t.Errorf("truncation message = %q, want it to say how many were dropped", last.Message)
	}
	// The kept findings are the earliest ones in the file, which is what an
	// operator reads first.
	if r.Diagnostics[0].Line != 1 || r.Diagnostics[MaxDiagnostics-1].Line != MaxDiagnostics {
		t.Error("truncation did not keep the earliest findings")
	}
}

func TestAcceptedIsComputedBeforeTruncation(t *testing.T) {
	// A thousand rejects on the first thousand lines and one fatal far beyond
	// the cap. The fatal is dropped from the report and must still be counted:
	// a file that cannot be read is not accepted because its worst finding did
	// not fit in the diagnostics list.
	b := NewBuilder("test", Options{})
	for i := 0; i < MaxDiagnostics; i++ {
		b.Add(Diagnostic{Code: "reject_me", Severity: SeverityReject, Line: i + 1, Message: "m"})
	}
	b.Add(Diagnostic{Code: CodeMalformedDocument, Severity: SeverityFatal, Line: 50000, Message: "m"})
	r := b.Result()
	if r.Accepted {
		t.Fatal("the file was accepted although a fatal diagnostic was reported")
	}
	fatal, reject, _ := b.Counts()
	if fatal != 1 || reject != MaxDiagnostics {
		t.Errorf("Counts() = %d fatal, %d reject; want 1 and %d", fatal, reject, MaxDiagnostics)
	}
	if _, ok := r.FirstFatal(); ok {
		t.Error("the truncated Result reports a fatal diagnostic it does not carry")
	}
}

func TestBuilderAddPanics(t *testing.T) {
	tests := []struct {
		name string
		d    Diagnostic
	}{
		{name: "no code", d: Diagnostic{Severity: SeverityWarn}},
		{name: "no severity", d: Diagnostic{Code: "x"}},
		{name: "unknown severity", d: Diagnostic{Code: "x", Severity: Severity("info")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Builder.Add did not panic")
				}
			}()
			NewBuilder("test", Options{}).Add(tc.d)
		})
	}
}

func TestBuilderAssignsOrdinals(t *testing.T) {
	b := NewBuilder("test", Options{})
	for i := 0; i < 5; i++ {
		// A format that assigns its own ordinal, wrongly, must not be believed.
		b.AddDocument(Document{Dataset: "supplier", Key: "k", Ordinal: 99})
	}
	for i, d := range b.Result().Documents {
		if d.Ordinal != i+1 {
			t.Fatalf("document %d has ordinal %d, want %d", i, d.Ordinal, i+1)
		}
	}
}

func TestBuilderRecordsDeclaredCount(t *testing.T) {
	b := NewBuilder("test", Options{DeclaredRecords: 12000})
	r := b.Result()
	if !r.Totals.TrailerDeclared || r.Totals.DeclaredDocuments != 12000 {
		t.Fatalf("Totals = %+v, want a declared count of 12000", r.Totals)
	}
	if r.Accepted {
		t.Error("a declared count of 12000 against zero documents was accepted")
	}
}

func TestSeverity(t *testing.T) {
	for _, s := range []Severity{SeverityFatal, SeverityReject, SeverityWarn} {
		if !s.Valid() {
			t.Errorf("%s is not Valid", s)
		}
	}
	if Severity("info").Valid() {
		t.Error("an unknown severity reported Valid")
	}
	if !SeverityFatal.Blocking() || !SeverityReject.Blocking() || SeverityWarn.Blocking() {
		t.Error("Blocking disagrees with the acceptance rule")
	}
}

func TestBuilderHasFatalAndHasBlocking(t *testing.T) {
	b := NewBuilder("test", Options{})
	if b.HasFatal() || b.HasBlocking() {
		t.Fatal("a fresh builder reports a finding")
	}
	b.Add(Diagnostic{Code: "x", Severity: SeverityWarn, Message: "m"})
	if b.HasFatal() || b.HasBlocking() {
		t.Error("a warning is neither fatal nor blocking")
	}
	b.Add(Diagnostic{Code: "y", Severity: SeverityReject, Message: "m"})
	if b.HasFatal() {
		t.Error("a reject is not fatal")
	}
	if !b.HasBlocking() {
		t.Error("a reject is blocking")
	}
	b.Add(Diagnostic{Code: "z", Severity: SeverityFatal, Message: "m"})
	if !b.HasFatal() || !b.HasBlocking() {
		t.Error("a fatal finding is both")
	}
}

func TestOptionsMaxDiagnostics(t *testing.T) {
	// The cap is part of the published contract, so production always uses the
	// default; the option exists so a test does not have to build a file with a
	// thousand broken lines to exercise truncation.
	b := NewBuilder("test", Options{MaxDiagnostics: 3})
	for i := 0; i < 10; i++ {
		b.Add(Diagnostic{Code: "reject_me", Severity: SeverityReject, Line: i + 1, Message: "m"})
	}
	r := b.Result()
	if len(r.Diagnostics) != 4 {
		t.Fatalf("kept %d diagnostics, want 3 plus one truncation warning", len(r.Diagnostics))
	}
	if r.Diagnostics[3].Code != CodeDiagnosticsTruncated {
		t.Errorf("last diagnostic = %s, want %s", r.Diagnostics[3].Code, CodeDiagnosticsTruncated)
	}
	if !strings.Contains(r.Diagnostics[3].Message, "7") {
		t.Errorf("message = %q, want it to name the 7 dropped findings", r.Diagnostics[3].Message)
	}
}
