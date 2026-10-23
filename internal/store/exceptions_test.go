package store

import (
	"errors"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func exceptionFixture(subject, code string) Exception {
	return Exception{
		SubjectKey:          subject,
		SubjectType:         "invoice",
		Stage:               StageMatch,
		Code:                code,
		Field:               "currency",
		Message:             "no DAILY rate covers the posting date",
		SourceBatchID:       "batch-a",
		SourceFileOrChunk:   "KRED_0100_20260329_001.txt",
		SourceLineOrOrdinal: "1042",
		Details:             map[string]string{"currency": "DKK", "posting_date": "2026-03-29"},
	}
}

func TestExceptionKeyRoundTrip(t *testing.T) {
	cases := []struct {
		subject string
		code    string
	}{
		{model.InvoiceKey("0000417", "0004711"), "EXC_FX_RATE_MISSING"},
		{"batch-a", "MANIFEST_MISSING"},
		{model.POLineKey("PO-004417", "00010"), "EXC_UOM_UNMAPPABLE"},
	}
	for _, c := range cases {
		key := ExceptionKey(c.subject, c.code)
		subject, code := SplitExceptionKey(key)
		if subject != c.subject || code != c.code {
			t.Errorf("round trip of (%q,%q) gave (%q,%q)", c.subject, c.code, subject, code)
		}
	}
	if subject, code := SplitExceptionKey("nosep"); subject != "nosep" || code != "" {
		t.Errorf("SplitExceptionKey(%q) = (%q,%q)", "nosep", subject, code)
	}
}

func TestRaiseExceptionIsIdempotentByContent(t *testing.T) {
	invoice := model.InvoiceKey("0000417", "0004711")
	e := exceptionFixture(invoice, "EXC_FX_RATE_MISSING")
	s, _, _ := openTemp(t)
	first, err := s.RaiseException(e, prov("batch-a", "run_1", "f", 1042))
	if err != nil {
		t.Fatal(err)
	}
	if first.Result != ResultApplied || first.Version != 1 {
		t.Fatalf("outcome = %+v", first)
	}
	second, err := s.RaiseException(e, prov("batch-b", "run_2", "f", 77))
	if err != nil {
		t.Fatal(err)
	}
	if second.Result != ResultUnchanged || second.Version != 1 {
		t.Errorf("re-raise = %+v, want unchanged at version 1", second)
	}
	n, err := s.CountExceptions(ExceptionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("open exceptions = %d, want 1: the queue must not grow on a re-raise", n)
	}
	revs, err := s.History(DatasetException, e.Key())
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || !revs[1].ProvenanceOnly {
		t.Errorf("history = %d revisions, want 2 with the second provenance-only", len(revs))
	}
	if revs[1].Provenance.RunID != "run_2" {
		t.Errorf("re-raise run id = %q, want run_2", revs[1].Provenance.RunID)
	}
}

func TestResolveAndReopenException(t *testing.T) {
	invoice := model.InvoiceKey("0000417", "0004711")
	e := exceptionFixture(invoice, "EXC_FX_RATE_MISSING")
	s, _, _ := openTemp(t)
	if _, err := s.RaiseException(e, prov("batch-a", "run_1", "f", 1)); err != nil {
		t.Fatal(err)
	}
	out, err := s.ResolveException(invoice, "EXC_FX_RATE_MISSING", prov("batch-b", "run_2", "f", 1))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultApplied || out.Version != 2 {
		t.Fatalf("resolve outcome = %+v", out)
	}
	open, err := s.CountExceptions(ExceptionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("open = %d, want 0", open)
	}
	all, err := s.CountExceptions("")
	if err != nil {
		t.Fatal(err)
	}
	if all != 1 {
		t.Errorf("all = %d, want 1: a resolved exception stays in the queue", all)
	}
	again, err := s.ResolveException(invoice, "EXC_FX_RATE_MISSING", prov("batch-c", "run_3", "f", 1))
	if err != nil {
		t.Fatal(err)
	}
	if again.Result != ResultUnchanged {
		t.Errorf("second resolve = %q, want unchanged", again.Result)
	}
	// Raising the same content again reopens it: state is part of the content.
	if _, err := s.RaiseException(e, prov("batch-d", "run_4", "f", 1)); err != nil {
		t.Fatal(err)
	}
	open, err = s.CountExceptions(ExceptionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Errorf("open after re-raise = %d, want 1", open)
	}
	if _, err := s.ResolveException("nosuch", "EXC_X", Provenance{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolve unknown: %v, want ErrNotFound", err)
	}
}

func TestRaiseExceptionRejects(t *testing.T) {
	s, _, _ := openTemp(t)
	cases := []struct {
		name string
		e    Exception
	}{
		{"no subject", Exception{Code: "EXC_X"}},
		{"no code", Exception{SubjectKey: "k"}},
	}
	for _, c := range cases {
		if _, err := s.RaiseException(c.e, Provenance{}); !errors.Is(err, ErrKeyEmpty) {
			t.Errorf("%s: err = %v, want ErrKeyEmpty", c.name, err)
		}
	}
}

func TestExceptionsOrderAndFilter(t *testing.T) {
	s, dir, _ := openTemp(t)
	entries := []struct {
		subject string
		code    string
	}{
		{model.InvoiceKey("0000418", "0004812"), "EXC_FX_RATE_MISSING"},
		{model.InvoiceKey("0000417", "0004711"), "EXC_TOTALS_MISMATCH"},
		{model.InvoiceKey("0000417", "0004711"), "EXC_COST_CENTER_UNKNOWN"},
	}
	for _, en := range entries {
		if _, err := s.RaiseException(exceptionFixture(en.subject, en.code), prov("b", "r", "f", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ResolveException(entries[1].subject, entries[1].code, prov("b", "r", "f", 1)); err != nil {
		t.Fatal(err)
	}
	open, err := s.Exceptions(ExceptionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("open = %d, want 2", len(open))
	}
	// Ascending key order: subject key first, code last.
	if open[0].Code != "EXC_COST_CENTER_UNKNOWN" || open[0].SubjectKey != entries[2].subject {
		t.Errorf("first open = %+v", open[0])
	}
	if open[1].Code != "EXC_FX_RATE_MISSING" || open[1].SubjectKey != entries[0].subject {
		t.Errorf("second open = %+v", open[1])
	}
	if open[0].Details["currency"] != "DKK" {
		t.Errorf("details lost: %v", open[0].Details)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, _ := reopen(t, dir)
	after, err := s2.Exceptions(ExceptionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].Key() != open[0].Key() {
		t.Errorf("exception queue did not survive a reopen: %+v", after)
	}
	n := 0
	if err := s2.ScanExceptions("", func(Exception) bool { n++; return false }); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("early stop visited %d exceptions, want 1", n)
	}
}
