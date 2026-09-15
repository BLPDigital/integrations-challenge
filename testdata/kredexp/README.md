# Public golden fixtures: `kredexp-2.1`

Four deliveries of the legacy Kreditoren-Sammelexport, each with the projection
your importer has to produce. They are what `go test ./internal/importer/ -run
Golden` compares against, and they are the only fixtures you get to see; the
hidden ones re-combine the same documented mechanisms and introduce nothing new.

| Fixture | What it pins |
|---|---|
| `01_minimal_ok.txt` | one Kopfsatz with one Positionssatz, ASCII only, trailer matching. No diagnostics. `"0004711"` is a string and `Mandant` stays `"0100"`. |
| `02_swiss_numbers_leading_zeros_ok.txt` | `"0004711"` and `"4711"` are **different** documents. Apostrophe grouping, a four-decimal and a three-decimal `Einzelpreis`, `Menge` `0.125` and `1'250.000`, an empty `MWST-Betrag` meaning `0.00`, `Sachkonto` with leading zeros. |
| `03_quoted_fields_ok.txt` | quoted fields containing semicolons and doubled quotes, an embedded CRLF **and** an embedded lone LF, plus windows-1252 high bytes: the typographic apostrophe (also used as a thousands separator), an en dash, a euro sign and an umlaut. |
| `04_gutschrift_trailing_minus_ok.txt` | a `GU` credit note whose trailing-minus amounts the trailer sums confirm, plus one document with a non-empty `Skonto` and one Positionssatz with an empty `Kostenstelle`, so both `ambiguous_field_semantics` warnings fire. |

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

Bytes `222`, `226`, `200`, `374` and `344` in that dump are `’`, `–`, `€`, `ü`
and `ä`. If you see `342 200 231` instead, the file was re-encoded and needs to
be restored from git.
