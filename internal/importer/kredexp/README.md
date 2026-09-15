# The Go task: `kredexp-2.1`

This package is a stub. You implement it.

**The specification is `docs/customer/KRED-EXP-2.1.md`.** It is the customer's own
interface document, translated from the German original, and it is what a real
sender hands you: released, six years old, and self-contradictory in three
places. This README is only the harness contract. Where the two disagree about
the format, the customer document wins; where they disagree about the harness,
this file wins.

Read section 9 of the customer document, "Open points and known deviations",
before you write any code. It is shorter than this README.

## What to implement

`importer.Format` - `Detect` and `Parse` - in this package and nothing outside
it. Optionally also `importer.StreamParser`, which the twin prefers and which the
golden runner exercises against `Parse` for equality. A 20 MB export is the
documented normal case.

Use `importer.NewBuilder`: it sorts the diagnostics, applies the 1000-entry cap,
guarantees the non-nil slices and computes `Accepted`. A `Result` assembled by
hand will get one of those four wrong.

Useful, already written, do not reimplement:

| Need | Use |
|---|---|
| CP1252 byte to rune, including the five undefined positions | `importer.CP1252Rune`, and `importer.NewDecoder(importer.EncodingCP1252, r)` for a stream |
| Detecting a byte order mark before decoding | `importer.StripBOM` |
| The accepted numeric shapes, apostrophe grouping, trailing minus, Swiss `.-` | `importer.ParseAmount` |
| Exact decimals, money, the canonical AP entities, natural keys, canonical JSON | `internal/model` |
| Diagnostics, severities, the published codes | `internal/importer` |

`TTMMJJ` dates are yours: `importer.ParseDate` does not know that format, and the
pivot at 70 is a rule of this interface, not of the landscape.
## The contract you must not break

1. **A defect in the data is never an `error`.** It is a `Diagnostic` on the
   `Result`. `error` is for programming and environment faults only: a nil or
   cancelled context, an impossible internal state.
2. **`Parse` is deterministic.** Identical `raw` and identical `Options` produce a
   byte-identical projection, on every machine and every run. No wall clock, no
   unseeded randomness, no map iteration order in an output, no locale. Sort
   explicitly wherever order matters.
3. **No `float64`** anywhere on the parsing path. Amounts and quantities are
   `model.Decimal`.
4. **Inputs are never rounded.** A value with more fraction digits than its field
   allows is `invalid_amount`, not a rounded value. Rounding an input is how a
   supplier gets paid the wrong amount.
5. **`Accepted`** is true only when there is no fatal and no reject diagnostic
   **and** the trailer verified. `importer.Builder` computes this; do not
   second-guess it.
6. **Do not change** anything outside this package, except exactly one line: the
   blank import in `internal/importer/all/all.go`, already there and commented.
   `tools/verify-pristine.sh` tells you if you slipped, and the grader grades a
   tree assembled from our files with only your four paths taken from yours, so a
   change anywhere else cannot reach the run.

## Diagnostic codes (BUILD-SPEC 10, closed set)

The constants are declared in `kredexp.go` with a sentence each on what they
mean. Nothing outside this list is graded, because nothing outside it is
published.

**Fatal, file level, first one wins, checked in this order:**
`encoding_bom_present`, `encoding_invalid_byte`, `malformed_quoting`,
`vorlauf_missing`, `format_version_unsupported`, `trailer_missing`.

**Reject, record level, parsing continues:** `unknown_record_type`,
`field_count_mismatch`, `missing_required_field`, `invalid_date`,
`invalid_amount`, `orphan_line`.

**Trailer, only when nothing was rejected:** `trailer_count_mismatch`,
`trailer_sum_mismatch`. Otherwise the warning `trailer_not_verified`, because a
control total computed over an incomplete set of records is not evidence.

**Warnings:** `ambiguous_field_semantics`, `trailer_not_verified`,
`diagnostics_truncated` (the framework's, never yours).

## Ordering rules

- Diagnostics are sorted by line, then record, then field, then code. The builder
  does it; ties keep the order you emitted them in, so emit deterministically.
- Fatal file-level checks are evaluated in the order listed above, and the first
  one that fires wins. A file with a BOM *and* no trailer reports the BOM.
- Documents come out in delivery order. The builder assigns the ordinals.
- The projection sorts documents by (dataset, key) and diagnostic tuples by
  (code, line, field): the order you emit documents in is not graded, the line
  you report a finding on is.

## Running the golden test

```
go test ./internal/importer/            # skips until the format is registered
go test ./internal/importer/ -run Golden -v
make golden-diff                        # the readable diff, per document and per finding
```

The runner walks `testdata/kredexp/`, calls `Detect` then `Parse` on each fixture,
projects with `importer.Projection`, and compares against the sibling
`*.expected.json` with `importer.GoldenDiff`. A fixture may carry a
`*.options.json` naming its encoding.

Until you uncomment the blank import in `internal/importer/all/all.go`, the
runner skips with a message that says exactly that. That is deliberate: an
unimplemented task should not look like a broken repository.

## Before you start

The contradictions in the customer document are the highest-signal part of this
task. Where a field's meaning is genuinely undecided, carry the value verbatim
and emit `ambiguous_field_semantics`; do not pick the reading that looks more
likely. Where a sign could be derived two ways, the trailer sums are the
available evidence.

Record what you decided and why in `DECISIONS.md`. A short, specific note about a
contradiction you found is worth more than a long one about architecture.
