package miniblp

import (
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// A batchState is everything the twin knows about one batch, on either channel.
// It is kept in memory for the life of the process: the store holds the records
// and their provenance, but the per-record outcomes of a delivery are a report
// about that delivery, and GET /admin/v1/batches/{id}/records serves them.
type batchState struct {
	// ID is the manifest's batch id and the key of the batch.
	ID string `json:"batch_id"`
	// Ref is what a client addresses the batch by. It is the batch id on both
	// channels: a ref that is not the id would be a mapping a resuming client
	// has to persist for no reason.
	Ref string `json:"batch_ref"`
	// Channel is ChannelFile or ChannelREST.
	Channel string `json:"channel"`
	// Status is one of the Batch constants.
	Status string `json:"status"`
	// ManifestSHA256 identifies the manifest. A second delivery of the same
	// batch id with this same hash is a replay; with a different one it is
	// CodeBatchIDConflict.
	ManifestSHA256 string `json:"manifest_sha256"`
	// Scan is the inbox scan that picked the batch up, 0 on the REST channel.
	Scan int64 `json:"received_scan"`
	// Codes are the batch-level findings, sorted.
	Codes []string `json:"codes"`
	// Counts is the batch tally.
	Counts Counts `json:"counts"`
	// Files is the per-file half of the receipt, one entry per file or chunk.
	Files []FileReceipt `json:"files"`
	// Records are the per-record results in delivery order.
	Records []RecordResult `json:"-"`
	// Findings are the file- and batch-level findings, errors and warnings
	// alike.
	Findings []Finding `json:"-"`
	// Proposals and Exceptions are what the matching engine produced from this
	// batch.
	Proposals  []string       `json:"-"`
	Exceptions []ExceptionRef `json:"-"`
	// Receipt is the report, written by the file channel and returned by commit.
	Receipt *Receipt `json:"-"`

	// manifest is the normalized manifest.
	manifest manifest
	// changedInvoices is the accumulated set of invoice keys this batch created
	// or changed.
	changedInvoices map[string]bool
	// invoiceVersions is the version each changed invoice reached.
	invoiceVersions map[string]int
	// docWarnings maps a document key to its importer warning codes, so a
	// warning can travel onto the proposal the document produces.
	docWarnings map[string][]string
	// sourceSHA maps a file name to the content address of its raw bytes.
	sourceSHA map[string]string
	// chunkNext is the next chunk ordinal expected per dataset on the REST
	// channel. Ordinals start at 1 and are strictly increasing per
	// (batch, dataset).
	chunkNext map[string]int64
	// chunkApplied maps "dataset|ordinal" to the idempotency key that applied
	// it, so a second application of the same chunk under a different key can
	// be counted as a duplicate apply attempt.
	chunkApplied map[string]string
	// applyFailKey is the idempotency key of the chunk that was applied and then
	// answered with the injected 500. A retry under that same key is the correct
	// client behavior and is what 5xx_retried counts.
	applyFailKey string
}

// newBatchState returns an empty batch for a manifest.
func newBatchState(m manifest, sha, channel string, scan int64) *batchState {
	return &batchState{
		ID:              m.BatchID,
		Ref:             m.BatchID,
		Channel:         channel,
		Status:          BatchOpen,
		ManifestSHA256:  sha,
		Scan:            scan,
		Codes:           []string{},
		Files:           []FileReceipt{},
		Records:         []RecordResult{},
		Findings:        []Finding{},
		Proposals:       []string{},
		Exceptions:      []ExceptionRef{},
		manifest:        m,
		changedInvoices: map[string]bool{},
		invoiceVersions: map[string]int{},
		docWarnings:     map[string][]string{},
		sourceSHA:       map[string]string{},
		chunkNext:       map[string]int64{},
		chunkApplied:    map[string]string{},
	}
}

// addCode records a batch-level finding once, keeping the list sorted.
func (b *batchState) addCode(code string) {
	for _, c := range b.Codes {
		if c == code {
			return
		}
	}
	b.Codes = append(b.Codes, code)
	sort.Strings(b.Codes)
}

// absorb merges the outcome of applying one file or chunk into the batch.
func (b *batchState) absorb(res applyResult) {
	b.Records = append(b.Records, res.records...)
	b.Counts.add(res.counts)
	for _, k := range res.changedInvoices {
		b.changedInvoices[k] = true
	}
	for k, v := range res.versions {
		b.invoiceVersions[k] = v
	}
	for k, codes := range res.docWarnings {
		for _, c := range codes {
			b.docWarnings[k] = appendCode(b.docWarnings[k], c)
		}
	}
}

// changed returns the invoice keys the batch touched, in ascending key order.
func (b *batchState) changed() []string { return sortedKeys(b.changedInvoices) }

// receipt assembles the batch's receipt. It is the same object on both
// channels: the file channel writes it to receipts/<batch_id>/receipt.json and
// the REST channel returns it from commit, so the two are byte-comparable
// except for the channel field and the shape of the per-record locators.
func (b *batchState) receipt(vms int64) *Receipt {
	m := b.manifest
	r := &Receipt{
		BatchID:        b.ID,
		BatchRef:       b.Ref,
		Channel:        b.Channel,
		RunID:          m.RunID,
		Tenant:         m.Tenant,
		SourceSystem:   m.SourceSystem,
		Producer:       m.Producer,
		Sequence:       m.Sequence,
		Mode:           m.Mode,
		FullLoad:       m.FullLoad,
		OnError:        m.OnError,
		Profiles:       m.profiles(),
		ManifestSHA256: b.ManifestSHA256,
		ReceivedScan:   b.Scan,
		Status:         b.Status,
		Replay:         b.Status == BatchReplayed,
		Codes:          b.Codes,
		Counts:         b.Counts,
		Files:          b.Files,
		Findings:       b.Findings,
		Proposals:      b.Proposals,
		Exceptions:     b.Exceptions,
		VirtualClockMs: vms,
	}
	if r.Profiles == nil {
		r.Profiles = []string{}
	}
	if r.Files == nil {
		r.Files = []FileReceipt{}
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Codes == nil {
		r.Codes = []string{}
	}
	return r
}

// A runReport is the twin's per-run summary, served by GET /admin/v1/runs/{id}
// and GET /admin/v1/runs/last. A run is named by the run id the connector puts
// in its manifests, which is the id the grader passed it.
type runReport struct {
	RunID string `json:"run_id"`
	// Status is "ok" or "failed". A foreign write into a twin-owned directory
	// and a closure violation are the two things that fail a run.
	Status string `json:"status"`
	// Scans is how many inbox scans this run's batches were picked up by.
	Scans int64 `json:"scans"`
	// Batches are the batch ids of the run, in first-seen order.
	Batches []string `json:"batches"`
	// Counts is the tally across the run's batches.
	Counts Counts `json:"counts"`
	// FullLoad reports that at least one batch of the run declared a full
	// reload. It is the full-reload detector of BUILD-SPEC 8.1.
	FullLoad bool `json:"full_load"`
	// Watermark is the last watermark the run's manifests declared, per
	// dataset.
	Watermark map[string]string `json:"watermark"`
	// Proposals and Exceptions count what the matching engine produced.
	Proposals  int `json:"proposals_emitted"`
	Exceptions int `json:"exceptions_opened"`
	// Codes are the run-level findings, sorted.
	Codes []string `json:"codes"`
}

// run returns the run report for a run id, creating it on first use.
func (s *Server) run(runID string) *runReport {
	if runID == "" {
		runID = s.cfg.RunID
	}
	r, ok := s.runs[runID]
	if !ok {
		r = &runReport{
			RunID:     runID,
			Status:    "ok",
			Batches:   []string{},
			Watermark: map[string]string{},
			Codes:     []string{},
		}
		s.runs[runID] = r
		s.runList = append(s.runList, runID)
	}
	s.lastRun = runID
	return r
}

// addCode records a run-level finding once.
func (r *runReport) addCode(code string) {
	for _, c := range r.Codes {
		if c == code {
			return
		}
	}
	r.Codes = append(r.Codes, code)
	sort.Strings(r.Codes)
}

// note folds one finished batch into the run report.
func (r *runReport) note(b *batchState) {
	for _, id := range r.Batches {
		if id == b.ID {
			return
		}
	}
	r.Batches = append(r.Batches, b.ID)
	r.Counts.add(b.Counts)
	if b.manifest.FullLoad {
		r.FullLoad = true
	}
	for k, v := range b.manifest.Watermark {
		r.Watermark[k] = v
	}
	r.Proposals += len(b.Proposals)
	r.Exceptions += len(b.Exceptions)
	for _, c := range b.Codes {
		r.addCode(c)
	}
}

// provenanceTemplate returns the provenance every revision of one file or chunk
// shares: everything about the delivery except the record's own position.
func (b *batchState) provenanceTemplate(f manifestFile, scan int64, requestSeq int64) store.Provenance {
	prov := store.Provenance{
		BatchID:      b.ID,
		Channel:      b.Channel,
		SourceSHA256: b.sourceSHA[f.Path],
		Profile:      f.Profile,
		Format:       f.Format,
		Encoding:     f.Encoding,
		SourceSystem: b.manifest.SourceSystem,
		RunID:        b.manifest.RunID,
	}
	if b.Channel == ChannelFile {
		prov.SourceFile = f.Path
		if scan > 0 {
			s := scan
			prov.ReceivedScan = &s
		}
		return prov
	}
	if requestSeq > 0 {
		q := requestSeq
		prov.RequestSeq = &q
	}
	return prov
}
