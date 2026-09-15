package miniblp

import (
	"errors"
	"os"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer/canonical"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Manifest profiles a batch may declare, from BUILD-SPEC 8.1.
const (
	// ProfileCanonical is the twin's own record shape, delivered in any of the
	// four wire formats. The manifest's format field picks the reader.
	ProfileCanonical = "blp-canonical-v1"
	// ProfileKredExp is the legacy Kreditoren-Sammelexport of the Swiss
	// subsidiary. The bytes are relayed unparsed by the connector and the twin
	// owns the parse.
	ProfileKredExp = "kredexp-2.1"
	// ProfileKredExpReference is the id our own reference implementation of the
	// legacy importer registers under. See ResolveFormat.
	ProfileKredExpReference = "kredexp-2.1-reference"
)

// EnvReferenceImporters is the environment variable that switches the reference
// importers on. See ResolveFormat.
const EnvReferenceImporters = "MINIBLP_REFERENCE_IMPORTERS"

// Manifest modes and error policies.
const (
	// ModeUpsert applies every record as an upsert. It is the default.
	ModeUpsert = "upsert"
	// ModeReplaceDataset declares a full reload of the datasets it carries. It
	// requires full_load and surfaces as full_load on the run report, which is
	// the full-reload detector.
	ModeReplaceDataset = "replace_dataset"
	// OnErrorContinue applies every acceptable file of a batch and reports the
	// rest. It is the default.
	OnErrorContinue = "continue"
	// OnErrorAbortBatch applies nothing when any file or any record of the batch
	// is rejected. Because the twin verifies and parses every file of a batch
	// before it applies the first record, the policy is exact on the file
	// channel: an aborted batch leaves no partial state behind.
	//
	// On the REST channel it is advisory. A chunk is applied when it arrives,
	// which is what makes the channel streamable, so a client that wants
	// all-or-nothing either uses the file channel or validates before it sends;
	// POST /v1/ingest/batches/{ref}/abort closes the batch without matching but
	// does not unapply what was already delivered, because the store is
	// append-only and a delivered record is a fact.
	OnErrorAbortBatch = "abort_batch"
)

// manifest is a batch manifest, in the JSON shape of BUILD-SPEC 8.1. The REST
// channel sends the same object with the per-file paths and hashes omitted,
// which is why those two fields are the only ones whose absence is allowed to
// depend on the channel.
type manifest struct {
	ManifestVersion string            `json:"manifest_version"`
	BatchID         string            `json:"batch_id"`
	Sequence        int               `json:"sequence"`
	Producer        string            `json:"producer"`
	RunID           string            `json:"run_id"`
	Tenant          string            `json:"tenant"`
	SourceSystem    string            `json:"source_system"`
	Mode            string            `json:"mode"`
	FullLoad        bool              `json:"full_load"`
	Watermark       map[string]string `json:"watermark"`
	OnError         string            `json:"on_error"`
	// Profile is an optional batch-level default for the files' profile. The
	// spec puts the profile on the file entry; a batch of one format may say it
	// once instead.
	Profile string         `json:"profile"`
	Files   []manifestFile `json:"files"`
}

// manifestFile is one file entry of a manifest.
type manifestFile struct {
	Path     string `json:"path"`
	Dataset  string `json:"dataset"`
	Format   string `json:"format"`
	Profile  string `json:"profile"`
	Encoding string `json:"encoding"`
	// RecordCount is a pointer because it is mandatory on the file channel and
	// absent is a different fact from zero: an empty file declares 0.
	RecordCount *int              `json:"record_count"`
	SHA256      string            `json:"sha256"`
	CSV         *httpx.CSVDialect `json:"csv"`
}

// normalize fills in the documented defaults and returns the manifest as the
// twin stores and hashes it. It is applied before the manifest hash is taken, so
// two deliveries that differ only in an omitted default are the same batch.
func (m *manifest) normalize() {
	if m.ManifestVersion == "" {
		m.ManifestVersion = "1"
	}
	if m.Mode == "" {
		m.Mode = ModeUpsert
	}
	if m.OnError == "" {
		m.OnError = OnErrorContinue
	}
	if m.Watermark == nil {
		m.Watermark = map[string]string{}
	}
	for i := range m.Files {
		f := &m.Files[i]
		if f.Profile == "" {
			f.Profile = m.Profile
		}
		if f.Profile == "" {
			f.Profile = ProfileCanonical
		}
		if f.Encoding == "" {
			f.Encoding = importer.EncodingUTF8
		}
		if f.Format == "" {
			f.Format = formatFromPath(f.Path)
		}
	}
}

// formatFromPath guesses a file's wire format from its extension. It is only
// ever a default for a manifest entry that omitted the format; a wrong guess
// surfaces as a parse finding on the file and never as silently applied data.
func formatFromPath(path string) string {
	switch {
	case strings.HasSuffix(path, ".csv"):
		return "csv"
	case strings.HasSuffix(path, ".xml"):
		return "xml"
	case strings.HasSuffix(path, ".ndjson"):
		return "ndjson"
	case strings.HasSuffix(path, ".json"):
		return "json"
	case strings.HasSuffix(path, ".txt"):
		return "txt"
	}
	return ""
}

// validate reports the manifest defects that make a batch unprocessable. Each
// one is a whole-batch reject with CodeManifestInvalid, because a manifest is the
// only statement about what the batch contains: if it cannot be trusted, no file
// in the batch can be either.
func (m *manifest) validate(channel string) error {
	if m.ManifestVersion != "1" {
		return errors.New("manifest_version 1 is the only version this twin reads")
	}
	if strings.TrimSpace(m.BatchID) == "" {
		return errors.New("batch_id is mandatory")
	}
	// A batch id and a run id both become directory names - the batch
	// directory, the receipt directory, the outbox subdirectory - so they are
	// restricted to a safe name. A manifest is delivered by a client; a client
	// that can choose a path separator can choose a path.
	if !isSafeName(m.BatchID) {
		return errors.New("batch_id is 1 to 128 characters of letters, digits, dot, dash or underscore")
	}
	if m.RunID != "" && !isSafeName(m.RunID) {
		return errors.New("run_id is 1 to 128 characters of letters, digits, dot, dash or underscore")
	}
	if m.Mode != ModeUpsert && m.Mode != ModeReplaceDataset {
		return errors.New("mode is upsert or replace_dataset")
	}
	if m.Mode == ModeReplaceDataset && !m.FullLoad {
		return errors.New("mode replace_dataset requires full_load true")
	}
	if m.OnError != OnErrorContinue && m.OnError != OnErrorAbortBatch {
		return errors.New("on_error is continue or abort_batch")
	}
	if channel == ChannelFile && len(m.Files) == 0 {
		return errors.New("a file batch declares at least one file")
	}
	seen := map[string]bool{}
	for i := range m.Files {
		f := m.Files[i]
		if channel == ChannelFile {
			if strings.TrimSpace(f.Path) == "" {
				return errors.New("every file entry declares a path")
			}
			if strings.Contains(f.Path, "/") || strings.Contains(f.Path, `\`) ||
				f.Path == "." || f.Path == ".." {
				return errors.New("a file path is a plain name inside the batch directory: " + f.Path)
			}
			if f.Path == manifestName {
				return errors.New("the manifest is not one of the batch's data files")
			}
			if seen[f.Path] {
				return errors.New("duplicate file entry: " + f.Path)
			}
			seen[f.Path] = true
			// The two mandatory guards of BUILD-SPEC 8.1. They are verified
			// before a single record is applied, which is the file channel's
			// structural advantage over the REST channel and the only reason a
			// truncated transfer cannot reach the ledger.
			if f.RecordCount == nil {
				return errors.New("record_count is mandatory for " + f.Path)
			}
			if *f.RecordCount < 0 {
				return errors.New("record_count is not negative for " + f.Path)
			}
			if !isSHA256(f.SHA256) {
				return errors.New("sha256 is mandatory and is 64 lowercase hex digits for " + f.Path)
			}
		}
		if f.Dataset != "" && !model.IsKnownDataset(f.Dataset) && f.Profile != ProfileKredExp {
			return errors.New("unknown dataset " + f.Dataset + " for " + f.Path)
		}
		if f.Encoding != importer.EncodingUTF8 && f.Encoding != importer.EncodingUTF8BOM &&
			f.Encoding != importer.EncodingLatin1 && f.Encoding != importer.EncodingCP1252 {
			return errors.New("unknown encoding " + f.Encoding + " for " + f.Path)
		}
	}
	return nil
}

// isSafeName reports whether s can be used as a single path element: 1 to 128
// characters of ASCII letters, digits, dot, dash or underscore, not starting
// with a dot and never the traversal names.
//
// The rule is deliberately narrower than any filesystem's: a batch id and a run
// id arrive from a client, they name directories the twin creates, and the
// cheapest way to be sure a delivery cannot write outside its own tree is to
// refuse every name that could.
func isSafeName(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." || strings.HasPrefix(s, ".") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// isSHA256 reports whether s is 64 lowercase hex digits.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// profiles returns the distinct profiles a manifest declares, in first-seen
// order, so a batch reject can name what it could not read.
func (m *manifest) profiles() []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !seen[f.Profile] {
			seen[f.Profile] = true
			out = append(out, f.Profile)
		}
	}
	return out
}

// ResolveFormat returns the importer that reads a manifest file entry, or a
// CodeUnknownProfile error when nothing is registered for its profile.
//
// The mapping is:
//
//   - ProfileCanonical picks a reader from the entry's format: csv, xml, json or
//     ndjson, which are canonical.FormatCSV, canonical.FormatXML and
//     canonical.FormatJSON (which reads both a JSON array and NDJSON).
//   - ProfileKredExp resolves through the registry, so a twin built with the
//     candidate's importer linked in uses it and one built without it reports a
//     batch reject instead of pretending to have read the file.
//   - a profile that is itself a registered format id resolves to that format,
//     which is how a fixture can name blp-csv-v1 directly.
//
// # Reference importers
//
// When the environment variable MINIBLP_REFERENCE_IMPORTERS is set to 1 and a
// format is registered under ProfileKredExpReference, that format is preferred
// for ProfileKredExp. It is what decouples the pipeline score from the
// candidate's parser: a candidate whose importer is unfinished can still be
// graded on the end-to-end pipeline, and a candidate whose pipeline is
// unfinished can still be graded on the parser through the golden fixtures. The
// switch never changes anything else - not the manifest, not the receipt shape,
// not the provenance profile, which stays the declared ProfileKredExp - so a
// digest computed with the reference importer is comparable with one computed
// without it.
func ResolveFormat(profile, format string) (importer.Format, error) {
	switch profile {
	case ProfileKredExp:
		if referenceImportersEnabled() {
			if f, ok := importer.Lookup(ProfileKredExpReference); ok {
				return f, nil
			}
		}
		if f, ok := importer.Lookup(ProfileKredExp); ok {
			return f, nil
		}
		return nil, unknownProfile(profile, format)

	case ProfileCanonical:
		id := ""
		switch format {
		case "csv":
			id = canonical.FormatCSV
		case "xml":
			id = canonical.FormatXML
		case "json", "ndjson":
			id = canonical.FormatJSON
		default:
			return nil, unknownProfile(profile, format)
		}
		if f, ok := importer.Lookup(id); ok {
			return f, nil
		}
		return nil, unknownProfile(profile, format)
	}
	if f, ok := importer.Lookup(profile); ok {
		return f, nil
	}
	return nil, unknownProfile(profile, format)
}

// referenceImportersEnabled reports whether the reference importers are switched
// on. It reads the environment once per call, which is cheap and keeps the
// switch usable from a test.
func referenceImportersEnabled() bool {
	return strings.TrimSpace(os.Getenv(EnvReferenceImporters)) == "1"
}

// unknownProfile returns the whole-batch reject for a profile nothing can read.
func unknownProfile(profile, format string) error {
	e := &httpx.Error{
		Code:      CodeUnknownProfile,
		Message:   "no importer is registered for this profile and format",
		Retriable: false,
		Status:    422,
	}
	return e.WithDetail("profile", profile).WithDetail("format", format)
}

// resolveKredExp returns the legacy importer this build resolves, if any. It
// exists so a caller - the UI, a test, a start-up log line - can tell whether
// the twin can read the legacy profile at all without provoking a batch reject.
func resolveKredExp() (importer.Format, bool) {
	f, err := ResolveFormat(ProfileKredExp, "txt")
	if err != nil {
		return nil, false
	}
	return f, true
}
