// Command seed writes a generated dataset to disk for inspection.
//
// It is the human-facing side of package seed: the same generator the grader
// calls in process, dumped as files you can read, diff and grep. The master data
// goes out as JSON, the legacy delivery as the raw CP1252 bytes plus its empty
// .ok sentinel, and the golden expected-exception set as its own file.
//
// Usage:
//
//	seed --scenario S1 --seed 20260416 --out var/seed/S1
//
// Everything is deterministic: two runs with the same flags produce
// byte-identical files, including the legacy delivery. Nothing here reads a
// clock, so a dump can be committed as a fixture and diffed later.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

// run parses the flags, generates the dataset and writes it out. It returns an
// error rather than exiting so it is testable.
func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scenario := fs.String("scenario", seed.ScenarioS0,
		"scenario to generate: "+strings.Join(seed.Scenarios(), ", "))
	seedValue := fs.Int64("seed", 0, "scenario seed; the same seed always yields the same bytes")
	out := fs.String("out", "", "output directory (created if absent); required")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: seed --scenario ID --seed N --out DIR\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("--out is required")
	}

	d, err := seed.Generate(*scenario, *seedValue)
	if err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return fmt.Errorf("the generated dataset is inconsistent: %w", err)
	}
	if err := write(d, *out); err != nil {
		return err
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "%s\n", summaryLine(d, *out))
	}
	return nil
}

// summaryLine is the one line the command prints: a scenario, a seed and the
// counts, so a `make seed` log says what it produced.
func summaryLine(d *seed.Dataset, out string) string {
	c := d.Counts()
	return fmt.Sprintf(
		"scenario=%s seed=%d out=%s suppliers=%d cost_centers=%d purchase_orders=%d "+
			"purchase_order_lines=%d fx_rows=%d invoices=%d invoice_lines=%d deliveries=%d "+
			"expected_exceptions=%d max_change_seq=%d",
		d.Scenario, d.Seed, out, c["suppliers"], c["cost_centers"], c["purchase_orders"],
		c["purchase_order_lines"], c["fx_rows"], c["invoices"], c["invoice_lines"],
		c["deliveries"], c["expected_exceptions"], d.MaxChangeSeq)
}

// summary is the index of a dump: what was generated, how big it is and which
// deliveries it contains. It deliberately does not repeat the records, which have
// their own files.
type summary struct {
	Scenario           string            `json:"scenario"`
	Seed               int64             `json:"seed"`
	Counts             map[string]int    `json:"counts"`
	MaxChangeSeq       int64             `json:"max_change_seq"`
	DeltaFromChangeSeq int64             `json:"delta_from_change_seq"`
	ClosedPeriodBefore string            `json:"closed_period_before"`
	LegacyFileName     string            `json:"legacy_file_name"`
	OKFileName         string            `json:"ok_file_name"`
	Deliveries         []deliverySummary `json:"deliveries"`
	FxGaps             []seed.FxGap      `json:"fx_gaps"`
}

// deliverySummary is one legacy file drop in the index.
type deliverySummary struct {
	Name        string `json:"name"`
	OKName      string `json:"ok_name"`
	Mandant     string `json:"mandant"`
	CompanyCode string `json:"company_code"`
	RunNumber   int    `json:"run_number"`
	ExportDate  string `json:"export_date"`
	SizeBytes   int    `json:"size_bytes"`
	KopfCount   int    `json:"kopf_count"`
	PosCount    int    `json:"pos_count"`
	SumGross    string `json:"sum_gross"`
	SumLines    string `json:"sum_lines"`
}

// newSummary builds the index of a dataset.
func newSummary(d *seed.Dataset) summary {
	s := summary{
		Scenario:           d.Scenario,
		Seed:               d.Seed,
		Counts:             d.Counts(),
		MaxChangeSeq:       d.MaxChangeSeq,
		DeltaFromChangeSeq: d.DeltaFromChangeSeq,
		ClosedPeriodBefore: d.ClosedPeriodBefore,
		LegacyFileName:     d.LegacyFileName,
		OKFileName:         d.OKFileName,
		FxGaps:             d.FxGaps,
	}
	for _, del := range d.Deliveries {
		s.Deliveries = append(s.Deliveries, deliverySummary{
			Name:        del.Name,
			OKName:      del.OKName,
			Mandant:     del.Mandant,
			CompanyCode: del.CompanyCode,
			RunNumber:   del.RunNumber,
			ExportDate:  del.ExportDate,
			SizeBytes:   len(del.Bytes),
			KopfCount:   del.KopfCount,
			PosCount:    del.PosCount,
			SumGross:    del.SumGross.String(),
			SumLines:    del.SumLines.String(),
		})
	}
	return s
}

// legacyDir is the subdirectory the delivery bytes and their sentinels go into.
// It mirrors the ERP's export drop, so the dump can be copied straight into it.
const legacyDir = "legacy"

// write writes the whole dataset. The file list is explicit and ordered, so two
// runs write the same files in the same order.
func write(d *seed.Dataset, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, legacyDir), 0o755); err != nil {
		return err
	}
	files := []struct {
		name  string
		value any
	}{
		{"suppliers.json", d.Suppliers},
		{"cost_centers.json", d.CostCenters},
		{"purchase_orders.json", d.PurchaseOrders},
		{"purchase_order_lines.json", d.PurchaseOrderLines},
		{"fx_rows.json", d.FxRows},
		{"fx_gaps.json", d.FxGaps},
		{"uom_conversions.json", d.UoMConversions},
		{"change_seqs.json", d.ChangeSeqs},
		{"invoices.json", d.Invoices},
		{"amended_invoices.json", d.AmendedInvoices},
		{"expected_exceptions.json", d.ExpectedExceptions},
	}
	for _, f := range files {
		body, err := jsonArray(f.value)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if err := writeFile(filepath.Join(dir, f.name), body); err != nil {
			return err
		}
	}
	summary, err := model.CanonicalJSON(newSummary(d))
	if err != nil {
		return fmt.Errorf("summary.json: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "summary.json"), append(summary, '\n')); err != nil {
		return err
	}
	for _, del := range d.Deliveries {
		if err := writeFile(filepath.Join(dir, legacyDir, del.Name), del.Bytes); err != nil {
			return err
		}
		// The sentinel is empty and is written LAST, because a data file without
		// its sibling must not be read.
		if err := writeFile(filepath.Join(dir, legacyDir, del.OKName), nil); err != nil {
			return err
		}
	}
	return nil
}

// jsonArray renders a slice as a JSON array with one canonical element per line.
// The result is valid JSON and byte-stable, and it is readable in a diff, which
// a single canonical line of twelve thousand records is not.
func jsonArray(v any) ([]byte, error) {
	elems, err := canonicalElements(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("[\n")
	for i, e := range elems {
		buf.WriteString("  ")
		buf.Write(e)
		if i+1 < len(elems) {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]\n")
	return buf.Bytes(), nil
}

// canonicalElements returns the canonical JSON of every element of a slice.
func canonicalElements(v any) ([][]byte, error) {
	switch xs := v.(type) {
	case []seed.Supplier:
		return each(xs)
	case []model.CostCenter:
		return each(xs)
	case []model.PurchaseOrder:
		return each(xs)
	case []model.PurchaseOrderLine:
		return each(xs)
	case []seed.FxRow:
		return each(xs)
	case []seed.FxGap:
		return each(xs)
	case []seed.UoMConversion:
		return each(xs)
	case []seed.ChangeSeqEntry:
		return each(xs)
	case []model.APInvoice:
		return each(xs)
	case []seed.ExpectedException:
		return each(xs)
	}
	return nil, fmt.Errorf("unsupported collection type %T", v)
}

// each renders every element of a typed slice as canonical JSON.
func each[T any](xs []T) ([][]byte, error) {
	out := make([][]byte, 0, len(xs))
	for i := range xs {
		b, err := model.CanonicalJSON(xs[i])
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// writeFile writes a file with fixed permissions, truncating an existing one.
func writeFile(path string, body []byte) error {
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
