package seed

import (
	"fmt"
	"sort"
)

// Scenario identifiers this generator knows.
const (
	// ScenarioS0 is the public smoke gate: a tiny dataset, every trap present at
	// least once, small enough to eyeball.
	ScenarioS0 = "S0"
	// ScenarioS1 is the cold load baseline.
	ScenarioS1 = "S1"
	// ScenarioS2 is the delta on top of S1: the same base data plus changed
	// master records, new exchange rates and a second legacy delivery that
	// re-sends some invoices unchanged and some amended.
	ScenarioS2 = "S2"
	// ScenarioX2 is the stretch scale scenario: S1 multiplied.
	ScenarioX2 = "X2"
)

// X2Multiplier is the factor by which ScenarioX2 multiplies the S1 master data
// and invoice volumes. The exchange rate table is not multiplied: a rate table
// does not grow with the number of invoices, and the row count is a published
// property of the SOAP channel.
const X2Multiplier = 3

// Spec is the size and shape contract of one scenario. It is a value, not a
// setting: the numbers are the published dataset sizes, and a test asserts the
// generated dataset matches them exactly.
type Spec struct {
	// ID is the scenario identifier.
	ID string
	// Description is a one-line human summary.
	Description string

	// Suppliers, CostCenters, PurchaseOrders and PurchaseOrderLines are the
	// master data record counts.
	Suppliers          int
	CostCenters        int
	PurchaseOrders     int
	PurchaseOrderLines int

	// FxRows is the exact number of rows the SOAP table serves, and FxStepDays
	// the length in days of one daily-rate interval. S0 uses weekly intervals so
	// the same coverage fits in a table small enough to read; the scored
	// scenarios use one row per day, which is what a real rate table looks like.
	FxRows     int
	FxStepDays int

	// Invoices and InvoiceLines are the counts of the base legacy delivery.
	Invoices     int
	InvoiceLines int

	// ExceptionPerMille is the share of invoices constructed to land in the
	// exception queue, in per mille so no float64 enters the generator.
	ExceptionPerMille int

	// ExportDate is the YYYY-MM-DD date in the legacy file names, and
	// InvoiceDateFrom / InvoiceDateTo bound the invoice document dates. All
	// three are constants of the scenario: nothing here reads a clock.
	ExportDate      string
	InvoiceDateFrom string
	InvoiceDateTo   string

	// DeltaOf names the base scenario a delta scenario builds on, empty for a
	// cold load. The base is generated first with the same seed and the same
	// code path, so the base delivery of a delta scenario is byte-identical to
	// the base scenario's own delivery.
	DeltaOf string
	// ChangedSuppliers, ChangedPurchaseOrders and ChangedPurchaseOrderLines are
	// the master records the delta re-issues with a higher change sequence.
	ChangedSuppliers          int
	ChangedPurchaseOrders     int
	ChangedPurchaseOrderLines int
	// NewFxRows is the number of rows the delta appends to the rate table.
	NewFxRows int
	// DeltaInvoices and DeltaInvoiceLines size the new invoices of the second
	// delivery; ResentInvoices are re-sent byte-identically and must dedupe by
	// content; AmendedInvoices are re-sent with a changed gross amount and must
	// each raise EXC_DUPLICATE_INVOICE_AMENDED.
	DeltaInvoices     int
	DeltaInvoiceLines int
	ResentInvoices    int
	AmendedInvoices   int
}

// specs is the scenario table, probed by key only.
var specs = map[string]Spec{
	ScenarioS0: {
		ID:                 ScenarioS0,
		Description:        "public smoke gate: tiny dataset, every trap present once",
		Suppliers:          240,
		CostCenters:        60,
		PurchaseOrders:     160,
		PurchaseOrderLines: 520,
		FxRows:             120,
		FxStepDays:         7,
		Invoices:           100,
		InvoiceLines:       300,
		ExceptionPerMille:  140,
		ExportDate:         "2026-04-16",
		InvoiceDateFrom:    "2026-02-02",
		InvoiceDateTo:      "2026-04-15",
	},
	ScenarioS1: {
		ID:                 ScenarioS1,
		Description:        "cold load baseline: full master pull and one legacy batch",
		Suppliers:          12000,
		CostCenters:        1400,
		PurchaseOrders:     8000,
		PurchaseOrderLines: 26400,
		FxRows:             600,
		FxStepDays:         1,
		Invoices:           5000,
		InvoiceLines:       14800,
		ExceptionPerMille:  140,
		ExportDate:         "2026-04-16",
		InvoiceDateFrom:    "2026-02-02",
		InvoiceDateTo:      "2026-04-15",
	},
	ScenarioS2: {
		ID:                 ScenarioS2,
		Description:        "delta on top of S1: changed master data and a second delivery",
		Suppliers:          12000,
		CostCenters:        1400,
		PurchaseOrders:     8000,
		PurchaseOrderLines: 26400,
		FxRows:             600,
		FxStepDays:         1,
		Invoices:           5000,
		InvoiceLines:       14800,
		ExceptionPerMille:  140,
		ExportDate:         "2026-04-16",
		InvoiceDateFrom:    "2026-02-02",
		InvoiceDateTo:      "2026-04-15",

		DeltaOf:                   ScenarioS1,
		ChangedSuppliers:          900,
		ChangedPurchaseOrders:     400,
		ChangedPurchaseOrderLines: 1300,
		NewFxRows:                 60,
		DeltaInvoices:             1100,
		DeltaInvoiceLines:         3300,
		ResentInvoices:            40,
		AmendedInvoices:           12,
	},
	ScenarioX2: {
		ID:                 ScenarioX2,
		Description:        "stretch scale: S1 multiplied, rate table unchanged",
		Suppliers:          12000 * X2Multiplier,
		CostCenters:        1400 * X2Multiplier,
		PurchaseOrders:     8000 * X2Multiplier,
		PurchaseOrderLines: 26400 * X2Multiplier,
		FxRows:             600,
		FxStepDays:         1,
		Invoices:           5000 * X2Multiplier,
		InvoiceLines:       14800 * X2Multiplier,
		ExceptionPerMille:  140,
		ExportDate:         "2026-04-16",
		InvoiceDateFrom:    "2026-02-02",
		InvoiceDateTo:      "2026-04-15",
	},
}

// SpecOf returns the size contract of a scenario. An unknown scenario is an
// error, never a silently substituted default.
func SpecOf(scenario string) (Spec, error) {
	s, ok := specs[scenario]
	if !ok {
		return Spec{}, fmt.Errorf("seed: unknown scenario %q, want one of %v", scenario, Scenarios())
	}
	return s, nil
}

// Scenarios returns every known scenario identifier in ascending byte order.
func Scenarios() []string {
	out := make([]string, 0, len(specs))
	for id := range specs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
