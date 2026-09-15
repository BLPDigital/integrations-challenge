package importer_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
)

// lineFormat is a Format whose only rule is "one supplier key per line". It
// exists so the golden runner can be tested against a parser whose behavior
// this file defines: a line of digits is a document, anything else is a rejected
// record, and no line is ever a fatal defect.
type lineFormat struct{}

func (lineFormat) ID() string { return "test-line-v1" }

func (lineFormat) Detect(head []byte, opt importer.Options) bool {
	line := head
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		line = head[:i]
	}
	return len(line) > 0 && allDigits(string(line))
}

func (f lineFormat) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	return f.ParseStream(ctx, bytes.NewReader(raw), opt)
}

func (f lineFormat) ParseStream(ctx context.Context, r io.Reader, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	b := importer.NewBuilder(f.ID(), opt)
	b.SetSourceBytes(body)
	n := 0
	for i, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		n++
		if !allDigits(line) {
			b.Add(importer.Diagnostic{
				Code: importer.CodeKeyNotString, Severity: importer.SeverityReject,
				Line: i + 1, Record: n, Field: "key", Value: line,
				Message: "a supplier key is digits",
			})
			continue
		}
		b.AddDocument(importer.Document{
			Dataset: "supplier", Key: line, Line: i + 1,
			Payload: []byte(`{"key":"` + line + `"}`),
		})
	}
	t := b.Totals()
	t.ComputedDocuments = b.Documents()
	b.SetTotals(t)
	b.SetLines(n)
	return b.Result(), nil
}

// erroringFormat returns an error for a data defect, which is exactly the
// contract violation the golden runner has to catch.
type erroringFormat struct{}

func (erroringFormat) ID() string                                    { return "test-erroring-v1" }
func (erroringFormat) Detect(head []byte, opt importer.Options) bool { return false }
func (erroringFormat) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	return nil, errors.New("that line looked wrong to me")
}

// driftingFormat parses the same bytes differently through its streaming path,
// which no expectation file could catch, because a fixture is only ever run one
// way.
type driftingFormat struct{ lineFormat }

func (f driftingFormat) ID() string { return "test-drifting-v1" }

func (f driftingFormat) ParseStream(ctx context.Context, r io.Reader, opt importer.Options) (*importer.Result, error) {
	res, err := f.lineFormat.ParseStream(ctx, r, opt)
	if err != nil {
		return nil, err
	}
	res.Documents = nil
	return res, nil
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
