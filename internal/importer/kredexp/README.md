# The twin's KRED-EXP importer: `kredexp-2.1`

This package is the importer BLP's invoice capture runs inside the twin to read
the Swiss subsidiary's nightly export, the legacy Kreditoren-Sammelexport,
format version 2.1. It is part of the twin, always linked in through
`internal/importer/all`, and registered under the profile `kredexp-2.1`. A
connector never reads, parses or relays such a file: the captured invoices are
already in the twin when a run starts, as batches named
`capture-KRED_<Mandant>_<YYYYMMDD>_<NNN>` with source system `blp-capture`.

The format is specified by the subsidiary's own interface document, KRED-EXP 2.1
(`KRED-EXP-2.1.md`, kept with the maintainers' internal notes). The constants and
comments in this package cite its sections.

## What it parses

One delivery `KRED_<Mandant>_<YYYYMMDD>_<NNN>.txt`, into canonical AP invoices
with their lines:

- windows-1252 without a byte order mark, `;` separated, CRLF terminated, `"` as
  text delimiter with doubled quotes and embedded line breaks,
- four record types: one `VORLAUF` first, `KOPF` per document followed by its own
  `POS` lines, one `NACHLAUF` last with the record counts and sum totals,
- decimal point with optional apostrophe grouping, a **trailing minus** for
  negatives, `TTMMJJ` dates with a century pivot at 70, per-field scales (amounts
  exactly 2 decimals, unit price 2 to 4, quantity up to 3), leading zeros
  significant everywhere.

Findings are `importer.Diagnostic`s from a closed set, never an `error`: six fatal
file-level codes (the first one wins), six record-level rejects (parsing
continues), two trailer checks (only when nothing was rejected, otherwise the
warning `trailer_not_verified`), and the warnings. `Accepted` is true only with no
fatal, no reject and a verified trailer. The constants in `kredexp.go` carry one
sentence each on what they mean.

The parse is deterministic: no clock, no randomness, no map order in any output,
no `float64`. Inputs are never rounded: a value with more fraction digits than its
field allows is `invalid_amount`.

## The three ambiguities

The interface document contradicts itself in three places. The importer surfaces
each one instead of guessing. The warnings travel onto the invoice's proposal and
into the outbox's `warnings` column; they are warnings, never exceptions.

| Field | Behavior | Why |
|---|---|---|
| `Skonto` (Kopfsatz 13) | carried verbatim in `discount_raw`, never converted, one `ambiguous_field_semantics` per document | the field table says amount, the glossary says percentage rate, and the two readings pay a supplier different sums |
| empty `Kostenstelle` (POS 5) | left empty, never inherited, one `ambiguous_field_semantics` per Positionssatz | section 9.3 says the sending system allows both readings; booking a cost to a center nobody chose is not recoverable |
| sign of a `GU` | the explicit trailing minus, never derived from `Belegart` | the trailer sums are the available evidence, and `04_gutschrift_trailing_minus_ok.txt` fails its trailer under a derived flip |

An empty `Steuercode` (POS 10) looks like the same problem and is not: section 6
states that it inherits the Kopfsatz's `MWST-Code`. It inherits, silently. The
difference between that field and `Kostenstelle` is the whole point of reading the
document rather than pattern-matching it.

## Tests and fixtures

```
go test ./internal/importer/kredexp/
```

The golden fixtures live in `testdata/kredexp/` and belong to this package: four
files, each pinning one documented rule, with their `*.options.json` and
`*.expected.json` siblings. `testdata/kredexp/README.md` says what each one pins.
`seeded_delivery_test.go` also parses the deliveries the seed generator produces,
so the twin's capture can never meet a file this importer refuses.

## Regenerating the fixtures

From the repository root:

```
go run internal/importer/kredexp/gen_fixtures.go           # rewrite the .txt bytes
go test ./internal/importer/kredexp/ -update               # rewrite the .expected.json
go test ./internal/importer/kredexp/                       # verify, then READ the diff
```

`gen_fixtures.go` carries `//go:build ignore` and writes genuine windows-1252 with
CRLF, which an editor cannot. Never commit an expectation you have not read:
`-update` rewrites the expected projection from whatever the importer does now, so
a bug in the importer would become the expectation.
