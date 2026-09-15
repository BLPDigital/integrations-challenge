package model

import (
	"strings"
	"testing"
)

func TestValidateSupplier(t *testing.T) {
	valid := Supplier{SupplierNumber: "0000417", Name: "ACME AG", Country: "CH",
		Currency: "CHF", PaymentTermsDays: 30}
	tests := []struct {
		name   string
		mutate func(*Supplier)
		codes  []string
	}{
		{"valid", func(*Supplier) {}, nil},
		{"country optional", func(s *Supplier) { s.Country = "" }, nil},
		{"missing key", func(s *Supplier) { s.SupplierNumber = "" }, []string{CodeKeyMissing}},
		{"whitespace key", func(s *Supplier) { s.SupplierNumber = "  " }, []string{CodeKeyMissing}},
		{"missing name", func(s *Supplier) { s.Name = "" }, []string{CodeFieldRequired}},
		{"missing currency", func(s *Supplier) { s.Currency = "" }, []string{CodeFieldRequired}},
		{"unknown currency", func(s *Supplier) { s.Currency = "XXX" }, []string{CodeCurrencyUnknown}},
		{"lowercase currency", func(s *Supplier) { s.Currency = "chf" }, []string{CodeCurrencyUnknown}},
		{"bad country", func(s *Supplier) { s.Country = "che" }, []string{CodeCountryFormat}},
		{"lowercase country", func(s *Supplier) { s.Country = "ch" }, []string{CodeCountryFormat}},
		{"negative terms", func(s *Supplier) { s.PaymentTermsDays = -1 }, []string{CodeFieldInvalid}},
		{"several findings", func(s *Supplier) { s.SupplierNumber = ""; s.Name = ""; s.Currency = "XXX" },
			[]string{CodeKeyMissing, CodeFieldRequired, CodeCurrencyUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.mutate(&s)
			assertCodes(t, s.Validate(), tc.codes)
		})
	}
}

func TestValidateCostCenter(t *testing.T) {
	valid := CostCenter{Code: "CC-2200", Name: "Facility", CompanyCode: CompanyCodeCH10,
		ValidFrom: "2026-01-01", ValidTo: "2027-01-01"}
	tests := []struct {
		name   string
		mutate func(*CostCenter)
		codes  []string
	}{
		{"valid", func(*CostCenter) {}, nil},
		{"open ended", func(c *CostCenter) { c.ValidTo = "" }, nil},
		{"missing code", func(c *CostCenter) { c.Code = "" }, []string{CodeKeyMissing}},
		{"missing name", func(c *CostCenter) { c.Name = "" }, []string{CodeFieldRequired}},
		{"unknown company code", func(c *CostCenter) { c.CompanyCode = "DE10" }, []string{CodeEnumUnknown}},
		{"missing company code", func(c *CostCenter) { c.CompanyCode = "" }, []string{CodeFieldRequired}},
		{"missing valid from", func(c *CostCenter) { c.ValidFrom = "" }, []string{CodeFieldRequired}},
		{"bad valid from", func(c *CostCenter) { c.ValidFrom = "01.01.2026" }, []string{CodeDateFormat}},
		{"bad valid to", func(c *CostCenter) { c.ValidTo = "2027-02-30" }, []string{CodeDateFormat}},
		{"empty range", func(c *CostCenter) { c.ValidTo = c.ValidFrom }, []string{CodeFieldInvalid}},
		{"inverted range", func(c *CostCenter) { c.ValidTo = "2025-01-01" }, []string{CodeFieldInvalid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mutate(&c)
			assertCodes(t, c.Validate(), tc.codes)
		})
	}
}

func TestValidatePurchaseOrderAndLine(t *testing.T) {
	po := PurchaseOrder{PONumber: "PO-004417", SupplierNumber: "0000417",
		CompanyCode: CompanyCodeCH20, Currency: "EUR", Status: POStatusOpen, OrderDate: "2026-02-01"}
	assertCodes(t, po.Validate(), nil)

	bad := po
	bad.PONumber = ""
	bad.Status = "OFFEN"
	bad.OrderDate = "2026-02-31"
	assertCodes(t, bad.Validate(), []string{CodeKeyMissing, CodeEnumUnknown, CodeDateFormat})

	line := PurchaseOrderLine{PONumber: "PO-004417", LineNo: "00010", Material: "M-1",
		Quantity: MustDecimal("2"), UoM: "EA", UnitPrice: MustDecimal("10.50"), Currency: "EUR"}
	assertCodes(t, line.Validate(), nil)

	tests := []struct {
		name   string
		mutate func(*PurchaseOrderLine)
		codes  []string
	}{
		{"missing line no", func(l *PurchaseOrderLine) { l.LineNo = "" }, []string{CodeKeyMissing}},
		{"unknown uom", func(l *PurchaseOrderLine) { l.UoM = "ZZZ" }, []string{CodeUoMUnknown}},
		{"missing uom", func(l *PurchaseOrderLine) { l.UoM = "" }, []string{CodeFieldRequired}},
		// A unit price is a rate, not a booked amount, so more fraction digits
		// than the currency's minor unit is normal: the customer's own KRED-EXP
		// layout specifies two to four for Einzelpreis and ERPs go to six.
		{"a four decimal price is a normal ERP price", func(l *PurchaseOrderLine) {
			l.UnitPrice = MustDecimal("10.5052")
		}, nil},
		{"a three decimal price is fine too", func(l *PurchaseOrderLine) {
			l.UnitPrice = MustDecimal("10.505")
		}, nil},
		{"price beyond six fraction digits", func(l *PurchaseOrderLine) {
			l.UnitPrice = MustDecimal("10.5051234")
		}, []string{CodeMoneyScale}},
		{"trailing zeros are fine", func(l *PurchaseOrderLine) { l.UnitPrice = MustDecimal("10.5000") }, nil},
		{"unknown currency is reported once, and the price scale check is independent of it",
			func(l *PurchaseOrderLine) {
				l.Currency = "XXX"
				l.UnitPrice = MustDecimal("10.505")
			}, []string{CodeCurrencyUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := line
			tc.mutate(&l)
			assertCodes(t, l.Validate(), tc.codes)
		})
	}
}

func TestValidateFxRate(t *testing.T) {
	valid := FxRate{Base: "EUR", Quote: "CHF", ValidFrom: "2026-03-29", RateType: RateTypeDaily,
		Rate: MustDecimal("0.960000"), RateFactor: 1, Sequence: 1, Status: FxStatusActive}
	tests := []struct {
		name   string
		mutate func(*FxRate)
		codes  []string
	}{
		{"valid", func(*FxRate) {}, nil},
		{"jpy per hundred", func(f *FxRate) { f.Base = "JPY"; f.RateFactor = 100 }, nil},
		{"monthly average", func(f *FxRate) { f.RateType = RateTypeMonthlyAvg }, nil},
		{"deleted is still well formed", func(f *FxRate) { f.Status = FxStatusDeleted }, nil},
		{"missing base", func(f *FxRate) { f.Base = "" }, []string{CodeKeyMissing}},
		{"unknown base", func(f *FxRate) { f.Base = "XXX" }, []string{CodeCurrencyUnknown}},
		{"unknown quote", func(f *FxRate) { f.Quote = "XXX" }, []string{CodeCurrencyUnknown}},
		{"bad valid from", func(f *FxRate) { f.ValidFrom = "29.03.2026" }, []string{CodeDateFormat}},
		{"unknown rate type", func(f *FxRate) { f.RateType = "WEEKLY" }, []string{CodeEnumUnknown}},
		{"unknown status", func(f *FxRate) { f.Status = "GONE" }, []string{CodeEnumUnknown}},
		{"zero factor", func(f *FxRate) { f.RateFactor = 0 }, []string{CodeFieldInvalid}},
		{"negative factor", func(f *FxRate) { f.RateFactor = -1 }, []string{CodeFieldInvalid}},
		{"zero rate", func(f *FxRate) { f.Rate = MustDecimal("0.000000") }, []string{CodeFieldInvalid}},
		{"negative rate", func(f *FxRate) { f.Rate = MustDecimal("-0.960000") }, []string{CodeFieldInvalid}},
		{"negative sequence", func(f *FxRate) { f.Sequence = -1 }, []string{CodeFieldInvalid}},
		{"empty range", func(f *FxRate) { f.ValidTo = f.ValidFrom }, []string{CodeFieldInvalid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := valid
			tc.mutate(&f)
			assertCodes(t, f.Validate(), tc.codes)
		})
	}
}

func TestValidateAPInvoice(t *testing.T) {
	valid := APInvoice{
		SupplierNumber: "0000417", SupplierInvoiceNumber: "0004711",
		CompanyCode: CompanyCodeCH10, DocumentType: DocumentTypeInvoice,
		DocumentDate: "2026-03-29", ReceiptDate: "2026-03-30", Currency: "CHF",
		GrossAmount: MustDecimal("123.45"), VATAmount: MustDecimal("9.45"),
		PaymentTermsDays: 30,
		Lines: []APInvoiceLine{
			{LineNo: "00010", GLAccount: "4000", Quantity: MustDecimal("1"), UoM: "EA",
				UnitPrice: MustDecimal("114.00"), LineAmount: MustDecimal("114.00")},
		},
	}
	tests := []struct {
		name   string
		mutate func(*APInvoice)
		codes  []string
	}{
		{"valid", func(*APInvoice) {}, nil},
		{"credit note", func(i *APInvoice) { i.DocumentType = DocumentTypeCreditNote }, nil},
		{"negative amount keeps its explicit sign", func(i *APInvoice) {
			i.DocumentType = DocumentTypeCreditNote
			i.GrossAmount = MustDecimal("-123.45")
			i.Lines[0].LineAmount = MustDecimal("-114.00")
		}, nil},
		{"no receipt date", func(i *APInvoice) { i.ReceiptDate = "" }, nil},
		{"no purchase order", func(i *APInvoice) { i.PONumber = "" }, nil},
		{"raw discount is never parsed", func(i *APInvoice) { i.DiscountRaw = "2,5%" }, nil},
		{"empty line cost center is allowed", func(i *APInvoice) { i.Lines[0].CostCenter = "" }, nil},
		{"missing supplier", func(i *APInvoice) { i.SupplierNumber = "" }, []string{CodeKeyMissing}},
		{"missing invoice number", func(i *APInvoice) { i.SupplierInvoiceNumber = "" }, []string{CodeKeyMissing}},
		{"unknown document type", func(i *APInvoice) { i.DocumentType = "XX" }, []string{CodeEnumUnknown}},
		{"missing document date", func(i *APInvoice) { i.DocumentDate = "" }, []string{CodeFieldRequired}},
		{"bad document date", func(i *APInvoice) { i.DocumentDate = "29.03.2026" }, []string{CodeDateFormat}},
		{"bad receipt date", func(i *APInvoice) { i.ReceiptDate = "2026-02-30" }, []string{CodeDateFormat}},
		{"unknown currency", func(i *APInvoice) { i.Currency = "XXX" }, []string{CodeCurrencyUnknown}},
		{"gross beyond minor units", func(i *APInvoice) { i.GrossAmount = MustDecimal("123.456") },
			[]string{CodeMoneyNotIntegerMinor}},
		{"jpy with fractions", func(i *APInvoice) {
			i.Currency = "JPY"
			i.GrossAmount = MustDecimal("123.45")
			i.VATAmount = MustDecimal("0")
			i.Lines[0].LineAmount = MustDecimal("123")
		}, []string{CodeMoneyNotIntegerMinor}},
		{"negative payment terms", func(i *APInvoice) { i.PaymentTermsDays = -1 }, []string{CodeFieldInvalid}},
		{"negative discount days", func(i *APInvoice) { i.DiscountDays = -1 }, []string{CodeFieldInvalid}},
		{"line without a number", func(i *APInvoice) { i.Lines[0].LineNo = "" }, []string{CodeKeyMissing}},
		{"line with an unknown uom", func(i *APInvoice) { i.Lines[0].UoM = "ZZZ" }, []string{CodeUoMUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := valid
			inv.Lines = append([]APInvoiceLine(nil), valid.Lines...)
			tc.mutate(&inv)
			assertCodes(t, inv.Validate(), tc.codes)
		})
	}
}

func TestValidateAPInvoiceLineFieldPaths(t *testing.T) {
	inv := APInvoice{
		SupplierNumber: "0000417", SupplierInvoiceNumber: "0004711",
		CompanyCode: CompanyCodeCH10, DocumentType: DocumentTypeInvoice,
		DocumentDate: "2026-03-29", Currency: "CHF",
		Lines: []APInvoiceLine{
			{LineNo: "00010", UoM: "EA"},
			{LineNo: "", UoM: "ZZZ"},
		},
	}
	errs := inv.Validate()
	if len(errs) != 2 {
		t.Fatalf("findings = %v, want two", errs)
	}
	for _, e := range errs {
		if !strings.HasPrefix(e.Field, "lines[1].") {
			t.Fatalf("field = %q, want a lines[1] path", e.Field)
		}
	}
	if errs[0].Field != "lines[1].line_no" || errs[1].Field != "lines[1].uom" {
		t.Fatalf("fields = %q, %q", errs[0].Field, errs[1].Field)
	}
}

func TestKeyString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
		code string
	}{
		{"string", "0000417", "0000417", ""},
		{"trimmed", "  0000417\t", "0000417", ""},
		{"empty", "", "", CodeKeyMissing},
		{"whitespace", "   ", "", CodeKeyMissing},
		{"nil", nil, "", CodeKeyMissing},
		{"json number", float64(417), "", CodeKeyNotString},
		{"int", 417, "", CodeKeyNotString},
		{"bool", true, "", CodeKeyNotString},
		{"object", map[string]any{}, "", CodeKeyNotString},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errs := KeyString("supplier_number", tc.in)
			if got != tc.want {
				t.Fatalf("KeyString = %q, want %q", got, tc.want)
			}
			if tc.code == "" {
				assertCodes(t, errs, nil)
				return
			}
			assertCodes(t, errs, []string{tc.code})
			if errs[0].Field != "supplier_number" {
				t.Fatalf("field = %q", errs[0].Field)
			}
		})
	}
}

func TestFieldErrorsError(t *testing.T) {
	var none FieldErrors
	if none.OrNil() != nil {
		t.Fatal("OrNil of an empty list is not nil")
	}
	errs := FieldErrors{
		{Code: CodeKeyMissing, Field: "supplier_number", Message: "must not be empty"},
		{Code: CodeUoMUnknown, Field: "lines[0].uom", Message: "unknown unit of measure"},
	}
	if errs.OrNil() == nil {
		t.Fatal("OrNil of a non-empty list is nil")
	}
	got := errs.Error()
	for _, want := range []string{"E_KEY_MISSING supplier_number", "E_UOM_UNKNOWN lines[0].uom", "; "} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, want it to contain %q", got, want)
		}
	}
	var single error = errs[0]
	if single.Error() != "E_KEY_MISSING supplier_number: must not be empty" {
		t.Fatalf("FieldError.Error = %q", single.Error())
	}
}

func TestValidatorsArePure(t *testing.T) {
	// A validator must not touch the value it is given.
	inv := APInvoice{SupplierNumber: " 0000417 ", DiscountRaw: " 2,5% ",
		Lines: []APInvoiceLine{{LineNo: " ", CostCenter: ""}}}
	before := inv
	beforeLine := inv.Lines[0]
	_ = inv.Validate()
	if inv.SupplierNumber != before.SupplierNumber || inv.DiscountRaw != before.DiscountRaw {
		t.Fatal("validator mutated the invoice header")
	}
	if inv.Lines[0] != beforeLine {
		t.Fatal("validator mutated a line")
	}
}
