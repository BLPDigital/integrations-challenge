package grader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	register(KindReportSchema, checkReportSchema)
	register(KindReportValue, checkReportValue)
	register(KindReconciliation, checkReconciliation)
	register(KindCounterCrosscheck, checkCounterCrosscheck)
	register(KindExitCodeIn, checkExitCodeIn)
	register(KindNoStderrPanic, checkNoStderrPanic)
	register(KindNoSecretInOutput, checkNoSecretInOutput)
	register(KindNoPaymentDataInOutput, checkNoPaymentDataInOutput)
	register(KindWatermarkAdvanced, checkWatermarkAdvanced)
	register(KindFullLoadFalse, checkFullLoadFalse)
	register(KindNoFullReloadOnSecondRun, checkFullLoadFalse)
	register(KindNoFilesLeftIn, checkNoFilesLeftIn)
	register(KindReceiptClosure, checkReceiptClosure)
	register(KindAuditChainSample, checkAuditRoundtrip)
	register(KindAuditRoundtrip, checkAuditRoundtrip)
	register(KindRuntimeWallMsMax, checkRuntimeWallMsMax)
	register(KindPeakRSSMax, checkPeakRSSMax)
}

// The assertion kinds that read the connector's own artifacts and the run's
// directory tree.
const (
	// KindReportSchema checks that the three report files exist and carry the
	// fixed schema of BUILD-SPEC 9.
	KindReportSchema = "report_schema"
	// KindReportValue pins one field of run.json.
	KindReportValue = "report_value"
	// KindReconciliation checks the per-run identities of BUILD-SPEC 17.6.
	KindReconciliation = "reconciliation"
	// KindCounterCrosscheck compares run.json's counters against the servers'
	// own metrics. It is information only, per the arbitration recorded in
	// BUILD-SPEC 17.6.6.
	KindCounterCrosscheck = "counter_crosscheck"
	// KindExitCodeIn constrains the connector's exit codes.
	KindExitCodeIn = "exit_code_in"
	// KindNoStderrPanic asserts no unhandled crash reached stderr.
	KindNoStderrPanic = "no_stderr_panic"
	// KindNoSecretInOutput asserts no credential appears verbatim in a
	// candidate artifact.
	KindNoSecretInOutput = "no_secret_in_output"
	// KindNoPaymentDataInOutput asserts no full IBAN appears verbatim.
	KindNoPaymentDataInOutput = "no_payment_data_in_output"
	// KindWatermarkAdvanced asserts the connector's watermark moved.
	KindWatermarkAdvanced = "watermark_advanced"
	// KindFullLoadFalse asserts no batch declared a full reload.
	KindFullLoadFalse = "full_load_false"
	// KindNoFullReloadOnSecondRun is 16.5's name for the same check.
	KindNoFullReloadOnSecondRun = "no_full_reload_on_second_run"
	// KindNoFilesLeftIn asserts a directory is empty at the end of a run.
	KindNoFilesLeftIn = "no_files_left_in"
	// KindReceiptClosure asserts every batch got a complete receipt.
	KindReceiptClosure = "receipt_closure"
	// KindAuditChainSample and KindAuditRoundtrip walk sampled audit chains in
	// both directions.
	KindAuditChainSample = "audit_chain_sample"
	KindAuditRoundtrip   = "audit_roundtrip"
	// KindRuntimeWallMsMax and KindPeakRSSMax report real elapsed time and peak
	// memory. Neither is scored in core; see the checks for why.
	KindRuntimeWallMsMax = "runtime_wall_ms_max"
	KindPeakRSSMax       = "peak_rss_max"
)

// reportSchemaArgs are the arguments of a report_schema assertion.
type reportSchemaArgs struct {
	// RequireRunJSONFields lists run.json fields that must be present. Empty
	// means the full contract list.
	RequireRunJSONFields []string `json:"require_run_json_fields,omitempty"`
	// AllowExtraColumns permits columns beyond the fixed header. Off by
	// default: postings.csv is an artifact a finance team reads, and an extra
	// column is a column somebody has to ask about.
	AllowExtraColumns bool `json:"allow_extra_columns,omitempty"`
}

// runJSONContractFields is the field list of BUILD-SPEC 9's run.json.
var runJSONContractFields = []string{
	"run_id", "connector_version", "exit_code", "channel_used", "formats_used",
	"erp_requests", "erp_429s", "erp_5xx_retried", "twin_requests", "twin_429s",
	"soap_calls", "batches_written", "records_read", "records_ingested",
	"records_rejected", "records_skipped_unchanged", "proposals_read",
	"proposals_posted", "proposals_rejected", "proposals_pending",
	"duplicate_document_attempts", "watermark_before", "watermark_after",
	"full_load", "fx_snapshot_token", "fx_correlation_id", "fx_truncated",
}

// checkReportSchema checks the three report files exist and carry the fixed
// schema.
func checkReportSchema(a Assertion, obs *Observed) (Result, error) {
	var args reportSchemaArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	rp := obs.Reports
	res := Result{Status: StatusPass}
	add := func(f Finding) {
		res.Status = StatusFail
		res.Findings = append(res.Findings, f)
	}
	if !rp.RunJSONPresent {
		add(Finding{Subject: "run.json", Expected: "the file exists",
			Actual:       "absent",
			Hint:         "the three report files are part of the task, not extra credit",
			EvidencePath: rp.RunJSONPath})
	} else if rp.RunJSONError != "" {
		add(Finding{Subject: "run.json", Expected: "a JSON object",
			Actual: rp.RunJSONError, EvidencePath: rp.RunJSONPath})
	} else {
		want := args.RequireRunJSONFields
		if len(want) == 0 {
			want = runJSONContractFields
		}
		var missing []string
		for _, f := range want {
			if _, ok := rp.RunRaw[f]; !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			add(Finding{Subject: "run.json fields",
				Expected:     fmt.Sprintf("%d contract fields", len(want)),
				Actual:       "missing " + strings.Join(missing, ", "),
				EvidencePath: rp.RunJSONPath})
		}
	}
	checkHeader := func(name, path string, present bool, parseErr string, got, want []string) {
		if !present {
			add(Finding{Subject: name, Expected: "the file exists", Actual: "absent",
				EvidencePath: path})
			return
		}
		if parseErr != "" {
			add(Finding{Subject: name, Expected: "a readable CSV", Actual: parseErr,
				EvidencePath: path})
			return
		}
		if diff := headerDiff(got, want, args.AllowExtraColumns); diff != "" {
			add(Finding{Subject: name + " header",
				Expected:     strings.Join(want, ","),
				Actual:       strings.Join(got, ","),
				Hint:         diff,
				EvidencePath: path})
		}
	}
	checkHeader("postings.csv", rp.PostingsPath, rp.PostingsPresent, rp.PostingsError,
		rp.PostingHeader, PostingsHeader)
	checkHeader("exceptions.csv", rp.ExceptionsPath, rp.ExceptionsPresent, rp.ExceptionsError,
		rp.ExceptionHeader, ExceptionsHeader)

	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("all three report files present and well-formed (%d postings, %d exceptions)",
			len(rp.Postings), len(rp.Exceptions))
	} else {
		res.Summary = fmt.Sprintf("%d report-schema problems", len(res.Findings))
	}
	return res, nil
}

// headerDiff describes how a CSV header departs from the contract, "" when it
// does not.
func headerDiff(got, want []string, allowExtra bool) string {
	wantSet := stringSet(want)
	gotSet := stringSet(got)
	var missing, extra []string
	for _, w := range want {
		if !gotSet[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !wantSet[g] {
			extra = append(extra, g)
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 && !allowExtra {
		parts = append(parts, "unexpected "+strings.Join(extra, ", "))
	}
	if len(parts) == 0 && len(missing) == 0 {
		// Same columns, possibly reordered. The order is part of the contract.
		if len(got) >= len(want) {
			for i, w := range want {
				if i < len(got) && got[i] != w {
					parts = append(parts, fmt.Sprintf("column %d is %q, the contract puts %q there",
						i+1, got[i], w))
					break
				}
			}
		}
	}
	return strings.Join(parts, "; ")
}

// reportValueArgs are the arguments of a report_value assertion.
type reportValueArgs struct {
	// Field is the run.json field.
	Field string `json:"field"`
	// Equals is the expected value rendered as a string. A JSON string, number
	// and boolean all compare as their rendered text, so "0", "false" and
	// "run_s1_ab12cd34" all work.
	Equals string `json:"equals,omitempty"`
	// EqualsRunID compares against the run id the harness handed the connector,
	// which is the check that catches a connector inventing its own.
	EqualsRunID bool `json:"equals_run_id,omitempty"`
	// Max and Min bound a numeric field.
	Max *int64 `json:"max,omitempty"`
	Min *int64 `json:"min,omitempty"`
}

// checkReportValue pins one field of run.json.
func checkReportValue(a Assertion, obs *Observed) (Result, error) {
	var args reportValueArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	if args.Field == "" {
		return Result{}, fmt.Errorf("assertion %s: field is required", a.ID)
	}
	rp := obs.Reports
	if !rp.RunJSONPresent {
		res, _ := fail("run.json is absent, so %s cannot be read", args.Field)
		res.Findings = append(res.Findings, Finding{
			Subject: "run.json", Expected: "the file exists", Actual: "absent",
			EvidencePath: rp.RunJSONPath})
		return res, nil
	}
	raw, ok := rp.RunRaw[args.Field]
	if !ok {
		res, _ := fail("run.json has no field %s", args.Field)
		res.Findings = append(res.Findings, Finding{
			Subject: "run.json " + args.Field, Expected: "the field is present",
			Actual: "absent", EvidencePath: rp.RunJSONPath})
		return res, nil
	}
	got := renderRawJSON(raw)
	want := args.Equals
	if args.EqualsRunID {
		want = obs.RunID
	}
	res := Result{Status: StatusPass}
	if want != "" && got != want {
		res.Status = StatusFail
		hint := ""
		if args.EqualsRunID {
			hint = "the run id is supplied by the harness and must never be invented by the connector"
		}
		res.Findings = append(res.Findings, Finding{
			Subject: "run.json " + args.Field, Expected: want, Actual: got,
			Hint: hint, EvidencePath: rp.RunJSONPath})
	}
	if args.Max != nil || args.Min != nil {
		n, err := parseIntish(got)
		if err != nil {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject: "run.json " + args.Field, Expected: "a number", Actual: got,
				EvidencePath: rp.RunJSONPath})
		} else {
			if args.Max != nil && n > *args.Max {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "run.json " + args.Field,
					Expected:     fmt.Sprintf("at most %d", *args.Max),
					Actual:       got,
					EvidencePath: rp.RunJSONPath})
			}
			if args.Min != nil && n < *args.Min {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "run.json " + args.Field,
					Expected:     fmt.Sprintf("at least %d", *args.Min),
					Actual:       got,
					EvidencePath: rp.RunJSONPath})
			}
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("run.json %s is %s", args.Field, got)
	} else {
		res.Summary = fmt.Sprintf("run.json %s is %s", args.Field, got)
	}
	return res, nil
}

// renderRawJSON renders a raw JSON value as its text: a string without its
// quotes, everything else verbatim.
func renderRawJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
	}
	return s
}

// parseIntish parses an integer out of a rendered JSON value.
func parseIntish(s string) (int64, error) {
	var n json.Number = json.Number(strings.TrimSpace(s))
	return n.Int64()
}

// checkReconciliation checks the per-run identities of BUILD-SPEC 17.6.
//
// The identities are stated per run and are therefore true on a delta run as
// well, which is the whole reason 17.6 rewrote them: an identity that only holds
// on a cold load is an identity a candidate cannot use.
func checkReconciliation(a Assertion, obs *Observed) (Result, error) {
	rp := obs.Reports
	if !rp.RunJSONPresent || rp.RunJSONError != "" {
		res, _ := fail("run.json is absent or unreadable, so the run cannot be reconciled")
		res.Findings = append(res.Findings, Finding{
			Subject: "run.json", Expected: "a readable JSON object",
			Actual:       either(rp.RunJSONPresent, rp.RunJSONError, "absent"),
			EvidencePath: rp.RunJSONPath})
		return res, nil
	}
	run := rp.Run
	res := Result{Status: StatusPass}
	add := func(f Finding) {
		res.Status = StatusFail
		res.Findings = append(res.Findings, f)
	}

	// 17.6.1 records_read == ingested + rejected + skipped_unchanged
	read, ok1 := i64(run.RecordsRead)
	ing, ok2 := i64(run.RecordsIngested)
	rej, ok3 := i64(run.RecordsRejected)
	skip, ok4 := i64(run.RecordsSkippedUnchanged)
	if ok1 && ok2 && ok3 && ok4 {
		if read != ing+rej+skip {
			add(Finding{
				Subject:      "records_read",
				Expected:     fmt.Sprintf("%d = %d ingested + %d rejected + %d skipped_unchanged", read, ing, rej, skip),
				Actual:       fmt.Sprintf("the three outcomes sum to %d", ing+rej+skip),
				Hint:         "a record that was read and has no outcome is a record nobody can account for",
				EvidencePath: rp.RunJSONPath})
		}
	} else {
		add(Finding{Subject: "records_read", Expected: "the four record counters are present",
			Actual: "at least one is absent", EvidencePath: rp.RunJSONPath})
	}

	// 17.6.2 proposals_read == posted + rejected + pending
	pread, ok1 := i64(run.ProposalsRead)
	pposted, ok2 := i64(run.ProposalsPosted)
	prej, ok3 := i64(run.ProposalsRejected)
	ppend, ok4 := i64(run.ProposalsPending)
	if ok1 && ok2 && ok3 && ok4 {
		if pread != pposted+prej+ppend {
			add(Finding{
				Subject:      "proposals_read",
				Expected:     fmt.Sprintf("%d = %d posted + %d rejected + %d pending", pread, pposted, prej, ppend),
				Actual:       fmt.Sprintf("the three outcomes sum to %d", pposted+prej+ppend),
				EvidencePath: rp.RunJSONPath})
		}
	} else {
		add(Finding{Subject: "proposals_read", Expected: "the four proposal counters are present",
			Actual: "at least one is absent", EvidencePath: rp.RunJSONPath})
	}

	// 17.6.4 every postings.csv row resolves to one twin invoice and one ERP
	// document number.
	docByNumber := map[string]bool{}
	for _, d := range obs.ERPDocuments {
		docByNumber[d.DocumentNumber] = true
	}
	unresolved := 0
	for _, p := range rp.Postings {
		if !strings.EqualFold(p.ERPStatus, "posted") {
			continue
		}
		if p.ERPDocumentNumber == "" || !docByNumber[p.ERPDocumentNumber] {
			unresolved++
			if unresolved <= 5 {
				add(Finding{
					Subject:      fmt.Sprintf("postings.csv line %d (%s)", p.Line, p.ProposalID),
					Expected:     "an ERP document number the ERP holds",
					Actual:       either(p.ERPDocumentNumber == "", "(empty)", p.ERPDocumentNumber),
					Hint:         "a posted row that does not resolve to a document is a broken audit chain",
					EvidencePath: rp.PostingsPath})
			}
		}
	}
	if unresolved > 5 {
		res.Status = StatusFail
	}

	// 17.6.5 every blocking twin exception appears in exceptions.csv and vice
	// versa. Rows with stage "info" are explicitly allowed and ignored, per
	// 17.5: warnings are not exceptions.
	twinPairs := map[exceptionPair]bool{}
	for _, e := range obs.TwinExceptions {
		twinPairs[exceptionPair{e.SubjectKey, e.Code}] = true
	}
	csvPairs := map[exceptionPair]bool{}
	for _, e := range rp.Exceptions {
		if strings.EqualFold(e.Stage, "info") {
			continue
		}
		key := strings.ReplaceAll(e.SubjectKey, "/", "\x1f")
		csvPairs[exceptionPair{key, e.Code}] = true
		csvPairs[exceptionPair{e.SubjectKey, e.Code}] = true
	}
	missingInCSV := 0
	for p := range twinPairs {
		if !csvPairs[p] {
			missingInCSV++
			if missingInCSV <= 5 {
				add(Finding{
					Subject:      p.String(),
					Expected:     "a row in exceptions.csv",
					Actual:       "absent from exceptions.csv",
					Hint:         "the twin opened this exception in this run; the report is what a human reads",
					EvidencePath: rp.ExceptionsPath})
			}
		}
	}
	if missingInCSV > 5 {
		res.Status = StatusFail
	}

	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("the run reconciles: %d records read, %d proposals read, "+
			"%d postings resolve, %d exceptions reported", read, pread, len(rp.Postings), len(rp.Exceptions))
	} else {
		res.Summary = fmt.Sprintf("%d reconciliation failures (%d unresolved postings, "+
			"%d twin exceptions absent from exceptions.csv)", len(res.Findings), unresolved, missingInCSV)
	}
	return res, nil
}

// counterCrosscheckArgs are the arguments of a counter_crosscheck assertion.
type counterCrosscheckArgs struct {
	// Tolerance is the permitted absolute difference. BUILD-SPEC 17.6.6 fixes it
	// at 2: a connector may count an auth refresh differently than the server
	// does without being wrong.
	Tolerance int64 `json:"tolerance,omitempty"`
}

// checkCounterCrosscheck compares run.json's counters against the servers' own.
//
// It is level info, and that is a deliberate reading of a contradiction in the
// specification. BUILD-SPEC 13 lists "a write-up that contradicts the server
// counters" among the automatic disqualifiers; 17.6.6 says a disagreement is
// "reported as a finding when they do not, worth no points, per the
// arbitration". Section 17 overrides section 13 by its own opening sentence, so
// the disagreement is reported and scores nothing, and the flag it raises is
// what a reviewer reads before the interview. The disqualifier that survives is
// the human-judged one about the decision log.
func checkCounterCrosscheck(a Assertion, obs *Observed) (Result, error) {
	var args counterCrosscheckArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	tol := args.Tolerance
	if tol <= 0 {
		tol = 2
	}
	rp := obs.Reports
	if !rp.RunJSONPresent {
		return skip("run.json is absent, so there is nothing to cross-check")
	}
	// In a multi-invocation scenario the surviving run.json is the LAST phase's,
	// while the servers' counters are cumulative over every phase, so the two
	// are not comparable and a difference here would be a false finding rather
	// than a candidate's telemetry lying. The comparison is skipped with that
	// reason instead of reported.
	if len(obs.Connector) > 1 {
		return skip("the scenario ran %d connector invocations, so run.json covers the last phase "+
			"while the servers' counters cover all of them; the cross-check needs a single-phase run",
			len(obs.Connector))
	}
	type pair struct {
		name     string
		reported *int64
		actual   int64
		evidence string
	}
	pairs := []pair{
		{"erp_requests", rp.Run.ERPRequests, obs.ERPMetrics.RequestsTotal, obs.Ev("erp-metrics.json")},
		{"erp_429s", rp.Run.ERP429s, obs.ERPMetrics.RateLimited, obs.Ev("erp-metrics.json")},
		{"erp_5xx_retried", rp.Run.ERP5xxRetried, obs.ERPMetrics.FiveXXRetried, obs.Ev("erp-metrics.json")},
		{"twin_requests", rp.Run.TwinRequests, obs.TwinMetrics.RequestsTotal, obs.Ev("twin-metrics.json")},
		{"twin_429s", rp.Run.Twin429s, obs.TwinMetrics.RateLimited, obs.Ev("twin-metrics.json")},
		{"soap_calls", rp.Run.SOAPCalls, int64(obs.ERPMetrics.SOAPCalls), obs.Ev("erp-soap-calls.json")},
		{"duplicate_document_attempts", rp.Run.DuplicateDocAttempts,
			obs.ERPMetrics.DuplicateDocumentAttempts, obs.Ev("erp-metrics.json")},
	}
	res := Result{Status: StatusPass}
	res.Info = map[string]string{}
	for _, p := range pairs {
		got, ok := i64(p.reported)
		if !ok {
			continue
		}
		delta := got - p.actual
		if delta < 0 {
			delta = -delta
		}
		res.Info[p.name] = fmt.Sprintf("reported %d, server %d", got, p.actual)
		if delta > tol {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      p.name,
				Expected:     fmt.Sprintf("%d, the server's own count, within %d", p.actual, tol),
				Actual:       fmt.Sprintf("%d in run.json", got),
				Hint:         "the servers' counters are the truth; a self-report that disagrees is a finding for the debrief",
				EvidencePath: p.evidence})
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("run.json agrees with both servers' counters within %d", tol)
	} else {
		res.Summary = fmt.Sprintf("run.json disagrees with the servers on %d counters (reported, not scored)",
			len(res.Findings))
	}
	return res, nil
}

// exitCodeArgs are the arguments of an exit_code_in assertion.
type exitCodeArgs struct {
	// Codes are the permitted exit codes. Empty means the contract's 0 and 2.
	Codes []int `json:"codes,omitempty"`
	// Phase restricts the check to one invocation. Empty checks every
	// invocation the harness did not signal on purpose.
	Phase string `json:"phase,omitempty"`
}

// checkExitCodeIn constrains the connector's exit codes.
//
// Exit code 2 - completed with business exceptions - is a success. It is in the
// default set because an exception-driven product that exits non-zero when it
// finds an exception has misunderstood what it is for, and a grader that treated
// 2 as a failure would teach exactly that misunderstanding.
func checkExitCodeIn(a Assertion, obs *Observed) (Result, error) {
	var args exitCodeArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	allowed := args.Codes
	if len(allowed) == 0 {
		allowed = []int{0, 2}
	}
	allowedSet := map[int]bool{}
	for _, c := range allowed {
		allowedSet[c] = true
	}
	var checked int
	res := Result{Status: StatusPass}
	for _, run := range obs.Connector {
		if args.Phase != "" && run.Phase != args.Phase {
			continue
		}
		if run.Killed {
			// The harness signalled this invocation on purpose. A deliberate
			// crash has no exit code worth grading.
			continue
		}
		checked++
		if run.TimedOut {
			// A safety timeout is a harness event, not a score. It is skipped
			// here and reported through HarnessErrors.
			continue
		}
		if !allowedSet[run.ExitCode] {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      "connector phase " + run.Phase,
				Expected:     "exit code in " + renderInts(allowed),
				Actual:       fmt.Sprintf("%d%s", run.ExitCode, either(run.Signal != "", " ("+run.Signal+")", "")),
				Hint:         "0 is clean, 2 is completed with business exceptions and is not a failure, 3 is a hard failure",
				EvidencePath: run.StderrPath})
		}
	}
	if checked == 0 {
		return skip("no connector invocation to check the exit code of")
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("every one of %d invocations exited in %s", checked, renderInts(allowed))
	} else {
		res.Summary = fmt.Sprintf("%d of %d invocations exited outside %s", len(res.Findings), checked, renderInts(allowed))
	}
	return res, nil
}

// renderInts renders an int slice as a set.
func renderInts(in []int) string {
	parts := make([]string, len(in))
	for i, n := range in {
		parts[i] = fmt.Sprint(n)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// checkNoStderrPanic asserts no unhandled crash reached stderr.
func checkNoStderrPanic(a Assertion, obs *Observed) (Result, error) {
	res := Result{Status: StatusPass}
	for _, run := range obs.Connector {
		if run.Killed {
			// A SIGKILL from the harness produces no stack trace, and a
			// SIGTERM handler that prints one is not a crash.
			continue
		}
		if run.StderrPanic {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      "connector phase " + run.Phase,
				Expected:     "no unhandled crash on stderr",
				Actual:       "stderr carries a panic, traceback or stack trace",
				Hint:         "an unhandled crash is a different failure from a reported one; the report never happened",
				EvidencePath: run.StderrPath})
		}
	}
	if res.Status == StatusPass {
		res.Summary = "no unhandled crash on any invocation's stderr"
	} else {
		res.Summary = fmt.Sprintf("%d invocations crashed", len(res.Findings))
	}
	return res, nil
}

// candidateArtifacts returns the files a data-minimization assertion scans: the
// connector's reports, its state directory, its stdout and stderr, and the
// batches it wrote into the twin's inbox.
//
// The twin's own store is not scanned. It legitimately holds the supplier master
// including the IBAN, because that is the record the ERP delivered; the rule is
// about what leaves the customer boundary through a candidate artifact.
func candidateArtifacts(obs *Observed) []string {
	var out []string
	for _, dir := range []string{obs.Dirs.Reports, obs.Dirs.State} {
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			out = append(out, p)
			return nil
		})
	}
	for _, run := range obs.Connector {
		out = append(out, run.StdoutPath, run.StderrPath)
	}
	sort.Strings(out)
	return out
}

// scanArtifactsFor looks for any needle in any candidate artifact and returns the
// hits as (path, needle) pairs.
//
// Matching is on the full value only, per BUILD-SPEC 17.7. No fragments, no
// entropy heuristics: a masked form like CH93****2957 must pass, because masking
// is the behavior the rule prefers, and a scored assertion that fires on correct
// work is worse than no assertion at all.
func scanArtifactsFor(obs *Observed, needles []string) map[string][]string {
	hits := map[string][]string{}
	if len(needles) == 0 {
		return hits
	}
	for _, path := range candidateArtifacts(obs) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(raw)
		for _, n := range needles {
			if n == "" {
				continue
			}
			if strings.Contains(text, n) {
				hits[path] = append(hits[path], n)
			}
		}
	}
	return hits
}

// checkNoSecretInOutput asserts no credential appears verbatim in a candidate
// artifact.
func checkNoSecretInOutput(a Assertion, obs *Observed) (Result, error) {
	hits := scanArtifactsFor(obs, obs.Secrets)
	if len(hits) == 0 {
		return pass("no credential appears in any of the %d candidate artifacts",
			len(candidateArtifacts(obs)))
	}
	res, _ := fail("a credential appears verbatim in %d candidate artifacts", len(hits))
	for _, path := range sortedKeys(hits) {
		res.Findings = append(res.Findings, Finding{
			Subject:      filepath.Base(path),
			Expected:     "no credential in a report, a state file or on stdout",
			Actual:       fmt.Sprintf("%d credential value(s) present verbatim", len(hits[path])),
			Hint:         "masking is acceptable and preferred over omission; the full value never is",
			EvidencePath: path})
	}
	return res, nil
}

// checkNoPaymentDataInOutput asserts no full seeded IBAN appears verbatim.
func checkNoPaymentDataInOutput(a Assertion, obs *Observed) (Result, error) {
	if obs.Dataset == nil {
		return skip("no dataset to read the seeded IBANs from")
	}
	var ibans []string
	for i := range obs.Dataset.Suppliers {
		if s := strings.TrimSpace(obs.Dataset.Suppliers[i].IBAN); len(s) >= 10 {
			ibans = append(ibans, s)
		}
	}
	if len(ibans) == 0 {
		return skip("the dataset carries no IBANs")
	}
	hits := scanArtifactsFor(obs, ibans)
	if len(hits) == 0 {
		return pass("none of the %d seeded IBANs appears in a candidate artifact", len(ibans))
	}
	res, _ := fail("a full supplier IBAN appears verbatim in %d candidate artifacts", len(hits))
	for _, path := range sortedKeys(hits) {
		res.Findings = append(res.Findings, Finding{
			Subject:      filepath.Base(path),
			Expected:     "no full IBAN; a masked form such as CH93****2957 is preferred",
			Actual:       fmt.Sprintf("%d full IBAN(s) present verbatim", len(hits[path])),
			Hint:         "payment data does not leave the customer boundary: SLA rule 5",
			EvidencePath: path})
	}
	return res, nil
}

// watermarkArgs are the arguments of a watermark_advanced assertion.
type watermarkArgs struct {
	// Datasets are the datasets whose watermark must have moved. Empty means
	// every dataset run.json names.
	Datasets []string `json:"datasets,omitempty"`
	// AtLeast is the value each watermark must reach. Zero derives it from the
	// dataset's own max_change_seq, which is the watermark a complete pull
	// leaves behind.
	AtLeast int64 `json:"at_least,omitempty"`
}

// checkWatermarkAdvanced asserts the connector's watermark moved.
//
// The watermark is read from run.json, because it is the connector's own
// persisted position and there is nowhere else it could honestly be read from.
// The value it must reach comes from the dataset's max_change_seq, which every
// list response carries in its envelope, so a connector never has to inspect
// records to learn it.
func checkWatermarkAdvanced(a Assertion, obs *Observed) (Result, error) {
	var args watermarkArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	rp := obs.Reports
	if !rp.RunJSONPresent {
		res, _ := fail("run.json is absent, so no watermark can be read")
		res.Findings = append(res.Findings, Finding{Subject: "run.json",
			Expected: "the file exists", Actual: "absent", EvidencePath: rp.RunJSONPath})
		return res, nil
	}
	after := rp.Run.WatermarkAfter
	if len(after) == 0 {
		res, _ := fail("run.json carries no watermark_after")
		res.Findings = append(res.Findings, Finding{Subject: "watermark_after",
			Expected:     "a per-dataset watermark",
			Actual:       "absent or empty",
			Hint:         "without a persisted watermark the next run is a full reload",
			EvidencePath: rp.RunJSONPath})
		return res, nil
	}
	want := args.AtLeast
	if want == 0 && obs.Dataset != nil {
		want = obs.Dataset.MaxChangeSeq
	}
	datasets := args.Datasets
	if len(datasets) == 0 {
		datasets = sortedKeys(after)
	}
	res := Result{Status: StatusPass}
	before := rp.Run.WatermarkBefore
	for _, ds := range datasets {
		got, err := parseIntish(after[ds])
		if err != nil {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject: "watermark_after." + ds, Expected: "an integer change sequence",
				Actual: emptyDash(after[ds]), EvidencePath: rp.RunJSONPath})
			continue
		}
		if want > 0 && got < want {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      "watermark_after." + ds,
				Expected:     fmt.Sprintf("at least %d, the dataset's max_change_seq", want),
				Actual:       formatInt(got),
				Hint:         "every list response carries max_change_seq so the watermark needs no record inspection",
				EvidencePath: rp.RunJSONPath})
			continue
		}
		if bstr, ok := before[ds]; ok {
			if b, err := parseIntish(bstr); err == nil && got < b {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "watermark_after." + ds,
					Expected:     fmt.Sprintf("at least watermark_before, %d", b),
					Actual:       formatInt(got),
					Hint:         "a watermark that went backwards re-reads work that already landed",
					EvidencePath: rp.RunJSONPath})
			}
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("the watermark advanced in all %d datasets (%s)",
			len(datasets), renderCounts(toInt64Map(after)))
	} else {
		res.Summary = fmt.Sprintf("the watermark did not advance in %d datasets", len(res.Findings))
	}
	return res, nil
}

// toInt64Map renders a string watermark map as numbers where it can, for a
// readable summary.
func toInt64Map(in map[string]string) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		if n, err := parseIntish(v); err == nil {
			out[k] = n
		}
	}
	return out
}

// checkFullLoadFalse asserts no batch of the run declared a full reload.
//
// It reads the twin's run report rather than run.json, because the twin's
// full_load flag is derived from the manifests it actually received and a
// connector cannot mis-report it. run.json's own full_load is checked too, and a
// disagreement between the two is worth naming: it is the shape of the
// contradiction the debrief asks about.
func checkFullLoadFalse(a Assertion, obs *Observed) (Result, error) {
	res := Result{Status: StatusPass}
	if obs.TwinRun == nil {
		return skip("the twin has no run report for %s, so no batch reached it", obs.RunID)
	}
	if obs.TwinRun.FullLoad {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "twin run " + obs.RunID,
			Expected:     "full_load false: a delta run reloads nothing",
			Actual:       "at least one manifest declared full_load or mode replace_dataset",
			Hint:         "a full reload every night is the failure mode the watermark exists to prevent",
			EvidencePath: obs.Ev("twin-run.json")})
	}
	if fl := obs.Reports.Run.FullLoad; fl != nil && *fl {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "run.json full_load",
			Expected:     "false",
			Actual:       "true",
			EvidencePath: obs.Reports.RunJSONPath})
	}
	if res.Status == StatusPass {
		res.Summary = "no batch of the run declared a full reload"
	} else {
		res.Summary = "the run declared a full reload"
	}
	return res, nil
}

// noFilesLeftArgs are the arguments of a no_files_left_in assertion.
type noFilesLeftArgs struct {
	// Dir is one of "twin_incoming", "twin_processing", "erp_export",
	// "state" or "reports".
	Dir string `json:"dir"`
	// AllowNames are file or directory names that may remain.
	AllowNames []string `json:"allow_names,omitempty"`
}

// checkNoFilesLeftIn asserts a directory is empty at the end of a run.
//
// The interesting one is twin_incoming: the twin moves a batch to processing and
// then to processed or rejected, so a directory left behind in incoming means a
// batch the scanner never accepted, which is a delivery the candidate believes
// landed and which did not.
func checkNoFilesLeftIn(a Assertion, obs *Observed) (Result, error) {
	var args noFilesLeftArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	var dir string
	switch args.Dir {
	case "twin_incoming":
		dir = obs.Dirs.TwinDrop
	case "twin_processing":
		dir = filepath.Join(obs.Dirs.TwinData, "inbox", "processing")
	case "erp_export":
		dir = obs.Dirs.ERPExport
	case "state":
		dir = obs.Dirs.State
	case "reports":
		dir = obs.Dirs.Reports
	default:
		return Result{}, fmt.Errorf("assertion %s: unknown dir %q", a.ID, args.Dir)
	}
	allow := stringSet(args.AllowNames)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return pass("%s does not exist, so nothing was left in it", args.Dir)
	}
	if err != nil {
		return Result{}, fmt.Errorf("assertion %s: %w", a.ID, err)
	}
	var left []string
	for _, e := range entries {
		if allow[e.Name()] {
			continue
		}
		left = append(left, e.Name())
	}
	sort.Strings(left)
	if len(left) == 0 {
		return pass("%s is empty at the end of the run", args.Dir)
	}
	res, _ := fail("%s still holds %d entries: %s", args.Dir, len(left), strings.Join(left, ", "))
	for _, name := range left {
		res.Findings = append(res.Findings, Finding{
			Subject:      args.Dir + "/" + name,
			Expected:     "the directory is empty at the end of the run",
			Actual:       "the entry is still there",
			Hint:         "a batch left in incoming is a delivery the twin never accepted",
			EvidencePath: filepath.Join(dir, name)})
	}
	return res, nil
}

// receiptClosureArgs are the arguments of a receipt_closure assertion.
type receiptClosureArgs struct {
	// RequireDone asserts the DONE sentinel exists for every receipt of the
	// file channel. DONE is the only completion signal, written last by rename;
	// polling for receipt.json is a documented mistake.
	RequireDone bool `json:"require_done,omitempty"`
	// MinBatches is the number of batches the run must have produced. Zero
	// accepts any number, including none, which is what a scenario that grades
	// the REST channel wants.
	MinBatches int `json:"min_batches,omitempty"`
}

// checkReceiptClosure asserts every batch got a complete receipt.
func checkReceiptClosure(a Assertion, obs *Observed) (Result, error) {
	var args receiptClosureArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	res := Result{Status: StatusPass}
	if len(obs.TwinBatches) < args.MinBatches {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "batches",
			Expected:     fmt.Sprintf("at least %d", args.MinBatches),
			Actual:       formatInt(int64(len(obs.TwinBatches))),
			Hint:         "no batch reached the twin, so there is nothing to be traceable",
			EvidencePath: obs.Ev("twin-batches.json")})
	}
	receiptsRoot := filepath.Join(obs.Dirs.TwinData, "inbox", "receipts")
	for _, b := range obs.TwinBatches {
		if !b.Counts.Closes() {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      "batch " + b.ID,
				Expected:     "the receipt's outcome counts close",
				Actual:       fmt.Sprintf("seen %d, outcomes %d", b.Counts.Seen, b.Counts.Sum()),
				EvidencePath: obs.Ev("twin-batches.json")})
		}
		if b.Channel != "file" {
			continue
		}
		dir := filepath.Join(receiptsRoot, b.ID)
		for _, name := range []string{"receipt.json", "records.csv", "rejects.csv"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "batch " + b.ID + " receipt",
					Expected:     name + " exists",
					Actual:       "absent",
					EvidencePath: dir})
			}
		}
		if args.RequireDone {
			if _, err := os.Stat(filepath.Join(dir, "DONE")); err != nil {
				res.Status = StatusFail
				res.Findings = append(res.Findings, Finding{
					Subject:      "batch " + b.ID + " receipt",
					Expected:     "the DONE sentinel exists",
					Actual:       "absent",
					Hint:         "DONE is written last, by rename, and is the only completion signal",
					EvidencePath: dir})
			}
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("all %d batches carry a complete, closing receipt", len(obs.TwinBatches))
	} else {
		res.Summary = fmt.Sprintf("%d receipt problems across %d batches", len(res.Findings), len(obs.TwinBatches))
	}
	return res, nil
}

// auditArgs are the arguments of an audit_roundtrip assertion.
type auditArgs struct {
	// Samples is how many postings to walk. BUILD-SPEC 13 fixes it at 25.
	Samples int `json:"samples,omitempty"`
	// BothDirections walks forwards from the source line and backwards from the
	// ERP document number.
	BothDirections bool `json:"both_directions,omitempty"`
}

// checkAuditRoundtrip walks sampled audit chains in both directions.
//
// The sample is taken deterministically: postings.csv is read in document-number
// order and every nth row is taken, so the same submission is sampled the same
// way on every run and two runs of one submission cannot score differently. Both
// directions matter because the two questions a finance team asks are "where did
// this posting come from" and "what happened to this invoice", and an interface
// that answers only one of them is half an interface.
func checkAuditRoundtrip(a Assertion, obs *Observed) (Result, error) {
	var args auditArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	samples := args.Samples
	if samples <= 0 {
		samples = 25
	}
	rows := append([]PostingRow(nil), obs.Reports.Postings...)
	posted := rows[:0]
	for _, r := range rows {
		if strings.EqualFold(r.ERPStatus, "posted") && r.ERPDocumentNumber != "" {
			posted = append(posted, r)
		}
	}
	if len(posted) == 0 {
		res, _ := fail("postings.csv carries no posted row, so no chain can be walked")
		res.Findings = append(res.Findings, Finding{
			Subject:      "postings.csv",
			Expected:     "at least one posted row carrying an ERP document number",
			Actual:       fmt.Sprintf("%d rows, none posted with a document number", len(obs.Reports.Postings)),
			EvidencePath: obs.Reports.PostingsPath})
		return res, nil
	}
	sort.Slice(posted, func(i, j int) bool { return posted[i].ERPDocumentNumber < posted[j].ERPDocumentNumber })
	step := len(posted) / samples
	if step < 1 {
		step = 1
	}
	var picked []PostingRow
	for i := 0; i < len(posted) && len(picked) < samples; i += step {
		picked = append(picked, posted[i])
	}

	docs := map[string]ERPDocument{}
	for _, d := range obs.ERPDocuments {
		docs[d.DocumentNumber] = d
	}
	res := Result{Status: StatusPass}
	ok := 0
	for _, row := range picked {
		problems := auditProblems(obs, row, docs, args.BothDirections)
		if len(problems) == 0 {
			ok++
			continue
		}
		res.Status = StatusFail
		for _, p := range problems {
			res.Findings = append(res.Findings, Finding{
				Subject:      fmt.Sprintf("%s (%s)", row.ERPDocumentNumber, row.ProposalID),
				Expected:     p.expected,
				Actual:       p.actual,
				Hint:         p.hint,
				EvidencePath: obs.Reports.PostingsPath})
		}
	}
	res.Info = map[string]string{
		"sampled":  formatInt(int64(len(picked))),
		"resolved": formatInt(int64(ok)),
		"posted":   formatInt(int64(len(posted))),
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("all %d sampled postings resolve in both directions and reconcile to the cent", len(picked))
	} else {
		res.Summary = fmt.Sprintf("%d of %d sampled postings do not resolve", len(picked)-ok, len(picked))
	}
	return res, nil
}

// auditProblem is one broken link in a chain.
type auditProblem struct{ expected, actual, hint string }

// auditProblems walks one posting row's chain and returns what did not hold.
func auditProblems(obs *Observed, row PostingRow, docs map[string]ERPDocument, both bool) []auditProblem {
	var out []auditProblem
	doc, ok := docs[row.ERPDocumentNumber]
	if !ok {
		return []auditProblem{{
			expected: "the ERP holds this document number",
			actual:   "the ERP does not",
			hint:     "postings.csv is the artifact a finance team is handed; a number nobody holds is worse than none",
		}}
	}
	// Forwards: the row must name where the record came from.
	if row.SourceBatchID == "" || row.SourceFileOrChunk == "" || row.SourceLineOrOrdinal == "" {
		out = append(out, auditProblem{
			expected: "source batch, file or chunk, and line or ordinal",
			actual: fmt.Sprintf("batch %q file %q line %q",
				row.SourceBatchID, row.SourceFileOrChunk, row.SourceLineOrOrdinal),
			hint: "tracing a document back to the line in the delivery file is a 2024 audit requirement",
		})
	}
	if row.IdempotencyKey == "" {
		out = append(out, auditProblem{
			expected: "the idempotency key the posting was made under",
			actual:   "empty",
		})
	}
	// Backwards: the ERP document number must resolve through the twin's audit
	// chain to the same invoice, and the amount must reconcile to the cent.
	if both {
		chain, err := obs.Audit(row.ERPDocumentNumber)
		if err != nil {
			out = append(out, auditProblem{
				expected: "the twin resolves the ERP document number to an invoice",
				actual:   "the twin's audit chain does not know this document number",
				hint:     "the twin learns a document number from the ack; an unacknowledged posting has no chain",
			})
		} else {
			wantKey := row.SupplierNumber + "\x1f" + row.SupplierInvoiceNumber
			if chain.InvoiceKey != wantKey && row.SupplierNumber != "" {
				out = append(out, auditProblem{
					expected: "the chain resolves to invoice " + printableKey(wantKey),
					actual:   "it resolves to " + printableKey(chain.InvoiceKey),
				})
			}
			if len(chain.Sources) == 0 {
				out = append(out, auditProblem{
					expected: "at least one source with a content address",
					actual:   "the chain carries no source",
					hint:     "the far end of the chain is the delivered bytes, addressed by sha256",
				})
			}
			// Amounts must reconcile to the cent between the proposal the twin
			// holds and the document the ERP booked.
			for _, p := range chain.Proposals {
				if p.Ack == nil || p.Ack.ExternalDocumentNumber != row.ERPDocumentNumber {
					continue
				}
				want, err := scaleAmount(doc.GrossAmount, p.Amount.Scale)
				if err == nil && want != p.Amount.AmountMinor {
					out = append(out, auditProblem{
						expected: fmt.Sprintf("the ERP's gross amount %s equals the proposal's %s",
							doc.GrossAmount, renderMoney(p.Amount)),
						actual: "they differ",
						hint:   "the amounts must reconcile to the cent in both directions",
					})
				}
			}
		}
	}
	return out
}

// wallArgs are the arguments of a runtime_wall_ms_max assertion.
type wallArgs struct {
	Max int64 `json:"max,omitempty"`
}

// checkRuntimeWallMsMax reports real elapsed time and never scores it.
//
// BUILD-SPEC 17.4 is explicit: no wall-clock timeout is scored. Under a virtual
// clock a fast connector and a slow one do the same amount of work, so elapsed
// time measures the machine and the language runtime, not the submission. The
// number is reported because it is useful to a reviewer, and the assertion
// refuses to fail on it because failing would grade the machine.
func checkRuntimeWallMsMax(a Assertion, obs *Observed) (Result, error) {
	var args wallArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	var total int64
	info := map[string]string{}
	for _, run := range obs.Connector {
		total += run.WallMs
		info["phase_"+run.Phase+"_wall_ms"] = formatInt(run.WallMs)
	}
	info["total_wall_ms"] = formatInt(total)
	res := Result{Status: StatusPass, Info: info}
	res.Summary = fmt.Sprintf("connector wall time %d ms across %d invocations (reported, never scored)",
		total, len(obs.Connector))
	if args.Max > 0 && total > args.Max {
		res.Summary = fmt.Sprintf("connector wall time %d ms exceeds the %d ms note; "+
			"reported, never scored, because a virtual clock makes elapsed time a property of the machine",
			total, args.Max)
	}
	return res, nil
}

// rssArgs are the arguments of a peak_rss_max assertion.
type rssArgs struct {
	MaxKB int64 `json:"max_kb,omitempty"`
}

// checkPeakRSSMax reports peak memory.
//
// It scores only in the stretch scale scenario, which is worth zero points
// anyway. BUILD-SPEC 17.4: a JVM or .NET baseline must never cost a candidate a
// point, so an assertion at level scored here would be a language penalty
// wearing an engineering hat.
func checkPeakRSSMax(a Assertion, obs *Observed) (Result, error) {
	var args rssArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	var peak int64
	info := map[string]string{}
	for _, run := range obs.Connector {
		if run.MaxRSSKB > peak {
			peak = run.MaxRSSKB
		}
		info["phase_"+run.Phase+"_max_rss_kb"] = formatInt(run.MaxRSSKB)
	}
	info["peak_rss_kb"] = formatInt(peak)
	res := Result{Info: info, Status: StatusPass}
	if args.MaxKB > 0 && peak > args.MaxKB {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "peak rss",
			Expected:     fmt.Sprintf("at most %d KiB", args.MaxKB),
			Actual:       fmt.Sprintf("%d KiB", peak),
			Hint:         "the memory cap exists only in the stretch scale scenario, which is worth zero points",
			EvidencePath: obs.Ev("connector-runs.json")})
		res.Summary = fmt.Sprintf("peak rss %d KiB exceeds the %d KiB cap", peak, args.MaxKB)
		return res, nil
	}
	res.Summary = fmt.Sprintf("peak rss %d KiB", peak)
	return res, nil
}
