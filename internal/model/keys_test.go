package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestNaturalKeys(t *testing.T) {
	sep := KeySeparator
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"supplier", SupplierKey("0000417"), "0000417"},
		{"supplier trimmed", SupplierKey("  0000417 "), "0000417"},
		{"cost center", CostCenterKey("CC-2200"), "CC-2200"},
		{"po", POKey("PO-004417"), "PO-004417"},
		{"po line", POLineKey("PO-004417", "00010"), "PO-004417" + sep + "00010"},
		{"po line trimmed", POLineKey(" PO-004417 ", " 00010 "), "PO-004417" + sep + "00010"},
		{"fx rate", FxRateKey("EUR", "CHF", "2026-03-29", "DAILY"),
			"EUR" + sep + "CHF" + sep + "2026-03-29" + sep + "DAILY"},
		{"invoice", InvoiceKey("0000417", "0004711"), "0000417" + sep + "0004711"},
		{"invoice line", InvoiceLineKey("0000417", "0004711", "00010"),
			"0000417" + sep + "0004711" + sep + "00010"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("key = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestKeysLeadingZerosAreSignificant(t *testing.T) {
	if SupplierKey("0000417") == SupplierKey("417") {
		t.Fatal("leading zeros were stripped")
	}
	if POLineKey("PO-1", "00010") == POLineKey("PO-1", "10") {
		t.Fatal("leading zeros were stripped from the line number")
	}
	if SupplierKey("abc") == SupplierKey("ABC") {
		t.Fatal("keys are case-insensitive")
	}
}

func TestKeysAreUnambiguous(t *testing.T) {
	// Without a separator these two would collide: "PO-1" + "000100010" versus
	// "PO-10" + "00100010". The unit separator keeps them apart.
	a := POLineKey("PO-1", "000100010")
	b := POLineKey("PO-10", "00100010")
	if a == b {
		t.Fatalf("composite keys collide: %q", a)
	}
	if strings.Count(a, KeySeparator) != 1 {
		t.Fatalf("key %q does not carry exactly one separator", a)
	}
	if got := SplitKey(a); !reflect.DeepEqual(got, []string{"PO-1", "000100010"}) {
		t.Fatalf("SplitKey = %q", got)
	}
	if got := SplitKey(SupplierKey("0000417")); !reflect.DeepEqual(got, []string{"0000417"}) {
		t.Fatalf("SplitKey of a single component = %q", got)
	}
	if got := SplitKey(InvoiceLineKey("a", "b", "c")); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("SplitKey = %q", got)
	}
}

func TestEntityKeyMethods(t *testing.T) {
	sep := KeySeparator
	if got := (Supplier{SupplierNumber: "0000417"}).Key(); got != "0000417" {
		t.Fatalf("Supplier.Key = %q", got)
	}
	if got := (CostCenter{Code: "CC-2200"}).Key(); got != "CC-2200" {
		t.Fatalf("CostCenter.Key = %q", got)
	}
	if got := (PurchaseOrder{PONumber: "PO-1"}).Key(); got != "PO-1" {
		t.Fatalf("PurchaseOrder.Key = %q", got)
	}
	if got := (PurchaseOrderLine{PONumber: "PO-1", LineNo: "00010"}).Key(); got != "PO-1"+sep+"00010" {
		t.Fatalf("PurchaseOrderLine.Key = %q", got)
	}
	fx := FxRate{Base: "EUR", Quote: "CHF", ValidFrom: "2026-03-29", RateType: RateTypeDaily, Sequence: 7}
	if got := fx.Key(); got != "EUR"+sep+"CHF"+sep+"2026-03-29"+sep+"DAILY" {
		t.Fatalf("FxRate.Key = %q", got)
	}
	// The sequence is not part of the key: two sequences share it and supersede.
	other := fx
	other.Sequence = 9
	if fx.Key() != other.Key() {
		t.Fatal("FxRate.Key includes the sequence")
	}
	inv := APInvoice{SupplierNumber: "0000417", SupplierInvoiceNumber: "0004711"}
	if got := inv.Key(); got != "0000417"+sep+"0004711" {
		t.Fatalf("APInvoice.Key = %q", got)
	}
	line := APInvoiceLine{LineNo: "00010"}
	if got := line.Key(inv.SupplierNumber, inv.SupplierInvoiceNumber); got != "0000417"+sep+"0004711"+sep+"00010" {
		t.Fatalf("APInvoiceLine.Key = %q", got)
	}
}

func TestDatasets(t *testing.T) {
	want := []Dataset{
		DatasetCostCenter, DatasetFxRate, DatasetInvoice, DatasetInvoiceLine,
		DatasetOutboxAck, DatasetPurchaseOrder, DatasetPurchaseOrderLine, DatasetSupplier,
	}
	for i := 0; i < 10; i++ { // repeated: order must not depend on map iteration
		if got := Datasets(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Datasets() = %v, want %v", got, want)
		}
	}
	names := []string{"supplier", "cost_center", "purchase_order", "purchase_order_line",
		"fx_rate", "invoice", "invoice_line", "outbox_ack"}
	for _, n := range names {
		if !IsKnownDataset(n) || !Dataset(n).Valid() {
			t.Fatalf("dataset %q is not known", n)
		}
	}
	for _, n := range []string{"", "Supplier", "suppliers", "purchaseorder"} {
		if IsKnownDataset(n) {
			t.Fatalf("dataset %q must not be known", n)
		}
	}
	if got := DatasetPurchaseOrderLine.String(); got != "purchase_order_line" {
		t.Fatalf("String = %q", got)
	}
}

func TestFxRateCovers(t *testing.T) {
	fx := FxRate{Base: "EUR", Quote: "CHF", ValidFrom: "2026-03-01", ValidTo: "2026-04-01",
		RateType: RateTypeDaily, Rate: MustDecimal("0.960000"), RateFactor: 1, Status: FxStatusActive}
	for _, tc := range []struct {
		date string
		want bool
	}{
		{"2026-02-28", false},
		{"2026-03-01", true},
		{"2026-03-29", true},
		{"2026-03-31", true},
		{"2026-04-01", false},
	} {
		got, err := fx.Covers(tc.date)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("Covers(%s) = %v, want %v", tc.date, got, tc.want)
		}
	}
	open := fx
	open.ValidTo = ""
	if got, err := open.Covers("2099-01-01"); err != nil || !got {
		t.Fatalf("open-ended Covers = %v, %v", got, err)
	}
}

func TestInvoiceLineTotal(t *testing.T) {
	inv := APInvoice{
		Lines: []APInvoiceLine{
			{LineNo: "00010", LineAmount: MustDecimal("100.00")},
			{LineNo: "00020", LineAmount: MustDecimal("23.45")},
			{LineNo: "00030", LineAmount: MustDecimal("-3.45")},
		},
	}
	sum, err := inv.LineTotal()
	if err != nil {
		t.Fatal(err)
	}
	if sum.String() != "120.00" {
		t.Fatalf("LineTotal = %q, want 120.00", sum.String())
	}
	// No lines: an exact zero, not an error.
	empty, err := (APInvoice{}).LineTotal()
	if err != nil {
		t.Fatal(err)
	}
	if !empty.IsZero() {
		t.Fatalf("LineTotal of an empty invoice = %q", empty.String())
	}
	// The scale of the first line is preserved, so the check against the header
	// total stays exact.
	scaled := APInvoice{Lines: []APInvoiceLine{{LineAmount: MustDecimal("1.0000")}}}
	sum, err = scaled.LineTotal()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scale() != 4 {
		t.Fatalf("LineTotal scale = %d, want 4", sum.Scale())
	}
}
