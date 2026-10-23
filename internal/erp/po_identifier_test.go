package erp

import (
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// docs/rules.md publishes one normalization rule and one only: a purchase order
// number is trimmed, a leading "PO-" is dropped, and what is left is compared.
// The twin implemented that; the ERP did not, so a connector that applied the
// published rule before posting was told the order does not exist on the quarter
// of orders the generator spells with a prefix.
//
// This test pins both directions on real seeded data, and it also pins the
// consequence a candidate would actually meet: the amount tolerance needs the
// order's net total, which is keyed the same way.
func TestEveryPublishedSpellingOfAPurchaseOrderResolves(t *testing.T) {
	d, err := loadData(seed.ScenarioS0, 20260416)
	if err != nil {
		t.Fatalf("loadData: %v", err)
	}

	var prefixed, padded, bare string
	for i := range d.purchaseOrders.items {
		switch n := d.purchaseOrders.items[i].PONumber; {
		case strings.HasPrefix(n, "PO-") && prefixed == "":
			prefixed = n
		case n != strings.TrimSpace(n) && padded == "":
			padded = n
		case n == strings.TrimSpace(n) && !strings.HasPrefix(n, "PO-") && bare == "":
			bare = n
		}
	}
	if prefixed == "" || padded == "" || bare == "" {
		t.Fatalf("the seed no longer plants all three spellings: prefixed=%q padded=%q bare=%q",
			prefixed, padded, bare)
	}

	// Every spelling of a stored number, and the other spelling of it, resolve to
	// the same order.
	cases := []struct{ name, lookup, want string }{
		{"the prefixed spelling as stored", prefixed, prefixed},
		{"the prefixed order without its prefix", strings.TrimPrefix(prefixed, "PO-"), prefixed},
		{"the space padded spelling as stored", padded, padded},
		{"the padded order trimmed", strings.TrimSpace(padded), padded},
		{"the bare spelling as stored", bare, bare},
		{"the bare order with a prefix added", "PO-" + bare, bare},
		{"the bare order padded", " " + bare + " ", bare},
	}
	for _, c := range cases {
		po, ok := d.purchaseOrder(c.lookup)
		if !ok {
			t.Errorf("%s: purchaseOrder(%q) not found", c.name, c.lookup)
			continue
		}
		if po.PONumber != c.want {
			t.Errorf("%s: purchaseOrder(%q) = %q, want %q", c.name, c.lookup, po.PONumber, c.want)
		}
	}

	// The tolerance check reads the order's net total from its lines, which carry
	// the header's spelling. A key that disagreed with the lookup would make the
	// total absent for exactly the prefixed quarter.
	for _, n := range []string{prefixed, padded, bare} {
		po, ok := d.purchaseOrder(n)
		if !ok {
			t.Fatalf("purchaseOrder(%q) not found", n)
		}
		if _, ok := d.poNetTotal[poKey(po.PONumber)]; !ok {
			t.Errorf("no net total indexed for %q, so the amount tolerance cannot be checked", po.PONumber)
		}
	}

	// Two spellings of one order are one logical request, so fault injection and
	// the request log treat them as the same call.
	if a, b := canonicalIDs(prefixed), canonicalIDs(strings.TrimPrefix(prefixed, "PO-")); len(a) != 1 ||
		len(b) != 1 || a[0] != b[0] {
		t.Errorf("canonicalIDs disagrees between spellings: %v vs %v", a, b)
	}
}
