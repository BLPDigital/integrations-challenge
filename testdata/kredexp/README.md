# Golden fixtures of the twin's own importer: `kredexp-2.1`

These four files belong to the importer BLP's invoice capture runs inside the twin
(`internal/importer/kredexp`) to read the subsidiary's nightly export. They are
ours and they are not part of your task: your connector never reads, parses or
relays such a file, and nothing in here is scored against you. They are in the
tree because the importer's tests run on them:

```
go test ./internal/importer/kredexp/ -run Golden
```

| Fixture | What it pins |
|---|---|
| `01_minimal_ok.txt` | one document with one line, ASCII only, trailer matching. No diagnostics. `"0004711"` is a string and the client number stays `"0100"`. |
| `02_swiss_numbers_leading_zeros_ok.txt` | `"0004711"` and `"4711"` are **different** documents. Apostrophe grouping, a four-decimal and a three-decimal unit price, quantities `0.125` and `1'250.000`, an empty VAT amount meaning `0.00`, a G/L account with leading zeros. |
| `03_quoted_fields_ok.txt` | quoted fields containing semicolons and doubled quotes, an embedded CRLF **and** an embedded lone LF, plus windows-1252 high bytes: the typographic apostrophe (also used as a thousands separator), an en dash, a euro sign and an umlaut. |
| `04_gutschrift_trailing_minus_ok.txt` | a `GU` credit note whose trailing-minus amounts the trailer sums confirm, plus one document with a non-empty discount field and one line with an empty cost center, so both `ambiguous_field_semantics` warnings fire. |

Each `*.txt` has two siblings: `*.options.json`, the parse options a manifest
would declare, and `*.expected.json`, the documented projection
(`importer.Projection`): canonical documents sorted by key, diagnostic tuples
`(code, line, field)`, the control totals, and `accepted`. Diagnostic messages,
severities, values, record ordinals and the order documents came out in are
deliberately not compared.

## The files are genuine windows-1252 with CRLF

Do not "fix the encoding". A `.txt` re-saved as UTF-8 with Unix line endings
still parses, and stops testing the two things these fixtures exist to test: byte
`0x92` is one typographic apostrophe, not three characters, and a record ends
with CRLF. `03_quoted_fields_ok.txt` carries exactly one bare LF on purpose,
inside a quoted field.

Check a fixture without an editor:

```
od -c testdata/kredexp/03_quoted_fields_ok.txt | head
```

Bytes `222`, `226`, `200`, `374` and `344` in that dump are the typographic
apostrophe, the en dash, the euro sign, `ü` and `ä`. If you see `342 200 231`
instead, the file was re-encoded and needs to be restored from git.
