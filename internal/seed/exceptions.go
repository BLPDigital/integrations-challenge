package seed

import (
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// SubjectTypeInvoice is the subject type of every exception this generator
// derives: all of them are statements about one invoice.
const SubjectTypeInvoice = "invoice"

// DeriveExceptions replays the documented matching order over the dataset's own
// deliveries and returns the exceptions a correct implementation must have open
// at the end of the run.
//
// It is the single source of the golden set: the generator does not record what
// it intended to plant, it derives what the data actually says, so an invoice
// that became an exception by accident shows up in the golden set instead of
// silently disagreeing with it. A test asserts the derived mix matches the
// planted plan.
//
// The order is the published one and the first failing rule wins, one exception
// per invoice:
//
//  1. the supplier must exist and must not be blocked;
//  2. a purchase order reference must resolve, belong to the same supplier and
//     be neither CLOSED nor CANCELLED;
//  3. the gross amount must equal the sum of the line amounts plus VAT, exactly;
//  4. the cost center (the invoice's, else the purchase order's) must exist, be
//     configured for the invoice's company code, be unblocked and be valid on
//     the document date;
//  5. a non-CHF invoice needs a DAILY rate covering the document date as a
//     Europe/Zurich calendar date, and a CH20 invoice has no rate table at all;
//  6. every line's unit of measure must be mappable and must convert to a base
//     quantity that is exact at three decimals.
//
// A re-delivery is checked before any of that: identical content dedupes, and
// changed content is EXC_DUPLICATE_INVOICE_AMENDED and stops there, because
// re-posting an amended invoice is a duplicate in a customer's ledger.
func (d *Dataset) DeriveExceptions() ([]ExpectedException, error) {
	suppliers := make(map[string]Supplier, len(d.Suppliers))
	for i := range d.Suppliers {
		suppliers[d.Suppliers[i].SupplierNumber] = d.Suppliers[i]
	}
	costCenters := make(map[string]model.CostCenter, len(d.CostCenters))
	for i := range d.CostCenters {
		costCenters[d.CostCenters[i].Code] = d.CostCenters[i]
	}
	pos := make(map[string]model.PurchaseOrder, len(d.PurchaseOrders))
	for i := range d.PurchaseOrders {
		pos[CanonicalPONumber(d.PurchaseOrders[i].PONumber)] = d.PurchaseOrders[i]
	}
	poLines := map[string]map[string]model.PurchaseOrderLine{}
	for i := range d.PurchaseOrderLines {
		l := d.PurchaseOrderLines[i]
		c := CanonicalPONumber(l.PONumber)
		if poLines[c] == nil {
			poLines[c] = map[string]model.PurchaseOrderLine{}
		}
		poLines[c][l.LineNo] = l
	}
	conv := make(map[string]UoMConversion, len(d.UoMConversions))
	for i := range d.UoMConversions {
		c := d.UoMConversions[i]
		conv[c.Material+"\x1f"+c.AltUoM] = c
	}
	lookupConv := func(material, unit string) (UoMConversion, bool) {
		if c, ok := conv[material+"\x1f"+unit]; ok {
			return c, true
		}
		c, ok := conv["\x1f"+unit]
		return c, ok
	}
	rates := map[string][]model.FxRate{}
	for _, r := range d.ResolvedDailyRates() {
		rates[r.Base] = append(rates[r.Base], r)
	}

	seen := map[string]string{}
	found := map[string]bool{}
	var out []ExpectedException
	raise := func(key, code string) {
		k := key + "\x1f" + code
		if found[k] {
			return
		}
		found[k] = true
		out = append(out, ExpectedException{SubjectKey: key, SubjectType: SubjectTypeInvoice, Code: code})
	}

	for di := range d.Deliveries {
		for _, inv := range d.Deliveries[di].Invoices {
			key := inv.Key()
			hash := model.ContentHash(inv)
			if prev, ok := seen[key]; ok {
				if prev != hash {
					raise(key, ExcDuplicateInvoiceAmend)
				}
				continue
			}
			seen[key] = hash

			// 1. Supplier.
			sup, ok := suppliers[inv.SupplierNumber]
			if !ok {
				raise(key, ExcSupplierUnknown)
				continue
			}
			if sup.Blocked {
				raise(key, ExcSupplierBlocked)
				continue
			}

			// 2. Purchase order.
			var po model.PurchaseOrder
			havePO := false
			if inv.PONumber != "" {
				num, _ := SplitPOReference(inv.PONumber)
				po, havePO = pos[CanonicalPONumber(num)]
				if !havePO {
					raise(key, ExcPONotFound)
					continue
				}
				if po.SupplierNumber != inv.SupplierNumber {
					raise(key, ExcPOSupplierMismatch)
					continue
				}
				if po.Status == model.POStatusClosed || po.Status == model.POStatusCancelled {
					raise(key, ExcPOClosed)
					continue
				}
			}

			// 3. Header total consistency, exact.
			lineSum, err := inv.LineTotal()
			if err != nil {
				return nil, err
			}
			want, err := lineSum.Add(inv.VATAmount)
			if err != nil {
				return nil, err
			}
			if !want.Equal(inv.GrossAmount) {
				raise(key, ExcTotalsMismatch)
				continue
			}

			// 4. Cost center: the invoice's, else the purchase order's.
			code := inv.CostCenter
			if code == "" && havePO {
				code = po.CostCenter
			}
			cc, ok := costCenters[code]
			if !ok || cc.Blocked || cc.CompanyCode != inv.CompanyCode {
				raise(key, ExcCostCenterUnknown)
				continue
			}
			inRange, err := model.DateInHalfOpenRange(inv.DocumentDate, cc.ValidFrom, cc.ValidTo)
			if err != nil {
				return nil, err
			}
			if !inRange {
				raise(key, ExcCostCenterUnknown)
				continue
			}

			// 5. Currency. CH20 has no rate table configured at all, so a
			// foreign currency invoice of that company code can never be
			// converted; retrying its permanent SOAP fault is a mistake, and
			// posting it unconverted is a worse one.
			if inv.Currency != QuoteCurrency {
				if inv.CompanyCode == model.CompanyCodeCH20 {
					raise(key, ExcFxRateMissing)
					continue
				}
				if _, ok, err := winningRate(rates[inv.Currency], inv.DocumentDate); err != nil {
					return nil, err
				} else if !ok {
					raise(key, ExcFxRateMissing)
					continue
				}
			}

			// 6. Units of measure.
			uomCode := ""
			for _, line := range inv.Lines {
				material := ""
				if havePO {
					if l, ok := poLines[CanonicalPONumber(po.PONumber)][line.LineNo]; ok {
						material = l.Material
					}
				}
				c, ok := lookupConv(material, line.UoM)
				if !ok {
					uomCode = ExcUoMUnmappable
					break
				}
				if _, err := convertQuantity(line.Quantity, c); err != nil {
					uomCode = ExcUoMUnconvertible
					break
				}
			}
			if uomCode != "" {
				raise(key, uomCode)
				continue
			}

			// 7. The ERP's own answer. Everything above is what the twin
			// concludes from the data; this is the one exception the ERP raises
			// after the twin was happy. An invoice that bills more than its
			// order allows is refused with ERP_AMOUNT_MISMATCH, and the twin
			// turns that answer into an E_ACK_REJECTED exception, so the
			// expected set has to carry it or a correct connector looks wrong.
			//
			// The comparison mirrors the ERP exactly: net of VAT, one sided,
			// and skipped across currencies (see internal/erp, withinAmountTolerance).
			if havePO && inv.Currency == po.Currency {
				poNet, err := poNetTotal(linesOfPO(poLines, po.PONumber))
				if err != nil {
					return nil, err
				}
				if poNet.Sign() > 0 {
					limit, err := poNet.MulRate(amountToleranceFactor, 1, 2)
					if err != nil {
						return nil, err
					}
					if lineSum.Cmp(limit) > 0 {
						raise(key, CodeAckRejected)
						continue
					}
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectKey != out[j].SubjectKey {
			return out[i].SubjectKey < out[j].SubjectKey
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

// amountToleranceFactor is 1 plus the ERP's published amount tolerance. It lives
// here as a decimal so the expectation and the ERP compute the same limit.
var amountToleranceFactor = mustParseDec("1.05")

// CodeAckRejected is the twin's code for an ERP business rejection carried back
// through an acknowledgment. It is duplicated here rather than imported to keep
// the seed free of a dependency on the twin.
const CodeAckRejected = "E_ACK_REJECTED"

// linesOfPO returns a purchase order's lines from the index the derivation built.
func linesOfPO(index map[string]map[string]model.PurchaseOrderLine, poNumber string) []model.PurchaseOrderLine {
	byLine := index[CanonicalPONumber(poNumber)]
	out := make([]model.PurchaseOrderLine, 0, len(byLine))
	keys := make([]string, 0, len(byLine))
	for k := range byLine {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, byLine[k])
	}
	return out
}

// winningRate returns the DAILY rate covering a calendar date, highest Sequence
// wins.
func winningRate(rows []model.FxRate, date string) (model.FxRate, bool, error) {
	var best model.FxRate
	found := false
	for _, r := range rows {
		in, err := model.DateInHalfOpenRange(date, r.ValidFrom, r.ValidTo)
		if err != nil {
			return model.FxRate{}, false, err
		}
		if !in {
			continue
		}
		if !found || r.Sequence > best.Sequence {
			best, found = r, true
		}
	}
	return best, found, nil
}
