// Package canonical holds the three file formats the digital twin ships:
// blp-csv-v1, blp-xml-v1 and blp-json-v1. They carry the canonical record shapes
// of every [model.Dataset] - supplier, cost_center, purchase_order,
// purchase_order_line, fx_rate, invoice, invoice_line and outbox_ack - so that
// channel and format are independent axes: the same logical data delivered as a
// CSV file in a batch, as XML over the ingest API or as NDJSON on stdin reaches
// the same state and produces the same state digest.
//
// The three formats share everything except their framing. One field table
// (records.go) defines each dataset's fields, their types, their scales and
// which of them are natural keys; one value reader (cell.go) turns a parsed cell
// into a canonical value and reports the diagnostic when it cannot; and
// [model]'s own validators produce the record-level findings. A defect therefore
// gets the same code and the same message whichever format delivered it, which
// is the property that makes the digest channel-blind.
//
// # Dialect
//
// Every format honors the manifest-declared dialect (BUILD-SPEC 8.1): the
// delimiter, quote and escape scheme, whether a header row is present, the line
// ending, the decimal and thousands separators, the date format, the null token,
// the trim rule and the encoding. The non-CSV formats honor the subset that is
// not about field framing, so a JSON file may carry "15.01.2026" when the
// manifest declares DD.MM.YYYY and may not otherwise.
//
// # Money, keys and dates
//
// In JSON and XML a monetary field is the canonical money object,
// {"amount_minor":134563,"currency":"CHF","scale":2}, or a decimal string. A
// JSON number is [importer.CodeMoneyNotIntegerMinor]: a float64 has already lost
// the value by the time it parses, and an integer without a currency has no
// scale. In CSV a monetary field is a decimal string, parsed with the declared
// separators. The accepted numeric shapes are exactly those of BUILD-SPEC 16.1;
// see [importer.ParseAmount]. An input is never rounded: more fraction digits
// than the field allows is [importer.CodeMoneyScale].
//
// Natural keys are strings and their leading zeros are significant. A key
// delivered as a JSON number is [importer.CodeKeyNotString], not a formatted
// integer, because "0000417" and 417 are different suppliers and only one of them
// exists.
//
// Dates are the canonical YYYY-MM-DD or the manifest-declared pattern. An
// undeclared pattern is [importer.CodeDateFormat] and never a guess.
//
// # Recovery, and why it differs between the formats
//
// Where a broken file resumes is a property of its grammar, not a policy choice:
//
//   - CSV recovers per record. A line with the wrong field count is rejected and
//     the next line is read, because the record separator is unambiguous. An
//     unterminated quote is fatal: from there on, every delimiter and every line
//     break is inside a string or outside one depending on a byte that is
//     missing.
//   - NDJSON recovers per line, for the same reason.
//   - A JSON array is fatal at the first structural break, with the byte offset
//     in the diagnostic. There is no safe resync point: a missing brace makes
//     the following objects members of the wrong container, and skipping to the
//     next "}," is a guess about data.
//   - XML is fatal at the breaking byte, for the same reason.
//
// That asymmetry is a real consequence of the three grammars and one graded
// scenario asserts it. It is also the honest answer to "why not just recover
// everywhere": recovering inside a JSON array means inventing a record boundary
// the format does not have.
//
// # Streaming
//
// All three formats implement [importer.StreamParser] and none of them holds a
// file in memory: a 50'000-record extract parses in bounded memory, and
// [importer.Format.Parse] is defined as ParseStream over a [bytes.Reader]. The
// bound is one record plus the reader's own buffer, with one deliberate
// exception - a single quoted CSV field or a single NDJSON line may itself be
// large, and is capped at [MaxRecordBytes] so a file with one unterminated quote
// cannot be read into memory in its entirety.
package canonical
