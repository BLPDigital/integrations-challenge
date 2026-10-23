package model

import "strings"

// KeySeparator joins the components of a composite natural key. It is the ASCII
// unit separator U+001F, which cannot occur in any component: components are
// trimmed printable master-data fields, so the joined key is unambiguous and
// reversible.
const KeySeparator = "\x1f"

// Natural-key helpers. Every key is a single canonical string, case-sensitive
// and whitespace-trimmed per component. Leading zeros are significant: supplier
// "0000417" and "417" are different suppliers, and line "00010" is not "10".
// Keys are never normalized further (no case folding, no zero stripping, no
// Unicode normalization), because the ERP treats them as opaque identifiers.

// SupplierKey returns the key of a supplier.
func SupplierKey(supplierNumber string) string { return joinKey(supplierNumber) }

// CostCenterKey returns the key of a cost center.
func CostCenterKey(code string) string { return joinKey(code) }

// POKey returns the key of a purchase order header.
func POKey(poNumber string) string { return joinKey(poNumber) }

// POLineKey returns the key of a purchase order line.
func POLineKey(poNumber, lineNo string) string { return joinKey(poNumber, lineNo) }

// FxRateKey returns the key of an FX rate row. Sequence is deliberately absent:
// rows sharing this key supersede one another.
func FxRateKey(base, quote, validFrom, rateType string) string {
	return joinKey(base, quote, validFrom, rateType)
}

// InvoiceKey returns the key of an AP invoice.
func InvoiceKey(supplierNumber, supplierInvoiceNumber string) string {
	return joinKey(supplierNumber, supplierInvoiceNumber)
}

// InvoiceLineKey returns the key of an AP invoice line.
func InvoiceLineKey(supplierNumber, supplierInvoiceNumber, lineNo string) string {
	return joinKey(supplierNumber, supplierInvoiceNumber, lineNo)
}

// joinKey trims every component and joins them with KeySeparator.
func joinKey(parts ...string) string {
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0])
	}
	trimmed := make([]string, len(parts))
	for i, p := range parts {
		trimmed[i] = strings.TrimSpace(p)
	}
	return strings.Join(trimmed, KeySeparator)
}

// SplitKey returns the components of a composite key. A single-component key
// yields a one-element slice.
func SplitKey(key string) []string { return strings.Split(key, KeySeparator) }
