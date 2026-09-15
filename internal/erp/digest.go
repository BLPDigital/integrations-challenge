package erp

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// A StateDigest is a content digest of everything the ERP holds: the master data
// it serves and the documents it has booked.
//
// It exists so two servers can be compared without diffing thousands of records,
// and so a determinism test can assert that a fixed request script leaves the same
// state twice. Deliberately excluded: the virtual clock, the request counters, the
// quota, cursors, idempotency keys and the SOAP call log. Those are properties of
// a run, not of the data, and a digest that moved when a reviewer read the metrics
// would be useless.
type StateDigest struct {
	// Digest is the sha256 over the component digests, hex, lowercase.
	Digest string `json:"digest"`
	// Scenario and Seed identify the dataset the digest is over.
	Scenario string `json:"scenario"`
	Seed     int64  `json:"seed"`
	// Components are the per-collection digests in the fixed order they are
	// folded into Digest.
	Components []DigestComponent `json:"components"`
}

// A DigestComponent is one collection's contribution to the state digest.
type DigestComponent struct {
	Name    string `json:"name"`
	Records int    `json:"records"`
	Digest  string `json:"digest"`
}

// Digest computes the state digest.
//
// The construction is fixed: components in the order below; within a component,
// records sorted by natural key in byte order; per record the canonical JSON of
// the record itself, length-prefixed by its key and a unit separator so no two
// records can forge one another's boundary.
func (s *Server) Digest() (StateDigest, error) {
	s.mu.Lock()
	d, st := s.data, s.st
	docs := append([]Document(nil), st.documents...)
	s.mu.Unlock()

	out := StateDigest{Scenario: d.set.Scenario, Seed: d.set.Seed}
	type entry struct {
		key    string
		record any
	}
	components := []struct {
		name    string
		entries []entry
	}{}

	suppliers := make([]entry, 0, len(d.suppliers.items))
	for _, sup := range d.suppliers.items {
		suppliers = append(suppliers, entry{sup.Key(), sup})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"supplier", suppliers})

	costCenters := make([]entry, 0, len(d.set.CostCenters))
	for _, cc := range d.set.CostCenters {
		costCenters = append(costCenters, entry{cc.Key(), cc})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"cost_center", costCenters})

	pos := make([]entry, 0, len(d.purchaseOrders.items))
	for _, po := range d.purchaseOrders.items {
		pos = append(pos, entry{po.Key(), po})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"purchase_order", pos})

	lines := make([]entry, 0, len(d.poLines.items))
	for _, l := range d.poLines.items {
		lines = append(lines, entry{l.Key(), l})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"purchase_order_line", lines})

	fx := make([]entry, 0, len(d.set.FxRows))
	for _, row := range d.set.FxRows {
		c := row.Canonical()
		// Sequence is part of the digest key: rows sharing a natural key
		// supersede one another, and the ERP serves all of them.
		fx = append(fx, entry{c.Key() + model.KeySeparator + strconv.Itoa(row.Sequence), row})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"fx_row", fx})

	uom := make([]entry, 0, len(d.uom))
	for _, c := range d.uom {
		uom = append(uom, entry{c.Material + model.KeySeparator + c.AltUoM, c})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"uom_conversion", uom})

	documents := make([]entry, 0, len(docs))
	for _, doc := range docs {
		documents = append(documents, entry{doc.ExternalReference, doc})
	}
	components = append(components, struct {
		name    string
		entries []entry
	}{"document", documents})

	outer := sha256.New()
	for _, comp := range components {
		sort.SliceStable(comp.entries, func(i, j int) bool { return comp.entries[i].key < comp.entries[j].key })
		inner := sha256.New()
		for _, e := range comp.entries {
			body, err := model.CanonicalJSON(e.record)
			if err != nil {
				return StateDigest{}, err
			}
			inner.Write([]byte(e.key))
			inner.Write([]byte{0x1f})
			inner.Write(body)
			inner.Write([]byte{0x0a})
		}
		sum := hex.EncodeToString(inner.Sum(nil))
		out.Components = append(out.Components, DigestComponent{
			Name: comp.name, Records: len(comp.entries), Digest: sum,
		})
		outer.Write([]byte(comp.name))
		outer.Write([]byte{0x1f})
		outer.Write([]byte(sum))
		outer.Write([]byte{0x0a})
	}
	out.Digest = hex.EncodeToString(outer.Sum(nil))
	return out, nil
}
