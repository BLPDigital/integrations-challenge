package seed

import (
	"fmt"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Validate reports the first inconsistency in the dataset, or nil.
//
// It runs the canonical model validators over every record, with two documented
// exemptions, because two of the planted defects are precisely values the
// canonical validators reject:
//
//   - A purchase order or invoice line may carry an alternative unit of measure
//     that is not a canonical base unit: TON, STK and PAL are the sender's
//     units, and the ERP's conversion table is what turns them into base units.
//     PAL has no conversion at all, which is the EXC_UOM_UNMAPPABLE trap and
//     must survive into the data.
//   - A supplier number may be shorter than the canonical ten characters:
//     [CollisionSupplierShort] is a migration remnant, and it is the whole point
//     of the leading-zero collision pair.
//   - A purchase order line's unit price may carry three or four fraction
//     digits. The customer document defines Einzelpreis as two to four decimals
//     and the build specification repeats it, so a four-decimal unit price is a
//     correct value of this landscape even though the canonical validator asks
//     every amount to be expressible in the minor units of its currency. A unit
//     price is a rate, not a payable amount; only the line amount and the
//     document totals are payable, and those are checked without exemption.
//
// Everything else is checked, so a defect the generator did not intend cannot
// hide behind the ones it did.
func (d *Dataset) Validate() error {
	for i := range d.Suppliers {
		s := d.Suppliers[i]
		if errs := model.ValidateSupplier(s.Supplier); len(errs) > 0 {
			return fmt.Errorf("supplier %q: %w", s.SupplierNumber, errs)
		}
		if s.LegacyID <= 0 {
			return fmt.Errorf("supplier %q: legacy_id must be a positive number", s.SupplierNumber)
		}
	}
	for i := range d.CostCenters {
		if errs := model.ValidateCostCenter(d.CostCenters[i]); len(errs) > 0 {
			return fmt.Errorf("cost center %q: %w", d.CostCenters[i].Code, errs)
		}
	}
	for i := range d.PurchaseOrders {
		if errs := model.ValidatePurchaseOrder(d.PurchaseOrders[i]); len(errs) > 0 {
			return fmt.Errorf("purchase order %q: %w", d.PurchaseOrders[i].PONumber, errs)
		}
	}
	for i := range d.PurchaseOrderLines {
		l := d.PurchaseOrderLines[i]
		for _, fe := range model.ValidatePurchaseOrderLine(l) {
			if fe.Field == "uom" && fe.Code == model.CodeUoMUnknown {
				continue // documented exemption: alternative units
			}
			if fe.Field == "unit_price" && fe.Code == model.CodeMoneyNotIntegerMinor {
				continue // documented exemption: Einzelpreis carries 2 to 4 decimals
			}
			return fmt.Errorf("purchase order line %q/%q: %w", l.PONumber, l.LineNo, fe)
		}
	}
	for i := range d.FxRows {
		if errs := model.ValidateFxRate(d.FxRows[i].Canonical()); len(errs) > 0 {
			return fmt.Errorf("fx row %d (%s %s): %w", i, d.FxRows[i].Base, d.FxRows[i].ValidFromDate, errs)
		}
	}
	for i := range d.Invoices {
		inv := d.Invoices[i]
		for _, fe := range model.ValidateAPInvoice(inv) {
			if fe.Code == model.CodeUoMUnknown {
				continue // documented exemption: alternative units
			}
			return fmt.Errorf("invoice %q/%q: %w", inv.SupplierNumber, inv.SupplierInvoiceNumber, fe)
		}
	}
	for i := range d.UoMConversions {
		c := d.UoMConversions[i]
		if c.AltUoM == "" || c.BaseUoM == "" || c.Numerator <= 0 || c.Denominator <= 0 {
			return fmt.Errorf("uom conversion %d (%s/%s) is incomplete", i, c.Material, c.AltUoM)
		}
	}
	var prev int64
	for i, e := range d.ChangeSeqs {
		if e.ChangeSeq <= prev {
			return fmt.Errorf("change sequence %d at index %d is not strictly increasing after %d",
				e.ChangeSeq, i, prev)
		}
		prev = e.ChangeSeq
	}
	for i := range d.ExpectedExceptions {
		if !isPublishedExceptionCode(d.ExpectedExceptions[i].Code) {
			return fmt.Errorf("expected exception %d carries unpublished code %q",
				i, d.ExpectedExceptions[i].Code)
		}
	}
	return nil
}

// isPublishedExceptionCode reports whether code is in the closed published set.
func isPublishedExceptionCode(code string) bool {
	for _, c := range ExceptionCodes() {
		if c == code {
			return true
		}
	}
	return false
}
