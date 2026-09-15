package miniblp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// The file channel's directory contract, from BUILD-SPEC 8.1. Only incoming is
// the connector's; the other four are the twin's, and the twin notices when
// something else writes into them.
const (
	// dirIncoming is the ONLY directory the connector may write.
	dirIncoming = "incoming"
	// dirProcessing holds a batch while the twin reads it.
	dirProcessing = "processing"
	// dirProcessed holds completed batches, files preserved.
	dirProcessed = "processed"
	// dirRejected holds wholesale-rejected batches, files preserved.
	dirRejected = "rejected"
	// dirReceipts holds one receipt directory per batch.
	dirReceipts = "receipts"
	// manifestName is the one mandatory file of a batch directory.
	manifestName = "manifest.json"
	// stagingPrefix is the publication protocol's staging name: the connector
	// writes incoming/.staging-<batch_id>/, fsyncs it and renames it to
	// incoming/<batch_id> in one atomic step. The scanner ignores it, along with
	// every other name that starts with a dot.
	stagingPrefix = ".staging-"
)

// Suffixes of a name still being written. A file or directory with one of them
// is invisible to the scanner, so a connector that cannot rename atomically has
// a documented second way to publish safely.
var inFlightSuffixes = []string{".tmp", ".part"}

// CodeUndeclaredFile is the warning a batch carries when its directory holds a
// data file the manifest does not name. It is a warning and never a reject: the
// manifest is authoritative about what to read, and an extra file is at worst a
// sender's leftover. It is reported so that a truncated manifest - the case
// where the extra file is data nobody read - is visible.
const CodeUndeclaredFile = "W_UNDECLARED_FILE"

// A ScanReport is the answer of POST /admin/v1/inbox/scan.
type ScanReport struct {
	// Scan is the value of the monotone scan counter this scan ran under. It is
	// never a timestamp: every provenance entry that names a scan names this
	// number, so a receipt can be correlated with a scan without a clock.
	Scan int64 `json:"scan"`
	// Batches are the batches this scan finished, in the order it processed
	// them, which is ascending directory name order.
	Batches []ScanBatch `json:"batches"`
	// Pending are the batch directories this scan saw and deliberately left
	// alone: a directory whose manifest has not appeared yet, seen for the
	// first time.
	Pending []string `json:"pending"`
	// ForeignWrites are the entries found in a twin-owned directory that the
	// twin did not create. Any of them fails the run.
	ForeignWrites []string `json:"foreign_writes"`
	// Proposals and Exceptions count what the matching engine produced from
	// this scan's batches.
	Proposals  int `json:"proposals_emitted"`
	Exceptions int `json:"exceptions_opened"`
	// Digest is the state digest after the scan, so a driver can assert
	// progress in one request.
	Digest string `json:"state_digest"`
}

// A ScanBatch is one batch's line in a scan report.
type ScanBatch struct {
	BatchID string   `json:"batch_id"`
	Status  string   `json:"status"`
	Codes   []string `json:"codes"`
	Counts  Counts   `json:"counts"`
	Replay  bool     `json:"replay"`
}

// adminScan serves POST /admin/v1/inbox/scan.
func (s *Server) adminScan(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	rep, err := s.Scan()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// Scan runs one inbox scan and returns what it did.
//
// Scanning is driven by POST /admin/v1/inbox/scan and by nothing else: there is
// no timer, no watcher and no goroutine, so a grading run's batch boundaries are
// exactly where the driver put them. The scan counter is monotone and is the
// only notion of "later" the file channel has, which is what makes the
// two-consecutive-scans rule of MANIFEST_MISSING decidable without a clock.
//
// Batches are processed in ascending directory name order. A batch that fails
// wholesale is moved to rejected/ and one that completes to processed/, in both
// cases with its files preserved and a receipt written.
func (s *Server) Scan() (ScanReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.scanCount++
	rep := ScanReport{
		Scan:          s.scanCount,
		Batches:       []ScanBatch{},
		Pending:       []string{},
		ForeignWrites: []string{},
	}

	if foreign := s.detectForeignWrites(); len(foreign) > 0 {
		rep.ForeignWrites = foreign
		run := s.run(s.cfg.RunID)
		run.Status = "failed"
		run.addCode(CodeForeignWriteDetected)
	}

	incoming := filepath.Join(s.cfg.InboxDir, dirIncoming)
	entries, err := os.ReadDir(incoming)
	if err != nil {
		return rep, fmt.Errorf("miniblp: reading %s: %w", incoming, err)
	}
	names := make([]string, 0, len(entries))
	dirs := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if skipName(name) {
			continue
		}
		if !e.IsDir() {
			// A batch is a directory. A loose file in incoming/ is the
			// connector's business and is left where it is.
			continue
		}
		names = append(names, name)
		dirs[name] = true
	}
	sort.Strings(names)

	// A directory that has disappeared cannot still be missing its manifest.
	for name := range s.noManifest {
		if !dirs[name] {
			delete(s.noManifest, name)
		}
	}

	for _, name := range names {
		b, pending, err := s.processBatch(name)
		if err != nil {
			return rep, err
		}
		if pending {
			rep.Pending = append(rep.Pending, name)
			continue
		}
		if b == nil {
			continue
		}
		rep.Batches = append(rep.Batches, ScanBatch{
			BatchID: b.ID, Status: b.Status, Codes: b.Codes,
			Counts: b.Counts, Replay: b.Status == BatchReplayed,
		})
		rep.Proposals += len(b.Proposals)
		rep.Exceptions += len(b.Exceptions)
	}

	digest, err := s.st.Digest()
	if err != nil {
		return rep, err
	}
	rep.Digest = digest
	return rep, nil
}

// skipName reports whether the scanner ignores a directory entry: anything
// starting with a dot, which covers the staging directory of the atomic drop
// protocol, and anything ending in .tmp or .part.
func skipName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return true
	}
	for _, suffix := range inFlightSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// processBatch processes one batch directory. It reports pending when the batch
// is not ready to be read yet: a first sighting with no manifest in it.
func (s *Server) processBatch(name string) (b *batchState, pending bool, err error) {
	dir := filepath.Join(s.cfg.InboxDir, dirIncoming, name)
	raw, readErr := os.ReadFile(filepath.Join(dir, manifestName))
	if readErr != nil {
		// The manifest is written last, so a batch directory without one is
		// either a publication in flight or a sender that forgot. One scan
		// cannot tell them apart; two can.
		s.noManifest[name]++
		if s.noManifest[name] < 2 {
			return nil, true, nil
		}
		delete(s.noManifest, name)
		b := newBatchState(manifest{BatchID: name}, "", ChannelFile, s.scanCount)
		b.manifest.normalize()
		b.addCode(CodeManifestMissing)
		b.Findings = append(b.Findings, Finding{Code: CodeManifestMissing,
			Message: "no manifest.json after two consecutive scans"})
		return b, false, s.rejectBatch(b, name)
	}
	delete(s.noManifest, name)

	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		b := newBatchState(manifest{BatchID: name}, "", ChannelFile, s.scanCount)
		b.manifest.normalize()
		b.addCode(CodeManifestInvalid)
		b.Findings = append(b.Findings, Finding{Code: CodeManifestInvalid,
			Message: "manifest.json is not a JSON object"})
		return b, false, s.rejectBatch(b, name)
	}
	m.normalize()
	sha := model.ContentHash(m)

	invalid := func(reason string) (*batchState, bool, error) {
		b := newBatchState(m, sha, ChannelFile, s.scanCount)
		b.addCode(CodeManifestInvalid)
		b.Findings = append(b.Findings, Finding{Code: CodeManifestInvalid, Message: reason})
		return b, false, s.rejectBatch(b, name)
	}
	if err := m.validate(ChannelFile); err != nil {
		return invalid(err.Error())
	}
	if m.BatchID != name {
		return invalid("the batch directory name and the manifest's batch_id disagree")
	}

	// Replay and conflict. A batch that was rejected is not a processed batch:
	// the customer's own interface document says a rejected run is re-delivered
	// unchanged, so an identical re-delivery of a rejected batch is a fresh
	// attempt and not a replay.
	if prev, ok := s.batches[m.BatchID]; ok && prev.Status != BatchRejected {
		if prev.ManifestSHA256 == sha {
			return s.replayBatch(prev, name)
		}
		b := newBatchState(m, sha, ChannelFile, s.scanCount)
		b.addCode(CodeBatchIDConflict)
		b.Findings = append(b.Findings, Finding{Code: CodeBatchIDConflict,
			Message: "this batch id was already delivered with a different manifest"})
		return b, false, s.rejectBatch(b, name)
	}

	b = newBatchState(m, sha, ChannelFile, s.scanCount)
	s.remember(b)

	// Every profile has to resolve before anything is read: a batch carrying a
	// file nobody can parse is not partially applicable, because the manifest is
	// one statement about one delivery.
	formats := make([]importer.Format, len(m.Files))
	for i, f := range m.Files {
		fm, err := ResolveFormat(f.Profile, f.Format)
		if err != nil {
			b.addCode(CodeUnknownProfile)
			b.Findings = append(b.Findings, Finding{Code: CodeUnknownProfile, Field: f.Path,
				Message: "no importer is registered for profile " + f.Profile})
			return b, false, s.rejectBatch(b, name)
		}
		formats[i] = fm
	}

	// Publication is complete, so the batch becomes the twin's.
	processing := filepath.Join(s.cfg.InboxDir, dirProcessing, name)
	if err := s.moveDir(dir, processing, dirProcessing, name); err != nil {
		return b, false, err
	}

	declared := map[string]bool{}
	for _, f := range m.Files {
		declared[f.Path] = true
	}
	if entries, err := os.ReadDir(processing); err == nil {
		for _, e := range entries {
			if e.IsDir() || e.Name() == manifestName || declared[e.Name()] || skipName(e.Name()) {
				continue
			}
			b.Findings = append(b.Findings, Finding{Code: CodeUndeclaredFile, Field: e.Name(),
				Message: "the batch carries a file the manifest does not name"})
		}
	}

	// Phase one: read, verify and parse every file. Nothing is applied yet, so
	// the mandatory sha256 and record_count guards of BUILD-SPEC 8.1 hold for
	// the whole batch and not merely for the file they are declared on.
	type prepared struct {
		file   manifestFile
		parsed *parsed
		codes  []string
		errs   []Finding
	}
	preps := make([]prepared, 0, len(m.Files))
	for i, f := range m.Files {
		p := prepared{file: f}
		bytes, err := os.ReadFile(filepath.Join(processing, f.Path))
		switch {
		case err != nil:
			p.codes = append(p.codes, CodeFileUnreadable)
			p.errs = append(p.errs, Finding{Code: CodeFileUnreadable, Field: f.Path,
				Message: "the file the manifest names could not be read"})
		default:
			if got := model.Sha256Hex(bytes); got != f.SHA256 {
				p.codes = append(p.codes, CodeChecksumMismatch)
				p.errs = append(p.errs, Finding{Code: CodeChecksumMismatch, Field: f.Path,
					Message: "the file's bytes do not hash to the sha256 the manifest declares"})
				break
			}
			sha, err := s.st.PutRaw(bytes)
			if err != nil {
				return b, false, err
			}
			b.sourceSHA[f.Path] = sha
			parsedFile, err := parseFile(context.Background(), formats[i], bytes,
				importerOptions(f), fileLocator(f))
			if err != nil {
				return b, false, err
			}
			p.parsed = parsedFile
			if n := declaredRecords(f); n >= 0 && n != parsedFile.Seen {
				p.codes = append(p.codes, CodeRecordCountMismatch)
				p.errs = append(p.errs, Finding{Code: CodeRecordCountMismatch, Field: f.Path,
					Message: "the file parsed to a different number of records than the manifest declares"})
			}
			if parsedFile.Fatal {
				p.codes = append(p.codes, CodeFileNotAccepted)
			}
			p.errs = append(p.errs, parsedFile.FileErrors...)
		}
		preps = append(preps, p)
	}

	// on_error abort_batch is exact because phase one already knows everything:
	// a batch that would reject anything applies nothing at all.
	abort := false
	if m.OnError == OnErrorAbortBatch {
		for _, p := range preps {
			if len(p.codes) > 0 || p.parsed == nil || p.parsed.blocked() {
				abort = true
				break
			}
		}
	}

	// Phase two: apply, or report what would have been applied.
	for _, p := range preps {
		entry := FileReceipt{
			Path:            p.file.Path,
			Dataset:         p.file.Dataset,
			Format:          p.file.Format,
			Profile:         p.file.Profile,
			Encoding:        p.file.Encoding,
			DeclaredRecords: declaredRecords(p.file),
			SHA256:          p.file.SHA256,
			Codes:           p.codes,
		}
		if entry.Codes == nil {
			entry.Codes = []string{}
		}
		b.Findings = append(b.Findings, p.errs...)
		if p.parsed == nil {
			entry.Status = BatchRejected
			b.Files = append(b.Files, entry)
			for _, c := range p.codes {
				b.addCode(c)
			}
			continue
		}
		entry.ParsedRecords = p.parsed.Seen
		wholeFileReject := len(p.codes) > 0

		res, err := s.apply(applyInput{
			dataset:  model.Dataset(p.file.Dataset),
			parsed:   p.parsed,
			prov:     b.provenanceTemplate(p.file, s.scanCount, 0),
			file:     p.file.Path,
			locate:   fileLocator(p.file),
			dryRun:   abort || wholeFileReject,
			preconds: nil,
		})
		if err != nil {
			return b, false, err
		}
		if abort || wholeFileReject {
			reason := "the batch was aborted before any record was applied"
			code := CodeFileNotAccepted
			if wholeFileReject {
				reason = "the file was rejected as a whole before any record was applied"
				code = p.codes[0]
			}
			res = rejectAll(res, code, reason)
		}
		b.absorb(res)
		entry.Counts = res.counts
		entry.Status = fileStatus(res.counts, p.codes)
		b.Files = append(b.Files, entry)
		for _, c := range p.codes {
			b.addCode(c)
		}
	}

	// The batch's own status, then matching, then the receipt: a receipt names
	// the proposals and exceptions its own records produced.
	switch {
	case abort:
		b.Status = BatchRejected
		b.addCode(CodeFileNotAccepted)
	case b.Counts.Rejected+b.Counts.Quarantined == 0 && len(b.Codes) == 0:
		b.Status = BatchAccepted
	case b.Counts.Accepted+b.Counts.AcceptedWithWarning+b.Counts.SkippedUnchanged == 0:
		b.Status = BatchRejected
	default:
		b.Status = BatchPartiallyAccepted
	}
	if b.Status != BatchRejected {
		if err := s.matchBatch(b); err != nil {
			return b, false, err
		}
	}
	if err := s.finishBatch(b, name); err != nil {
		return b, false, err
	}
	return b, false, nil
}

// fileLocator returns the pointer function of a file entry: the pointer shape
// follows the file's own format, so a finding on a CSV row is line:col and one
// on an XML element is an XPath.
func fileLocator(f manifestFile) func(int, string) string {
	switch f.Format {
	case "csv":
		return func(ord int, field string) string {
			return strconv.Itoa(ord+1) + ":" + field
		}
	case "xml":
		return func(ord int, field string) string {
			p := "/records/record[" + strconv.Itoa(ord) + "]"
			if field != "" {
				p += "/" + field
			}
			return p
		}
	case "json", "ndjson":
		return func(ord int, field string) string {
			return strconv.Itoa(ord) + ":" + jsonPointer(field)
		}
	default:
		// A legacy fixed-layout file has records, not fields, so the locator is
		// the record and the field's own name.
		return func(ord int, field string) string {
			if field == "" {
				return strconv.Itoa(ord)
			}
			return strconv.Itoa(ord) + ":" + field
		}
	}
}

// rejectAll turns every applied outcome of a dry run into a rejection carrying
// the batch- or file-level code that stopped it, so an aborted batch reports one
// outcome per record and the closure invariant still holds.
func rejectAll(res applyResult, code, message string) applyResult {
	out := applyResult{versions: map[string]int{}, docWarnings: res.docWarnings}
	for _, r := range res.records {
		if r.Outcome != OutcomeRejected && r.Outcome != OutcomeQuarantined {
			r.Outcome = OutcomeRejected
			r.Version = 0
			r.ContentHash = ""
			r.RawExcerptSHA256 = ""
			r.Errors = append(r.Errors, Finding{Code: code, Message: message})
		}
		out.records = append(out.records, r)
		out.counts.count(r.Outcome)
	}
	out.counts.Seen = len(out.records)
	return out
}

// remember records a batch under its id, keeping the first-seen order that
// GET /admin/v1/batches reports.
func (s *Server) remember(b *batchState) {
	if _, ok := s.batches[b.ID]; !ok {
		s.batchOrder = append(s.batchOrder, b.ID)
	}
	s.batches[b.ID] = b
}

// finishBatch writes the receipt, moves the batch directory to its final home
// and folds it into the run report. The closure invariant is asserted here,
// which is the last moment at which the twin can still tell the truth about
// what it did with the delivery.
func (s *Server) finishBatch(b *batchState, dirName string) error {
	b.Receipt = b.receipt(s.gov.NowVms())
	b.Receipt.ClosureOK = true
	closureErr := assertClosure(b.Receipt)
	if closureErr != nil {
		b.Receipt.ClosureOK = false
		b.addCode(CodeClosureViolation)
		b.Receipt.Codes = b.Codes
	}
	if err := s.writeReceipt(b); err != nil {
		return err
	}
	target := dirProcessed
	if b.Status == BatchRejected {
		target = dirRejected
	}
	from := filepath.Join(s.cfg.InboxDir, dirProcessing, dirName)
	if _, err := os.Stat(from); err == nil {
		if err := s.moveDir(from, filepath.Join(s.cfg.InboxDir, target, dirName), target, dirName); err != nil {
			return err
		}
	}
	run := s.run(b.manifest.RunID)
	run.Scans = s.scanCount
	run.note(b)
	if closureErr != nil {
		run.Status = "failed"
		return closureErr
	}
	return nil
}

// rejectBatch is finishBatch for a batch that never reached the processing
// directory: the manifest was missing, unreadable, invalid, in conflict or
// unreadable by any registered importer. The files are preserved under
// rejected/, because the sender will ask what was wrong with them.
func (s *Server) rejectBatch(b *batchState, dirName string) error {
	b.Status = BatchRejected
	s.remember(b)
	b.Receipt = b.receipt(s.gov.NowVms())
	b.Receipt.ClosureOK = true
	if err := s.writeReceipt(b); err != nil {
		return err
	}
	from := filepath.Join(s.cfg.InboxDir, dirIncoming, dirName)
	if _, err := os.Stat(from); err == nil {
		if err := s.moveDir(from, filepath.Join(s.cfg.InboxDir, dirRejected, dirName), dirRejected, dirName); err != nil {
			return err
		}
	}
	run := s.run(b.manifest.RunID)
	run.Scans = s.scanCount
	run.note(b)
	return nil
}

// replayBatch handles a byte-identical re-delivery of a batch already
// processed: nothing is applied, no proposal is emitted, and the receipt is the
// first delivery's receipt with replay set. The re-delivered directory is
// preserved under processed/ with the scan counter appended, so the two
// deliveries stay distinguishable without a timestamp.
func (s *Server) replayBatch(prev *batchState, dirName string) (*batchState, bool, error) {
	replay := *prev
	replay.Status = BatchReplayed
	replay.Scan = s.scanCount
	records, counts := replayResults(prev.Records)
	replay.Records = records
	replay.Counts = counts
	replay.Counts.Seen = 0
	replay.Files = make([]FileReceipt, 0, len(prev.Files))
	for _, f := range prev.Files {
		// The replayed count carries over from the previous delivery, whether
		// that was the original or itself a replay, so a third delivery still
		// reports how many records the batch holds.
		f.Counts = Counts{Replayed: f.Counts.Seen + f.Counts.Replayed}
		f.ParsedRecords = 0
		f.Status = BatchReplayed
		replay.Files = append(replay.Files, f)
	}
	replay.Proposals = []string{}
	replay.Exceptions = []ExceptionRef{}
	s.batches[replay.ID] = &replay

	replay.Receipt = replay.receipt(s.gov.NowVms())
	replay.Receipt.ClosureOK = true
	if err := assertClosure(replay.Receipt); err != nil {
		replay.Receipt.ClosureOK = false
		return &replay, false, err
	}
	if err := s.writeReceipt(&replay); err != nil {
		return &replay, false, err
	}
	from := filepath.Join(s.cfg.InboxDir, dirIncoming, dirName)
	to := filepath.Join(s.cfg.InboxDir, dirProcessed, dirName)
	if _, err := os.Stat(to); err == nil {
		to += ".replay-" + strconv.FormatInt(s.scanCount, 10)
	}
	if _, err := os.Stat(from); err == nil {
		if err := s.moveDir(from, to, dirProcessed, filepath.Base(to)); err != nil {
			return &replay, false, err
		}
	}
	run := s.run(replay.manifest.RunID)
	run.Scans = s.scanCount
	return &replay, false, nil
}

// moveDir renames a batch directory and records that the twin owns the target.
// A target that already exists is not overwritten: the scan counter is appended,
// so two deliveries of one name are both preserved and neither is silently lost.
func (s *Server) moveDir(from, to, ownerDir, ownerName string) error {
	if _, err := os.Stat(to); err == nil {
		to += ".scan-" + strconv.FormatInt(s.scanCount, 10)
		ownerName += ".scan-" + strconv.FormatInt(s.scanCount, 10)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("miniblp: moving %s to %s: %w", from, to, err)
	}
	s.own(ownerDir + "/" + ownerName)
	return fsyncDir(filepath.Dir(to))
}

// own records that the twin created an entry in a directory it owns, so
// detectForeignWrites can tell its own work from the connector's.
func (s *Server) own(rel string) { s.owned[rel] = true }

// adoptOwned records every entry already present in the twin-owned directories
// when the twin opens.
//
// A restarted twin cannot tell its own earlier output from a foreign write, and
// guessing would mean reporting every batch of the previous process as a
// violation the first time the new one scans. Adopting what is already there is
// the honest reading of the contract: the detector's job is to catch a write
// that appears WHILE the twin is running, which is exactly when a connector
// could be racing it for a batch. POST /admin/v1/reset empties the directories,
// so a fresh run adopts nothing.
func (s *Server) adoptOwned() {
	for _, d := range []string{dirProcessing, dirProcessed, dirRejected, dirReceipts} {
		entries, err := os.ReadDir(filepath.Join(s.cfg.InboxDir, d))
		if err != nil {
			continue
		}
		for _, e := range entries {
			s.own(d + "/" + e.Name())
		}
	}
	entries, err := os.ReadDir(s.cfg.OutboxDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		s.own("outbox/" + e.Name())
	}
}

// detectForeignWrites returns the entries found in a twin-owned directory that
// the twin did not create, in sorted order.
//
// Only incoming/ is the connector's. A connector that writes into processing/,
// processed/, rejected/, receipts/ or the outbox is either racing the twin for
// a batch it does not own or fabricating a receipt, and in both cases the run's
// bookkeeping has stopped meaning anything - which is why this fails the run
// rather than warning about it. The comparison is over the top-level entries of
// each directory; inside a receipt directory everything is the twin's.
func (s *Server) detectForeignWrites() []string {
	var foreign []string
	check := func(root, label string) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".tmp-") {
				continue
			}
			if !s.owned[label+"/"+name] {
				foreign = append(foreign, label+"/"+name)
			}
		}
	}
	for _, d := range []string{dirProcessing, dirProcessed, dirRejected, dirReceipts} {
		check(filepath.Join(s.cfg.InboxDir, d), d)
	}
	check(s.cfg.OutboxDir, "outbox")
	sort.Strings(foreign)
	return foreign
}
