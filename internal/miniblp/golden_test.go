package miniblp

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// TestMatchingAgreesWithTheGoldenExpectations replays a whole generated scenario
// through the matching engine and compares the twin's open exception queue
// against the golden set the generator derives from the same data.
//
// It is the contract test between the two halves of the challenge that have to
// agree exactly. The generator derives what its own data says; the engine decides
// what to do about it. If they disagree, the graded symmetric difference is
// undecidable and every candidate loses points for our disagreement, so this test
// is worth more than any number of hand-built cases: it exercises every rule at
// once, on the data a candidate actually meets, including the re-delivery of
// amended invoices.
//
// The master data is loaded the way a correct connector delivers it: the FX table
// filtered to ACTIVE DAILY rows with supersession resolved, the cost centers and
// the conversion table pre-seeded, the purchase orders in their delivered
// spellings.
func TestMatchingAgreesWithTheGoldenExpectations(t *testing.T) {
	// S0 is the smoke dataset and S2 is the delta with the re-deliveries, which
	// together reach every rule including the amended-invoice one. The two large
	// scenarios are the same rules over twenty times the rows and cost a minute
	// of fsyncs, so they run when MINIBLP_GOLDEN_ALL is set.
	for _, scenario := range seed.Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			core := scenario == seed.ScenarioS0 || scenario == seed.ScenarioS2
			if !core && os.Getenv("MINIBLP_GOLDEN_ALL") != "1" {
				t.Skip("set MINIBLP_GOLDEN_ALL=1 to replay every scenario")
			}
			if testing.Short() && !core {
				t.Skip("the large scenarios are the long form of this test")
			}
			assertGoldenAgreement(t, scenario)
		})
	}
}

// assertGoldenAgreement replays one scenario and compares the queue.
func assertGoldenAgreement(t *testing.T, scenario string) {
	t.Helper()
	dataset, err := seed.Generate(scenario, 20260416)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	h := newHarness(t, nil)

	for i := range dataset.Suppliers {
		s := dataset.Suppliers[i].Supplier
		h.seedRecord(model.DatasetSupplier.String(), s.Key(), s)
	}
	for _, cc := range dataset.CostCenters {
		h.seedRecord(model.DatasetCostCenter.String(), cc.Key(), cc)
	}
	for _, po := range dataset.PurchaseOrders {
		h.seedRecord(model.DatasetPurchaseOrder.String(), po.Key(), po)
	}
	for _, line := range dataset.PurchaseOrderLines {
		h.seedRecord(model.DatasetPurchaseOrderLine.String(), line.Key(), line)
	}
	// What the connector owes the twin: DELETED rows dropped, supersession
	// resolved, MONTHLY_AVG left alone, rate and factor kept apart.
	for _, r := range dataset.ResolvedDailyRates() {
		h.seedRecord(model.DatasetFxRate.String(), r.Key(), r)
	}
	for _, c := range dataset.UoMConversions {
		conv := UoMConversion{Material: c.Material, AltUoM: c.AltUoM, Numerator: c.Numerator,
			Denominator: c.Denominator, BaseUoM: c.BaseUoM}
		h.seedRecord(DatasetUoMConversion, conv.Key(), conv)
	}

	// The deliveries in the order the ERP writes them, each one its own batch,
	// so a re-delivery is a re-delivery and not a first sighting.
	for di := range dataset.Deliveries {
		h.deliverInvoices(dataset.Deliveries[di].Invoices...)
	}

	// The golden set covers the WHOLE pipeline, and one of its codes is raised
	// by the ERP's answer rather than by the match: an invoice that bills more
	// than its order allows is refused with ERP_AMOUNT_MISMATCH and the twin
	// turns that into E_ACK_REJECTED when the acknowledgment arrives. This test
	// exercises the matching engine alone, with no ERP in it, so that code is
	// excluded here and asserted end to end by the grading scenarios instead.
	want := make([]string, 0, len(dataset.ExpectedExceptions))
	for _, e := range dataset.ExpectedExceptions {
		if e.Code == seed.ExcAckRejected {
			continue
		}
		want = append(want, e.SubjectKey+"|"+e.Code)
	}
	sort.Strings(want)
	got := h.openPairs()

	missing, spurious := diffSets(want, got)
	if len(missing) != 0 || len(spurious) != 0 {
		t.Fatalf("the engine and the golden set disagree\n missing (%d): %s\n spurious (%d): %s",
			len(missing), sample(missing), len(spurious), sample(spurious))
	}

	// Every invoice that is not blocked has to have produced exactly one
	// proposal, or the twin dropped one silently.
	//
	// An amended invoice is the one case where a blocked invoice still has a
	// proposal: its FIRST delivery matched and was proposed, and it is the
	// re-delivery with the changed content that is blocked. That is the whole
	// point of the rule - the first posting is legitimate and the second one
	// would be the duplicate - so the amended code alone does not remove an
	// invoice from the expected count.
	// The ERP-raised code is the other case where an exception does not mean the
	// twin refused to propose: the twin proposed, the ERP refused the posting,
	// and the exception arrived afterwards through the acknowledgment.
	blocked := map[string]bool{}
	for _, e := range dataset.ExpectedExceptions {
		if e.Code == ExcDuplicateInvoiceAmended || e.Code == seed.ExcAckRejected {
			continue
		}
		blocked[e.SubjectKey] = true
	}
	expectProposals := 0
	for _, inv := range dataset.Invoices {
		if !blocked[inv.Key()] {
			expectProposals++
		}
	}
	counts, err := h.srv.Store().CountProposalsByStatus()
	if err != nil {
		t.Fatalf("proposal counts: %v", err)
	}
	if counts[store.ProposalPending] != expectProposals {
		t.Fatalf("pending proposals = %d, want %d (one per unblocked invoice)",
			counts[store.ProposalPending], expectProposals)
	}
}

// diffSets returns the entries of want that got is missing and the entries of got
// that want does not contain. Both inputs are sorted.
func diffSets(want, got []string) (missing, spurious []string) {
	inGot := make(map[string]bool, len(got))
	for _, g := range got {
		inGot[g] = true
	}
	inWant := make(map[string]bool, len(want))
	for _, w := range want {
		inWant[w] = true
	}
	for _, w := range want {
		if !inGot[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !inWant[g] {
			spurious = append(spurious, g)
		}
	}
	return missing, spurious
}

// sample renders at most five entries of a difference, with the unit separator
// made visible.
func sample(list []string) string {
	if len(list) > 5 {
		list = list[:5]
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, strings.ReplaceAll(s, model.KeySeparator, "/"))
	}
	return strings.Join(out, ", ")
}
