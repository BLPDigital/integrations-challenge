// Command smoke is a smoke test, not a solution.
//
// It satisfies the CLI contract, authenticates against both servers, reads one
// paginated page from the ERP, calls the one SOAP operation (and retries it once,
// because the first delivery of a rate table answers a retryable fault over
// HTTP 200), drops one tiny batch into the twin through the atomic protocol,
// writes a schema-valid run.json plus both CSV headers, and exits 3 with a
// message saying what it is.
//
// Its whole purpose is to prove your plumbing before you invest in semantics.
// Read it once, then throw it away. The Python sibling in ../python does the same
// thing in the same order, so you can compare them line by line.
//
//	go run ./examples/connector-smoke/go run \
//	    --run-id smoke --erp-base-url http://127.0.0.1:8082 \
//	    --twin-base-url http://127.0.0.1:8081 \
//	    --erp-export-dir var/erp/export --twin-drop-dir var/miniblp/inbox \
//	    --state-dir /tmp/smoke-state --report-dir /tmp/smoke-reports
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The exit codes of the connector contract. 3 is "failed": a smoke test has not
// done the job, and it says so rather than reporting a clean run.
const (
	exitClean      = 0
	exitExceptions = 2
	exitFailure    = 3
)

func main() { os.Exit(run()) }

// opts are the flags of the published contract. Unknown ones are tolerated so
// that a harness which learns a new flag does not kill the smoke test.
type opts struct {
	runID, erpBase, twinBase       string
	erpExportDir, twinDropDir      string
	stateDir, reportDir, configArg string
	noChaos                        bool
}

func run() int {
	var o opts
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.StringVar(&o.runID, "run-id", "", "run id assigned by the caller")
	fs.StringVar(&o.erpBase, "erp-base-url", "", "ERP base URL")
	fs.StringVar(&o.twinBase, "twin-base-url", "", "twin base URL")
	fs.StringVar(&o.erpExportDir, "erp-export-dir", "", "the ERP's legacy export drop")
	fs.StringVar(&o.twinDropDir, "twin-drop-dir", "", "the twin's inbox root")
	fs.StringVar(&o.stateDir, "state-dir", "", "where the watermark lives between runs")
	fs.StringVar(&o.reportDir, "report-dir", "", "where the three report files go")
	fs.StringVar(&o.configArg, "config", "", "optional config file")
	fs.BoolVar(&o.noChaos, "no-chaos", false, "informational: the services were started without injection")
	// The subcommand is positional and is always "run"; absorb every leading
	// occurrence of it so that "run run" cannot break the parse.
	args := os.Args[1:]
	for len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	for _, f := range []struct {
		name, val string
	}{{"run-id", o.runID}, {"erp-base-url", o.erpBase}, {"twin-base-url", o.twinBase},
		{"twin-drop-dir", o.twinDropDir}, {"report-dir", o.reportDir}} {
		if f.val == "" {
			fmt.Fprintf(os.Stderr, "smoke: --%s is required\n", f.name)
			return exitFailure
		}
	}
	if err := smoke(o); err != nil {
		fmt.Fprintln(os.Stderr, "smoke:", err)
		return exitFailure
	}
	fmt.Println()
	fmt.Println("This is a SMOKE TEST, not a solution: it pulled one page, called SOAP " +
		"once, dropped one batch, and posted nothing.")
	return exitFailure
}

// client is one HTTP client with a timeout. A connector without a timeout hangs
// forever on the first stalled connection, which is the cheapest way to lose a
// grading run.
var client = &http.Client{Timeout: 30 * time.Second}

// token asks a base URL for a bearer token with the client-credentials body both
// services publish.
func token(base, path, id, secret string) (string, int64, error) {
	var out struct {
		AccessToken           string `json:"access_token"`
		ExpiresAfterRequests  int64  `json:"expires_after_requests"`
		ExpiresAfterSimulated int64  `json:"expires_after_vms"`
	}
	body, _ := json.Marshal(map[string]string{"client_id": id, "client_secret": secret})
	resp, err := client.Post(base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, fmt.Errorf("%s%s: status %d: %s", base, path, resp.StatusCode, raw)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.AccessToken, out.ExpiresAfterRequests, nil
}

// page is the shape every paginated ERP collection answers with.
//
// MaxChangeSeq is a json.Number and not a string or an int64 on purpose: the ERP
// serves the watermark as a JSON number, while the run report carries watermarks
// as strings, and json.Number accepts either without deciding. Do not widen this
// to float64 - a change sequence is an identifier, and 2^53 is closer than it
// looks.
type page struct {
	Returned     int64       `json:"returned"`
	HasMore      bool        `json:"has_more"`
	MaxChangeSeq json.Number `json:"max_change_seq"`
	NextCursor   string      `json:"next_cursor"`
}

// getPage reads one page. It asks for the largest legal page on purpose: the
// clock advances by what a request costs, and the bucket refills from that same
// clock, so a 250-record page earns more tokens than it spends and a
// record-at-a-time client starves itself.
func getPage(base, path, bearer string) (page, error) {
	var p page
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return p, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return p, fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, raw)
	}
	return p, json.NewDecoder(resp.Body).Decode(&p)
}

// soapEnvelope is the one request of the SOAP channel, with the WS-Security
// UsernameToken header the service requires.
const soapEnvelope = `<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">%s</wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>%s</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH10</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>
`

// soapCall posts the envelope once and returns the HTTP status and the body.
//
// The body is NOT decoded as UTF-8: the response declares ISO-8859-1 in its
// prolog and sends no charset in the HTTP header, and the provider name carries
// an umlaut. Every high byte maps one-to-one from ISO-8859-1 to a rune, so the
// conversion is this loop and needs no library.
func soapCall(base, user, password, runID string) (int, string, error) {
	body := fmt.Sprintf(soapEnvelope, user, password, runID)
	req, err := http.NewRequest(http.MethodPost, base+"/soap/FinancialReferenceDataService",
		strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	// The quotes are part of the value. Without them the service answers HTTP 200
	// with a soap:Fault, which is the whole lesson of this channel: the status is
	// never your success signal.
	req.Header.Set("SOAPAction", `"urn:blp-erp:finref:1.0/GetExchangeRateTable"`)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	var sb strings.Builder
	for _, b := range raw {
		sb.WriteRune(rune(b))
	}
	return resp.StatusCode, sb.String(), nil
}

// env reads an environment variable with a fallback.
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func smoke(o opts) error {
	for _, dir := range []string{o.reportDir, o.stateDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	erpToken, _, err := token(o.erpBase, "/erp/v1/auth/token",
		env("ERP_CLIENT_ID", "blp-connector"), os.Getenv("ERP_CLIENT_SECRET"))
	if err != nil {
		return fmt.Errorf("erp token: %w", err)
	}
	sup, err := getPage(o.erpBase, "/erp/v1/suppliers?limit=250", erpToken)
	if err != nil {
		return err
	}
	fmt.Printf("suppliers page: %d records, has_more=%v, max_change_seq=%s\n",
		sup.Returned, sup.HasMore, sup.MaxChangeSeq.String())

	status, xml, err := soapCall(o.erpBase, os.Getenv("SOAP_USERNAME"), os.Getenv("SOAP_PASSWORD"), o.runID)
	if err != nil {
		return fmt.Errorf("soap: %w", err)
	}
	soapCalls := int64(1)
	faulted := strings.Contains(xml, "Fault")
	fmt.Printf("soap call 1: http %d, fault=%v\n", status, faulted)
	if faulted {
		// The first delivery of a company's rate table answers a RETRYABLE fault.
		// Retrying the SAME request succeeds; that is the published contract.
		status, xml, err = soapCall(o.erpBase, os.Getenv("SOAP_USERNAME"), os.Getenv("SOAP_PASSWORD"), o.runID)
		if err != nil {
			return fmt.Errorf("soap retry: %w", err)
		}
		soapCalls++
		fmt.Printf("soap call 2: http %d, fault=%v\n", status, strings.Contains(xml, "Fault"))
	}

	_, expiresAfter, err := token(o.twinBase, "/v1/auth/token",
		env("TWIN_CLIENT_ID", "blp-connector"), os.Getenv("TWIN_CLIENT_SECRET"))
	if err != nil {
		return fmt.Errorf("twin token: %w", err)
	}
	fmt.Printf("twin token acquired, expires after %d requests\n", expiresAfter)

	batch, err := publishBatch(o)
	if err != nil {
		return err
	}
	fmt.Printf("batch published: %s\n", batch)
	fmt.Printf("the twin's scanner is driven by the harness, so the receipt appears in %s once it runs\n",
		filepath.Join(o.twinDropDir, "receipts", batch))

	// One record was published, so one record is what the report accounts for.
	// records_read is not "rows the HTTP response contained": the published
	// invariant is records_read == ingested + rejected + skipped_unchanged, which
	// makes it "records this run took into the pipeline and can account for". The
	// 240 rows the page returned are printed above and deliberately dropped, so
	// counting them here would report 239 silent drops.
	return writeReports(o, 1, soapCalls)
}

// smokeCSV is one supplier record with CRLF endings, exactly as its manifest
// declares. The sha256 is taken over these bytes, so the two can never disagree.
const smokeCSV = "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\r\n" +
	"0000000417,Meier Praezision AG,CH,CHF,CH9300762011623852957,CHE-123.456.789 MWST,30,false,100417\r\n"

// publishBatch writes one batch through the atomic drop protocol: everything into
// incoming/.staging-<id>/, fsync, then a single rename. The scanner ignores every
// name starting with a dot, so a half-written batch is invisible rather than
// half-read.
func publishBatch(o opts) (string, error) {
	batch := "smoke-" + o.runID
	incoming := filepath.Join(o.twinDropDir, "incoming")
	staging := filepath.Join(incoming, ".staging-"+batch)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(smokeCSV))
	manifest := map[string]any{
		"manifest_version": "1", "batch_id": batch, "producer": "smoke/1.0",
		"run_id": o.runID, "tenant": "acme-ch", "source_system": "erp-prod",
		"mode": "upsert", "full_load": false, "on_error": "continue",
		"files": []any{map[string]any{
			"path": "suppliers.csv", "dataset": "supplier", "format": "csv",
			"profile": "blp-canonical-v1", "encoding": "UTF-8", "record_count": 1,
			"sha256": hex.EncodeToString(sum[:]),
			"csv": map[string]any{
				"delimiter": ",", "quote": "\"", "escape": "double", "header": true,
				"line_ending": "CRLF", "decimal_separator": ".", "thousands_separator": "",
				"date_format": "RFC3339", "null_token": "", "trim": "both",
			},
		}},
	}
	man, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	for name, content := range map[string][]byte{
		"suppliers.csv": []byte(smokeCSV),
		"manifest.json": append(man, '\n'),
	} {
		if err := writeSynced(filepath.Join(staging, name), content); err != nil {
			return "", err
		}
	}
	if err := syncDir(staging); err != nil {
		return "", err
	}
	if err := os.Rename(staging, filepath.Join(incoming, batch)); err != nil {
		return "", err
	}
	return batch, syncDir(incoming)
}

// writeSynced writes a file and flushes it to disk before returning. Without the
// fsync the rename can become visible before the bytes do, and the scanner then
// reads a manifest that names files which are not there yet.
func writeSynced(path string, content []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir flushes a directory entry, so the rename itself is durable.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeReports writes the three report files. run.json carries every field of
// the published schema, because a missing field is a report the grader cannot
// read, and the CSVs carry their fixed headers with no rows: this run posted
// nothing and says so.
//
// The counters here are this process's own tally and satisfy the two balance
// rules. The smoke does not attempt the reconciliation rules that compare the
// report against the servers' metrics; a real connector must.
func writeReports(o opts, recordsRead, soapCalls int64) error {
	run := map[string]any{
		"run_id": o.runID, "connector_version": "smoke-1.0", "exit_code": exitFailure,
		"channel_used": "file", "formats_used": []string{"csv"},
		"erp_requests": 2, "erp_429s": 0, "erp_5xx_retried": 0,
		"twin_requests": 1, "twin_429s": 0, "soap_calls": soapCalls,
		"batches_written": 1, "records_read": recordsRead,
		"records_ingested": recordsRead, "records_rejected": 0,
		"records_skipped_unchanged": 0,
		"proposals_read":            0, "proposals_posted": 0, "proposals_rejected": 0,
		"proposals_pending": 0, "duplicate_document_attempts": 0,
		"watermark_before": map[string]string{}, "watermark_after": map[string]string{},
		"full_load": false,
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(o.reportDir, "run.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	for name, header := range map[string]string{
		"postings.csv": "proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number," +
			"source_batch_id,source_file_or_chunk,source_line_or_ordinal,idempotency_key," +
			"erp_document_number,erp_status,http_status,attempts,idempotency_replay",
		"exceptions.csv": "subject_key,subject_type,stage,code,field,message,source_batch_id," +
			"source_file_or_chunk,source_line_or_ordinal",
	} {
		if err := os.WriteFile(filepath.Join(o.reportDir, name), []byte(header+"\n"), 0o644); err != nil {
			return err
		}
	}
	// exitClean and exitExceptions are the codes a real connector ends on. They
	// are referenced here so that the contract is visible in one place.
	_, _ = exitClean, exitExceptions
	return nil
}
