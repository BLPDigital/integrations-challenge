package grader

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/seed"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func init() {
	register(KindStateDigest, checkStateDigest)
	register(KindDatasetCount, checkDatasetCount)
	register(KindRecordField, checkRecordField)
	register(KindExceptionSet, checkExceptionSet)
	register(KindExceptionsSetExactly, checkExceptionSet)
	register(KindProposalStatusCount, checkProposalStatusCount)
	register(KindProposalAmount, checkProposalAmount)
	register(KindERPDocumentSet, checkERPDocumentSet)
	register(KindExternalReferenceUnique, checkExternalReferenceUnique)
	register(KindStoreCountUnchanged, checkStoreCountUnchanged)
	register(KindSOAPCallCount, checkSOAPCallCount)
	register(KindTerminalStatesSum, checkTerminalStatesSum)
}

// The assertion kinds of BUILD-SPEC 13 and 16.5 that read the twin's and the
// ERP's state.
const (
	// KindStateDigest compares the twin's state digest against the pinned
	// golden value. It is the primary assertion: a hash over logical content
	// only, so it is blind to channel, format, batching and delivery order.
	KindStateDigest = "state_digest"
	// KindDatasetCount compares record counts per dataset.
	KindDatasetCount = "dataset_count"
	// KindRecordField compares one field of one record, which is how a
	// scenario pins a specific trap: a leading zero preserved, a factor
	// applied, a sign kept.
	KindRecordField = "record_field"
	// KindExceptionSet compares the twin's open exception queue against the
	// golden set as a symmetric difference.
	KindExceptionSet = "exception_set"
	// KindExceptionsSetExactly is 16.5's name for the same check.
	KindExceptionsSetExactly = "exceptions_set_exactly"
	// KindProposalStatusCount compares the proposal status histogram.
	KindProposalStatusCount = "proposal_status_count"
	// KindProposalAmount pins one proposal's converted amount, which is where
	// the rounding mode and the rate factor become visible.
	KindProposalAmount = "proposal_amount"
	// KindERPDocumentSet compares the set of documents the ERP holds against
	// the set the data says should exist.
	KindERPDocumentSet = "erp_document_set"
	// KindExternalReferenceUnique asserts one document per external reference.
	KindExternalReferenceUnique = "external_reference_unique"
	// KindStoreCountUnchanged asserts a replay changed no record count.
	KindStoreCountUnchanged = "store_count_unchanged"
	// KindSOAPCallCount bounds the SOAP calls, which is where retrying a
	// permanent fault becomes visible.
	KindSOAPCallCount = "soap_call_count"
	// KindTerminalStatesSum asserts every source record reached exactly one
	// terminal state: the anti-silent-drop invariant at run scope.
	KindTerminalStatesSum = "terminal_states_sum_to_source_count"
)

// checkStateDigest compares the twin's state digest against the pinned golden
// value.
//
// An unpinned digest is a skip, not a pass and not a failure. Pinning it needs a
// reference run somebody read, and awarding twelve points for an unpinned
// comparison, or taking twelve away, would both be wrong. The observed value is
// in the evidence tree, named in the summary, ready to be pinned.
func checkStateDigest(a Assertion, obs *Observed) (Result, error) {
	want := strings.TrimSpace(obs.Golden.StateDigest)
	got := obs.TwinDigest.Digest
	if want == "" {
		res, _ := skip("state digest not pinned for %s; observed %s (evidence %s). "+
			"Pin it in the scenario file's golden.state_digest after a reviewed reference run.",
			obs.Scenario, short(got), obs.Ev("twin-digest.json"))
		res.Info = map[string]string{"observed_digest": got}
		return res, nil
	}
	if want == got {
		return pass("state digest matches the pinned golden value (%s)", short(got))
	}
	// A bare digest mismatch is the one thing selfcheck must never print, so the
	// failure carries the per-dataset counts that are the first place to look.
	res, _ := fail("state digest differs; the datasets below are where to look")
	res.Findings = append(res.Findings, Finding{
		Subject:      "state_digest",
		Expected:     want,
		Actual:       got,
		Hint:         "the digest covers natural keys and payloads only: no versions, no provenance, no counters",
		EvidencePath: obs.Ev("twin-digest.json"),
	})
	names := sortedKeys(obs.TwinDigest.Datasets)
	expectedCounts := expectedDatasetCounts(obs)
	for _, ds := range names {
		got := obs.TwinDigest.Datasets[ds]
		if want, ok := expectedCounts[ds]; ok && want != got {
			res.Findings = append(res.Findings, Finding{
				Subject:      "dataset " + ds,
				Expected:     fmt.Sprintf("%d records", want),
				Actual:       fmt.Sprintf("%d records", got),
				EvidencePath: obs.Ev("twin-digest.json"),
			})
		}
	}
	for ds, want := range expectedCounts {
		if _, ok := obs.TwinDigest.Datasets[ds]; !ok {
			res.Findings = append(res.Findings, Finding{
				Subject:      "dataset " + ds,
				Expected:     fmt.Sprintf("%d records", want),
				Actual:       "the dataset is absent from the twin",
				EvidencePath: obs.Ev("twin-digest.json"),
			})
		}
	}
	sort.Slice(res.Findings, func(i, j int) bool { return res.Findings[i].Subject < res.Findings[j].Subject })
	return res, nil
}

// expectedDatasetCounts derives the record count the twin should hold per
// dataset from the generated dataset.
//
// The exchange rates are deliberately absent. The published rules say the
// MONTHLY_AVG rows must not be *used* for a daily conversion; they do not say
// whether they must be delivered to the twin at all, so two correct connectors
// may legitimately hold a different number of rate rows. Asserting a count there
// would grade a choice the specification leaves open. The rates are graded
// through the proposal amounts instead, which is where a wrong rate actually
// costs money.
func expectedDatasetCounts(obs *Observed) map[string]int {
	d := obs.Dataset
	if d == nil {
		return nil
	}
	return map[string]int{
		model.DatasetSupplier.String():          len(d.Suppliers),
		model.DatasetPurchaseOrder.String():     len(d.PurchaseOrders),
		model.DatasetPurchaseOrderLine.String(): len(d.PurchaseOrderLines),
		model.DatasetCostCenter.String():        len(d.CostCenters),
		model.DatasetInvoice.String():           len(d.Invoices),
	}
}

// datasetCountArgs are the arguments of a dataset_count assertion.
type datasetCountArgs struct {
	// Counts pins exact counts per dataset. An empty map derives them from the
	// generated dataset instead.
	Counts map[string]int `json:"counts,omitempty"`
	// Datasets restricts a derived comparison to these datasets.
	Datasets []string `json:"datasets,omitempty"`
}

// checkDatasetCount compares record counts per dataset.
func checkDatasetCount(a Assertion, obs *Observed) (Result, error) {
	var args datasetCountArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	want := args.Counts
	if len(want) == 0 {
		want = expectedDatasetCounts(obs)
		if len(args.Datasets) > 0 {
			keep := map[string]int{}
			for _, ds := range args.Datasets {
				if n, ok := want[ds]; ok {
					keep[ds] = n
				}
			}
			want = keep
		}
	}
	if len(want) == 0 {
		return skip("no dataset counts to compare")
	}
	res := Result{Status: StatusPass}
	for _, ds := range sortedKeys(want) {
		got := obs.TwinDigest.Datasets[ds]
		if got != want[ds] {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      ds,
				Expected:     fmt.Sprintf("%d records", want[ds]),
				Actual:       fmt.Sprintf("%d records", got),
				EvidencePath: obs.Ev("twin-digest.json"),
			})
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("record counts match in all %d datasets", len(want))
	} else {
		res.Summary = fmt.Sprintf("record counts differ in %d of %d datasets", len(res.Findings), len(want))
	}
	return res, nil
}

// recordFieldArgs are the arguments of a record_field assertion.
type recordFieldArgs struct {
	// Dataset and Key name the record.
	Dataset string `json:"dataset"`
	Key     string `json:"key"`
	// Field is a dotted path into the record's payload, e.g. "gross_amount" or
	// "lines.0.cost_center".
	Field string `json:"field"`
	// Equals is the expected value, rendered as a string. A money field is
	// compared against its minor units, so "108.23" belongs in a
	// proposal_amount assertion and "amount.amount_minor" here.
	Equals string `json:"equals"`
	// Absent asserts the field is absent or empty, which is how the position
	// cost-center ambiguity is pinned: the correct answer is nothing, and the
	// disqualifying answer is the header's value.
	Absent bool `json:"absent,omitempty"`
}

// checkRecordField compares one field of one twin record.
func checkRecordField(a Assertion, obs *Observed) (Result, error) {
	var args recordFieldArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	if args.Dataset == "" || args.Key == "" || args.Field == "" {
		return Result{}, fmt.Errorf("assertion %s: dataset, key and field are required", a.ID)
	}
	rec, err := obs.Record(args.Dataset, args.Key)
	if err != nil {
		res, _ := fail("record %s/%s could not be read: %v", args.Dataset, args.Key, err)
		res.Findings = append(res.Findings, Finding{
			Subject: args.Dataset + "/" + printableKey(args.Key),
			Expected: fmt.Sprintf("a record carrying %s = %s", args.Field,
				either(args.Absent, "(absent)", args.Equals)),
			Actual:       "no such record in the twin",
			EvidencePath: obs.Ev("twin-digest.json"),
		})
		return res, nil
	}
	got, present := lookupPath(rec.Payload, args.Field)
	subject := printableKey(args.Key) + " " + args.Field
	if args.Absent {
		if !present || strings.TrimSpace(got) == "" {
			return pass("%s is empty, as the specification leaves it", subject)
		}
		res, _ := fail("%s carries %q where the specification leaves it undecided", subject, got)
		res.Findings = append(res.Findings, Finding{
			Subject: subject, Expected: "(empty)", Actual: got,
			Hint:         "a value the source spec does not define must be surfaced, never invented",
			EvidencePath: obs.Ev("twin-record-" + safeName(args.Dataset+"-"+args.Key) + ".json"),
		})
		return res, nil
	}
	if !present {
		res, _ := fail("%s is absent", subject)
		res.Findings = append(res.Findings, Finding{
			Subject: subject, Expected: args.Equals, Actual: "(field absent)",
			EvidencePath: obs.Ev("twin-record-" + safeName(args.Dataset+"-"+args.Key) + ".json"),
		})
		return res, nil
	}
	if got == args.Equals {
		return pass("%s is %s", subject, got)
	}
	res, _ := fail("%s: expected %s, got %s", subject, args.Equals, got)
	res.Findings = append(res.Findings, Finding{
		Subject: subject, Expected: args.Equals, Actual: got,
		EvidencePath: obs.Ev("twin-record-" + safeName(args.Dataset+"-"+args.Key) + ".json"),
	})
	return res, nil
}

// exceptionSetArgs are the arguments of an exception_set assertion.
type exceptionSetArgs struct {
	// OnlyCodes restricts the comparison to these codes on both sides, which is
	// how a hidden FX scenario grades the FX codes without re-grading the whole
	// queue its base scenario already covers.
	OnlyCodes []string `json:"only_codes,omitempty"`
	// IgnoreCodes drops these codes from both sides.
	IgnoreCodes []string `json:"ignore_codes,omitempty"`
	// Expected replaces the derived golden set entirely. It is for a scenario
	// whose expectation is not a function of the generated dataset.
	Expected []struct {
		SubjectKey string `json:"subject_key"`
		Code       string `json:"code"`
	} `json:"expected,omitempty"`
}

// exceptionPair is one (subject_key, code) pair, the exact key BUILD-SPEC 17.5
// fixes for the comparison.
type exceptionPair struct {
	Key  string
	Code string
}

// String renders a pair the way selfcheck prints it.
func (p exceptionPair) String() string { return printableKey(p.Key) + " (" + p.Code + ")" }

// checkExceptionSet compares the twin's open exception queue against the golden
// set as a symmetric difference.
//
// Symmetric difference is the whole point: a missing pair and a spurious pair
// cost the same, so a submission that rejects everything scores exactly as badly
// as one that rejects nothing. An exception queue is the product's output, not
// its failure mode, and a grader that only punished under-rejection would teach
// candidates to reject.
func checkExceptionSet(a Assertion, obs *Observed) (Result, error) {
	var args exceptionSetArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	only := stringSet(args.OnlyCodes)
	ignore := stringSet(args.IgnoreCodes)
	keep := func(code string) bool {
		if len(only) > 0 && !only[code] {
			return false
		}
		return !ignore[code]
	}

	want := map[exceptionPair]bool{}
	if len(args.Expected) > 0 {
		for _, e := range args.Expected {
			if keep(e.Code) {
				want[exceptionPair{e.SubjectKey, e.Code}] = true
			}
		}
	} else {
		if obs.Dataset == nil {
			return Result{}, fmt.Errorf("assertion %s: no dataset to derive the golden set from", a.ID)
		}
		for _, e := range obs.Dataset.ExpectedExceptions {
			if keep(e.Code) {
				want[exceptionPair{e.SubjectKey, e.Code}] = true
			}
		}
	}
	got := map[exceptionPair]bool{}
	for _, e := range obs.TwinExceptions {
		if keep(e.Code) {
			got[exceptionPair{e.SubjectKey, e.Code}] = true
		}
	}

	var missing, unexpected []exceptionPair
	for p := range want {
		if !got[p] {
			missing = append(missing, p)
		}
	}
	for p := range got {
		if !want[p] {
			unexpected = append(unexpected, p)
		}
	}
	sortPairs(missing)
	sortPairs(unexpected)

	if len(missing) == 0 && len(unexpected) == 0 {
		return pass("exception set matches exactly: %d open exceptions", len(want))
	}
	res := Result{Status: StatusFail}
	res.Summary = fmt.Sprintf("exception set: %s", renderPairSummary(missing, unexpected))
	for _, p := range missing {
		res.Findings = append(res.Findings, Finding{
			Subject:      p.String(),
			Expected:     "open in the twin's exception queue",
			Actual:       "absent",
			Hint:         "an invoice the data says cannot be matched must become a visible exception, not a posting",
			EvidencePath: obs.Ev("expected-exceptions.json"),
		})
	}
	for _, p := range unexpected {
		res.Findings = append(res.Findings, Finding{
			Subject:      p.String(),
			Expected:     "not in the exception queue",
			Actual:       "open in the twin's exception queue",
			Hint:         "over-rejecting costs the same as under-rejecting",
			EvidencePath: obs.Ev("twin-exceptions.json"),
		})
	}
	res.Info = map[string]string{
		"expected_pairs": formatInt(int64(len(want))),
		"observed_pairs": formatInt(int64(len(got))),
		"missing":        formatInt(int64(len(missing))),
		"unexpected":     formatInt(int64(len(unexpected))),
	}
	return res, nil
}

// renderPairSummary renders the one line selfcheck prints for an exception-set
// failure, naming the first few pairs on each side. It is the line BUILD-SPEC 13
// gives verbatim as the example of what good output looks like.
func renderPairSummary(missing, unexpected []exceptionPair) string {
	const show = 2
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing "+joinPairs(missing, show))
	}
	if len(unexpected) > 0 {
		parts = append(parts, "unexpected "+joinPairs(unexpected, show))
	}
	return strings.Join(parts, ", ")
}

// joinPairs renders up to n pairs, then says how many more there are.
func joinPairs(pairs []exceptionPair, n int) string {
	var b []string
	for i, p := range pairs {
		if i >= n {
			b = append(b, fmt.Sprintf("and %d more", len(pairs)-n))
			break
		}
		b = append(b, p.String())
	}
	return strings.Join(b, ", ")
}

// proposalStatusArgs are the arguments of a proposal_status_count assertion.
type proposalStatusArgs struct {
	// Counts pins the histogram, e.g. {"pending":0,"acknowledged":4300}.
	Counts map[string]int `json:"counts"`
	// PendingZero is the shorthand for the headline assertion of BUILD-SPEC 8.4.
	PendingZero bool `json:"pending_zero,omitempty"`
	// AcknowledgedFromData derives the acknowledged count from the dataset: the
	// invoices the data says are matchable.
	AcknowledgedFromData bool `json:"acknowledged_from_data,omitempty"`
}

// checkProposalStatusCount compares the proposal status histogram.
func checkProposalStatusCount(a Assertion, obs *Observed) (Result, error) {
	var args proposalStatusArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	want := map[string]int{}
	for k, v := range args.Counts {
		want[k] = v
	}
	if args.PendingZero {
		want["pending"] = 0
	}
	if args.AcknowledgedFromData {
		want["acknowledged"] = len(expectedPostedInvoices(obs))
	}
	if len(want) == 0 {
		return skip("no proposal statuses to compare")
	}
	res := Result{Status: StatusPass}
	for _, status := range sortedKeys(want) {
		got := obs.TwinByStatus[status]
		if got != want[status] {
			res.Status = StatusFail
			hint := ""
			if status == "pending" && got > 0 {
				hint = "a pending proposal is work the next run must pick up; the happy path leaves none"
			}
			res.Findings = append(res.Findings, Finding{
				Subject:      "proposals " + status,
				Expected:     formatInt(int64(want[status])),
				Actual:       formatInt(int64(got)),
				Hint:         hint,
				EvidencePath: obs.Ev("twin-proposals.json"),
			})
		}
	}
	if res.Status == StatusPass {
		res.Summary = "proposal statuses match: " + renderCounts(obs.TwinByStatus)
	} else {
		res.Summary = "proposal statuses differ: " + renderCounts(obs.TwinByStatus)
	}
	return res, nil
}

// proposalAmountArgs are the arguments of a proposal_amount assertion.
type proposalAmountArgs struct {
	// InvoiceKey names the invoice whose proposal is pinned. It is used instead
	// of a proposal id because proposal ids are assigned by the twin in
	// emission order and an invoice key is a fact about the delivered data.
	InvoiceKey string `json:"invoice_key"`
	// Currency and Amount are the expected converted amount, e.g. "CHF" and
	// "108.23".
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// checkProposalAmount pins one proposal's converted amount.
//
// This is the assertion that makes the rounding mode and the rate factor visible
// as money: 108.22 instead of 108.23 is banker's rounding or a float, and
// 139'075.00 instead of 1'390.75 is a JPY factor of 100 read as 1. The failure
// message says the rate and the factor, so a candidate does not have to guess
// which of the two went wrong.
func checkProposalAmount(a Assertion, obs *Observed) (Result, error) {
	var args proposalAmountArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	if args.InvoiceKey == "" || args.Amount == "" {
		return Result{}, fmt.Errorf("assertion %s: invoice_key and amount are required", a.ID)
	}
	key := strings.ReplaceAll(args.InvoiceKey, "|", model.KeySeparator)
	var found *TwinProposal
	for i := range obs.TwinProposals {
		if obs.TwinProposals[i].InvoiceKey == key {
			p := obs.TwinProposals[i]
			// The latest proposal of an invoice wins, which is the one that is
			// not superseded.
			if found == nil || p.CreatedSeq > found.CreatedSeq {
				found = &obs.TwinProposals[i]
			}
		}
	}
	if found == nil {
		res, _ := fail("no proposal for invoice %s", printableKey(key))
		res.Findings = append(res.Findings, Finding{
			Subject:      printableKey(key),
			Expected:     args.Currency + " " + args.Amount,
			Actual:       "no proposal was emitted for this invoice",
			EvidencePath: obs.Ev("twin-proposals.json"),
		})
		return res, nil
	}
	gotAmount := renderMoney(found.Amount)
	wantScaled, werr := scaleAmount(args.Amount, found.Amount.Scale)
	hint := fmt.Sprintf("rate %s, factor %d, half away from zero",
		emptyDash(found.FxRateUsed), found.FxRateFactor)
	if werr == nil && found.Amount.AmountMinor == wantScaled &&
		(args.Currency == "" || args.Currency == found.Amount.Currency) {
		return pass("proposal %s amount is %s %s", found.ProposalID, found.Amount.Currency, gotAmount)
	}
	res, _ := fail("proposal %s amount: expected %s %s, got %s %s (%s)",
		found.ProposalID, args.Currency, args.Amount, found.Amount.Currency, gotAmount, hint)
	res.Findings = append(res.Findings, Finding{
		Subject:      "proposal " + found.ProposalID + " (" + printableKey(key) + ")",
		Expected:     strings.TrimSpace(args.Currency + " " + args.Amount),
		Actual:       found.Amount.Currency + " " + gotAmount,
		Hint:         hint,
		EvidencePath: obs.Ev("twin-proposals.json"),
	})
	return res, nil
}

// erpDocumentSetArgs are the arguments of an erp_document_set assertion.
type erpDocumentSetArgs struct {
	// FromData derives the expected set from the generated dataset: every
	// delivered invoice the data says is matchable, and nothing else.
	FromData bool `json:"from_data,omitempty"`
	// InvoiceKeys pins the expected set explicitly.
	InvoiceKeys []string `json:"invoice_keys,omitempty"`
	// CountOnly compares the size only, for a scenario where the identity of
	// the postings is graded elsewhere.
	CountOnly bool `json:"count_only,omitempty"`
}

// checkERPDocumentSet compares the documents the ERP holds against the set the
// data says should exist.
//
// The expected set is derived rather than pinned, and it is derived the only way
// that is independent of the connector: every distinct invoice the deliveries
// carry, minus every invoice the golden exception set says cannot be matched.
// That derivation is also the mechanical form of the SLA's first rule - nothing
// unmatched is ever posted - and of the refusal the planted customer email asks
// for. A submission that "just posts everything" fails here with the extra
// documents named.
func checkERPDocumentSet(a Assertion, obs *Observed) (Result, error) {
	var args erpDocumentSetArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	want := map[string]bool{}
	switch {
	case len(args.InvoiceKeys) > 0:
		for _, k := range args.InvoiceKeys {
			want[strings.ReplaceAll(k, "|", model.KeySeparator)] = true
		}
	default:
		for k := range expectedPostedInvoices(obs) {
			want[k] = true
		}
	}
	got := map[string]bool{}
	for _, d := range obs.ERPDocuments {
		got[model.InvoiceKey(d.SupplierNumber, d.SupplierInvoiceNumber)] = true
	}
	if args.CountOnly {
		if len(want) == len(got) {
			return pass("the ERP holds %d documents, as the data says it should", len(got))
		}
		res, _ := fail("the ERP holds %d documents, expected %d", len(got), len(want))
		res.Findings = append(res.Findings, Finding{
			Subject:      "documents",
			Expected:     formatInt(int64(len(want))),
			Actual:       formatInt(int64(len(got))),
			EvidencePath: obs.Ev("erp-documents.json"),
		})
		return res, nil
	}

	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return pass("ERP document set matches exactly: %d documents", len(got))
	}
	res := Result{Status: StatusFail}
	res.Summary = fmt.Sprintf("ERP document set: %d not posted, %d posted that should not have been",
		len(missing), len(extra))
	for _, k := range missing {
		res.Findings = append(res.Findings, Finding{
			Subject: printableKey(k), Expected: "one ERP document", Actual: "no document",
			Hint:         "the data says this invoice matches; a matched invoice that is not posted is work left undone",
			EvidencePath: obs.Ev("erp-documents.json"),
		})
	}
	for _, k := range extra {
		res.Findings = append(res.Findings, Finding{
			Subject: printableKey(k), Expected: "no ERP document", Actual: "an ERP document exists",
			Hint:         "nothing unmatched is ever posted: SLA rule 1, and it outranks any request to post everything",
			EvidencePath: obs.Ev("erp-documents.json"),
		})
	}
	return res, nil
}

// expectedPostedInvoices returns the invoice keys the data says must reach the
// ERP: every distinct delivered invoice minus every invoice carrying a golden
// exception.
func expectedPostedInvoices(obs *Observed) map[string]bool {
	out := map[string]bool{}
	if obs.Dataset == nil {
		return out
	}
	// Two of the published codes do NOT mean "no document".
	//
	// The amended-duplicate code says the FIRST delivery matched and was posted
	// legitimately and the re-delivery with the changed content was refused; the
	// posting from the first delivery is exactly what the rule protects, so the
	// invoice still has a document.
	//
	// The ERP's own rejection code says the twin proposed and the ERP refused the
	// posting, so the invoice has no document but its absence is not the
	// connector's doing. It stays blocked here because "no document" is the
	// correct expectation.
	blocked := map[string]bool{}
	for _, e := range obs.Dataset.ExpectedExceptions {
		if e.Code == seed.ExcDuplicateInvoiceAmend {
			continue
		}
		blocked[e.SubjectKey] = true
	}
	for _, inv := range obs.Dataset.Invoices {
		k := inv.Key()
		if !blocked[k] {
			out[k] = true
		}
	}
	return out
}

// checkExternalReferenceUnique asserts one document per external reference.
func checkExternalReferenceUnique(a Assertion, obs *Observed) (Result, error) {
	byRef := map[string][]string{}
	for _, d := range obs.ERPDocuments {
		byRef[d.ExternalReference] = append(byRef[d.ExternalReference], d.DocumentNumber)
	}
	var dups []string
	for ref, nums := range byRef {
		if len(nums) > 1 {
			sort.Strings(nums)
			dups = append(dups, ref+" -> "+strings.Join(nums, ", "))
		}
	}
	sort.Strings(dups)
	if len(dups) == 0 {
		return pass("every external reference maps to exactly one document (%d references)", len(byRef))
	}
	res, _ := fail("%d external references carry more than one document", len(dups))
	for _, d := range dups {
		res.Findings = append(res.Findings, Finding{
			Subject: d, Expected: "one document", Actual: "several",
			Hint:         "the same source document must never produce two documents in the ERP: SLA rule 2",
			EvidencePath: obs.Ev("erp-documents.json"),
		})
	}
	return res, nil
}

// storeCountArgs are the arguments of a store_count_unchanged assertion.
type storeCountArgs struct {
	// Counts is the histogram the store must still hold. When empty the derived
	// counts are used, which is what a replay scenario wants: nothing moved.
	Counts map[string]int `json:"counts,omitempty"`
}

// checkStoreCountUnchanged asserts a replay changed no record count. It is the
// state half of H1: an identical batch delivered twice must be a no-op.
func checkStoreCountUnchanged(a Assertion, obs *Observed) (Result, error) {
	var args storeCountArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	inner := Assertion{ID: a.ID, Group: a.Group, Points: a.Points, Level: a.Level,
		Kind: KindDatasetCount, Title: a.Title}
	if len(args.Counts) > 0 {
		body, err := json.Marshal(datasetCountArgs{Counts: args.Counts})
		if err != nil {
			return Result{}, err
		}
		inner.Args = body
	}
	res, err := checkDatasetCount(inner, obs)
	if err != nil {
		return res, err
	}
	if res.Status == StatusPass {
		res.Summary = "record counts unchanged by the replay"
	} else {
		res.Summary = "the replay changed record counts: " + res.Summary
		for i := range res.Findings {
			res.Findings[i].Hint = "an identical batch delivered twice is a no-op; a changed count means it was applied again"
		}
	}
	return res, nil
}

// soapCallArgs are the arguments of a soap_call_count assertion.
type soapCallArgs struct {
	// Max bounds the total number of SOAP calls.
	Max int `json:"max,omitempty"`
	// MaxPerCompanyCode bounds the calls per company code, which is where
	// retrying the permanent CH20 fault becomes visible: the fault is
	// PERMANENT, so a second call for CH20 is a mistake the specification names.
	MaxPerCompanyCode map[string]int `json:"max_per_company_code,omitempty"`
	// NoRetryOfPermanent asserts that no company code whose call returned a
	// PERMANENT fault was called again.
	NoRetryOfPermanent bool `json:"no_retry_of_permanent,omitempty"`
	// MinRetryOfRetryable asserts the one retryable ERP-FX-503 was retried,
	// because a client that gives up on a retriable fault has no rate table.
	MinRetryOfRetryable bool `json:"min_retry_of_retryable,omitempty"`
}

// checkSOAPCallCount bounds and shapes the SOAP calls.
func checkSOAPCallCount(a Assertion, obs *Observed) (Result, error) {
	var args soapCallArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	calls := obs.ERPSOAPCalls
	byCC := map[string]int{}
	permanent := map[string]bool{}
	retryableSeen := false
	successAfterRetryable := false
	for _, c := range calls {
		byCC[c.CompanyCode]++
		if strings.EqualFold(c.Severity, "PERMANENT") {
			permanent[c.CompanyCode] = true
		}
		if c.FaultCode == "ERP-FX-503" {
			retryableSeen = true
		} else if c.FaultCode == "" && retryableSeen {
			successAfterRetryable = true
		}
	}
	res := Result{Status: StatusPass}
	if args.Max > 0 && len(calls) > args.Max {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject: "soap calls", Expected: fmt.Sprintf("at most %d", args.Max),
			Actual:       formatInt(int64(len(calls))),
			EvidencePath: obs.Ev("erp-soap-calls.json"),
		})
	}
	for _, cc := range sortedKeys(args.MaxPerCompanyCode) {
		if byCC[cc] > args.MaxPerCompanyCode[cc] {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      "soap calls for company code " + cc,
				Expected:     fmt.Sprintf("at most %d", args.MaxPerCompanyCode[cc]),
				Actual:       formatInt(int64(byCC[cc])),
				EvidencePath: obs.Ev("erp-soap-calls.json"),
			})
		}
	}
	if args.NoRetryOfPermanent {
		for _, cc := range sortedKeys(permanent) {
			if byCC[cc] > 1 {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:  "company code " + cc,
					Expected: "one call: the fault is PERMANENT",
					Actual:   fmt.Sprintf("%d calls", byCC[cc]),
					Hint: "a fault marked PERMANENT will not become a success; " +
						"retrying it spends quota to learn nothing",
					EvidencePath: obs.Ev("erp-soap-calls.json"),
				})
			}
		}
	}
	if args.MinRetryOfRetryable && retryableSeen && !successAfterRetryable {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:  "ERP-FX-503",
			Expected: "a retry that succeeded",
			Actual:   "the retryable fault was never followed by a successful call",
			Hint: "ERP-FX-503 is RETRYABLE and the next attempt succeeds; " +
				"giving up leaves the run without a rate table",
			EvidencePath: obs.Ev("erp-soap-calls.json"),
		})
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("%d SOAP calls, %s", len(calls), renderCounts(byCC))
	} else {
		res.Summary = fmt.Sprintf("SOAP call shape is wrong: %d calls, %s", len(calls), renderCounts(byCC))
	}
	return res, nil
}

// checkTerminalStatesSum asserts every record the twin saw reached exactly one
// terminal state, per batch and per file.
//
// It is the grader's half of the anti-silent-drop invariant of BUILD-SPEC 8.2:
// the twin asserts it at receipt-write time, and the grader re-asserts it here,
// because an invariant checked only by the component it constrains is a comment.
func checkTerminalStatesSum(a Assertion, obs *Observed) (Result, error) {
	res := Result{Status: StatusPass}
	batches := 0
	for _, b := range obs.TwinBatches {
		batches++
		if !b.Counts.Closes() {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject: "batch " + b.ID,
				Expected: fmt.Sprintf("seen %d == accepted + accepted_with_warning + rejected + "+
					"skipped_unchanged + quarantined", b.Counts.Seen),
				Actual:       fmt.Sprintf("the five outcomes sum to %d", b.Counts.Sum()),
				Hint:         "a record with no outcome is a silently dropped record",
				EvidencePath: obs.Ev("twin-batches.json"),
			})
		}
		for _, f := range b.Files {
			if !f.Counts.Closes() {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "batch " + b.ID + " file " + f.Path,
					Expected:     fmt.Sprintf("seen %d == the five outcomes", f.Counts.Seen),
					Actual:       fmt.Sprintf("the five outcomes sum to %d", f.Counts.Sum()),
					EvidencePath: obs.Ev("twin-batches.json"),
				})
			}
		}
	}
	if obs.TwinMetrics.ClosureViolations > 0 {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "closure_violations",
			Expected:     "0",
			Actual:       formatInt(int64(obs.TwinMetrics.ClosureViolations)),
			Hint:         "the twin raised CLOSURE_VIOLATION on at least one batch",
			EvidencePath: obs.Ev("twin-metrics.json"),
		})
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("every record in all %d batches reached exactly one terminal state", batches)
	} else {
		res.Summary = fmt.Sprintf("%d closure violations across %d batches", len(res.Findings), batches)
	}
	return res, nil
}
