package seed

import (
	"fmt"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// CRLF is the record terminator of the KRED-EXP format. Every record ends with
// it, the last one included.
const CRLF = "\r\n"

// FieldSeparator is the KRED-EXP field separator.
const FieldSeparator = ";"

// Record type tags of the KRED-EXP format, always capitalized.
const (
	RecordVorlauf  = "VORLAUF"
	RecordKopf     = "KOPF"
	RecordPos      = "POS"
	RecordNachlauf = "NACHLAUF"
)

// LegacyFileName returns the data file name of a delivery:
// KRED_<Mandant>_<YYYYMMDD>_<NNN>.txt. The run counter restarts at 001 every
// day, so two deliveries of the same day differ only in the counter.
func LegacyFileName(mandant, exportDate string, runNumber int) (string, error) {
	d, err := model.ParseDate(exportDate)
	if err != nil {
		return "", err
	}
	if runNumber < 1 || runNumber > 999 {
		return "", fmt.Errorf("seed: run number %d is outside 001..999", runNumber)
	}
	return fmt.Sprintf("KRED_%s_%04d%02d%02d_%03d.txt", mandant, d.Year, d.Month, d.Day, runNumber), nil
}

// OKFileNameFor returns the sentinel name of a data file: the same base with the
// .ok extension. The sentinel is empty and the sender writes it last, so a data
// file without its sibling must not be read.
func OKFileNameFor(dataFileName string) string {
	return strings.TrimSuffix(dataFileName, ".txt") + ".ok"
}

// buildDelivery renders one KRED-EXP delivery.
//
// The output is genuine Windows-1252, CRLF terminated, with no byte order mark
// and no column header row, exactly the record layout of the customer document.
// The Nachlaufsatz control totals are CORRECT for the file: this file must be
// valid, and the invalid variants exist only as fixtures of the Go task.
func (b *builder) buildDelivery(companyCode string, runNumber int, invoices []model.APInvoice) (LegacyDelivery, error) {
	mandant, err := MandantForCompanyCode(companyCode)
	if err != nil {
		return LegacyDelivery{}, err
	}
	name, err := LegacyFileName(mandant, b.spec.ExportDate, runNumber)
	if err != nil {
		return LegacyDelivery{}, err
	}
	// The creation timestamp is a constant of the scenario and the run counter,
	// never a reading of the wall clock.
	stamp, err := FormatTTMMJJHHMM(b.spec.ExportDate, 5+runNumber, 12+7*runNumber)
	if err != nil {
		return LegacyDelivery{}, err
	}

	var records [][]string
	records = append(records, []string{
		RecordVorlauf, FormatVersion, mandant, stamp, QuoteCurrency, Sender, Receiver, TestIndicator,
	})

	sumGross := dec(0, 2)
	sumLines := dec(0, 2)
	posCount := 0
	for i := range invoices {
		inv := invoices[i]
		kopf, err := b.kopfRecord(inv)
		if err != nil {
			return LegacyDelivery{}, err
		}
		records = append(records, kopf)
		next, err := sumGross.Add(inv.GrossAmount)
		if err != nil {
			return LegacyDelivery{}, err
		}
		if sumGross, err = next.Rescale(2); err != nil {
			return LegacyDelivery{}, err
		}
		for _, line := range inv.Lines {
			pos, err := b.posRecord(inv, line)
			if err != nil {
				return LegacyDelivery{}, err
			}
			records = append(records, pos)
			posCount++
			next, err := sumLines.Add(line.LineAmount)
			if err != nil {
				return LegacyDelivery{}, err
			}
			if sumLines, err = next.Rescale(2); err != nil {
				return LegacyDelivery{}, err
			}
		}
	}

	grossLit, err := FormatSwissAmount(sumGross, 2, true)
	if err != nil {
		return LegacyDelivery{}, err
	}
	linesLit, err := FormatSwissAmount(sumLines, 2, true)
	if err != nil {
		return LegacyDelivery{}, err
	}
	records = append(records, []string{
		RecordNachlauf,
		fmt.Sprintf("%d", len(invoices)),
		fmt.Sprintf("%d", posCount),
		grossLit,
		linesLit,
	})

	var sb strings.Builder
	for _, rec := range records {
		for k, f := range rec {
			if k > 0 {
				sb.WriteString(FieldSeparator)
			}
			sb.WriteString(QuoteLegacyField(f))
		}
		sb.WriteString(CRLF)
	}
	raw, err := EncodeCP1252(sb.String())
	if err != nil {
		return LegacyDelivery{}, fmt.Errorf("seed: delivery %s: %w", name, err)
	}
	return LegacyDelivery{
		Name:        name,
		OKName:      OKFileNameFor(name),
		Mandant:     mandant,
		CompanyCode: companyCode,
		RunNumber:   runNumber,
		ExportDate:  b.spec.ExportDate,
		Bytes:       raw,
		Invoices:    invoices,
		KopfCount:   len(invoices),
		PosCount:    posCount,
		SumGross:    sumGross,
		SumLines:    sumLines,
	}, nil
}

// kopfRecord renders the sixteen fields of a Kopfsatz.
func (b *builder) kopfRecord(inv model.APInvoice) ([]string, error) {
	belegdatum, err := FormatTTMMJJ(inv.DocumentDate)
	if err != nil {
		return nil, err
	}
	eingang := ""
	if inv.ReceiptDate != "" {
		if eingang, err = FormatTTMMJJ(inv.ReceiptDate); err != nil {
			return nil, err
		}
	}
	// Every formatting choice in this record is a function of the DOCUMENT, not
	// of its position in the file, so a re-delivered invoice renders byte
	// identically. See stableFormatChoice.
	choice := stableFormatChoice(inv.SupplierNumber, inv.SupplierInvoiceNumber)
	group := choice%7 != 3
	gross, err := FormatSwissAmount(inv.GrossAmount, 2, group)
	if err != nil {
		return nil, err
	}
	// An empty MWST-Betrag means 0.00, which the document says explicitly, so a
	// zero-rated document leaves the field empty on some deliveries.
	vat := ""
	if !inv.VATAmount.IsZero() || choice%11 != 5 {
		if vat, err = FormatSwissAmount(inv.VATAmount, 2, group); err != nil {
			return nil, err
		}
	}
	// An empty Währung means the currency of the Vorlaufsatz applies, which is
	// CHF; a document in any other currency must always name it.
	currency := inv.Currency
	if currency == QuoteCurrency && choice%13 == 0 {
		currency = ""
	}
	terms := ""
	if inv.PaymentTermsDays > 0 {
		terms = fmt.Sprintf("%d", inv.PaymentTermsDays)
	}
	discountDays := ""
	if inv.DiscountDays > 0 {
		discountDays = fmt.Sprintf("%d", inv.DiscountDays)
	}
	return []string{
		RecordKopf,
		inv.SupplierInvoiceNumber,
		inv.SupplierNumber,
		inv.DocumentType,
		belegdatum,
		eingang,
		inv.PONumber,
		currency,
		gross,
		vat,
		inv.VATCode,
		terms,
		inv.DiscountRaw,
		discountDays,
		inv.CostCenter,
		inv.Text,
	}, nil
}

// stableFormatChoice is the per-record formatting draw of the legacy writer: a
// small hash of the fields that identify the record, so the same record renders
// byte-identically in every delivery it appears in.
//
// It is deliberately not the file position and deliberately not the seeded PRNG:
// the position moves between deliveries, and a PRNG draw depends on how many
// records came before.
func stableFormatChoice(parts ...string) int {
	h := 2166136261
	for _, p := range parts {
		for i := 0; i < len(p); i++ {
			h ^= int(p[i])
			h = (h * 16777619) & 0x7fffffff
		}
		h ^= 0x1f
		h = (h * 16777619) & 0x7fffffff
	}
	return h
}

// posRecord renders the eleven fields of a Positionssatz.
func (b *builder) posRecord(inv model.APInvoice, line model.APInvoiceLine) ([]string, error) {
	// Quantities carry up to three decimals and are never grouped.
	qty, err := FormatSwissAmount(line.Quantity, 3, false)
	if err != nil {
		return nil, err
	}
	// The unit price carries two to four decimals. Where the value is exact at
	// two, some records use two, which is what "two to four" means in practice.
	//
	// WHICH records use two is a function of the DOCUMENT, never of the record's
	// position in the file. That distinction is not cosmetic: a re-delivered
	// invoice sits at a different position in the second file, so a
	// position-dependent choice wrote the same price as 14.5300 in one delivery
	// and 14.53 in the other. The parser preserves the scale as written, exactly
	// as it should, so the twin saw a content change and raised the
	// amended-invoice exception on a delivery that amended nothing. A
	// re-delivery has to be byte identical, and that means every formatting
	// decision has to come from the data.
	priceScale := uint8(4)
	if stableFormatChoice(inv.SupplierInvoiceNumber, line.LineNo)%3 == 1 {
		if _, err := line.UnitPrice.Rescale(2); err == nil {
			priceScale = 2
		}
	}
	price, err := FormatSwissAmount(line.UnitPrice, priceScale, false)
	if err != nil {
		return nil, err
	}
	amount, err := FormatSwissAmount(line.LineAmount, 2,
		stableFormatChoice(inv.SupplierInvoiceNumber, line.LineNo)%7 != 3)
	if err != nil {
		return nil, err
	}
	return []string{
		RecordPos,
		inv.SupplierInvoiceNumber,
		line.LineNo,
		line.GLAccount,
		line.CostCenter,
		qty,
		line.UoM,
		price,
		amount,
		line.TaxCode,
		b.lineText(inv, line.LineNo),
	}, nil
}

// splitByCompanyCode partitions invoices into the CH10 and CH20 deliveries,
// preserving file order inside each. The company code travels in the Mandant of
// the Vorlaufsatz and in the file name, which is the only place the customer's
// own layout carries it: a Kopfsatz has no company code field, so one delivery
// per company code is the documented way to say which one an invoice belongs to.
func splitByCompanyCode(invoices []model.APInvoice) (ch10, ch20 []model.APInvoice) {
	for _, inv := range invoices {
		if inv.CompanyCode == model.CompanyCodeCH20 {
			ch20 = append(ch20, inv)
			continue
		}
		ch10 = append(ch10, inv)
	}
	return ch10, ch20
}
