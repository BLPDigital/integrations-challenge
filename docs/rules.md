# Published rules

Every semantic the grader depends on is here or in `docs/spec.md`, `docs/soap.md` or
`docs/customer/KRED-EXP-2.1.md`. If a code or a rule is not published, it is not graded. Traps in
this exercise are about care, never about clairvoyance.

---

## 1. Numbers

### 1.1 Accepted shapes on input, enumerated

These, and nothing else:

| Shape | Example | Note |
|---|---|---|
| plain decimal | `1234.5` | scale is the number of fraction digits **as written** |
| apostrophe grouping | `1'234.50` | U+0027, groups of three |
| typographic apostrophe grouping | `12’345.67` | U+2019, always accepted regardless of what the manifest declares |
| Swiss shorthand | `12'345.-` | means `.00`. The trailing `-` here is not a sign |
| trailing minus | `1234.00-` | negative |
| leading minus | `-1234.00` | negative |
| accounting parentheses | `(1'234.50)` | negative |
| currency prefix | `CHF 1'234.50` | exactly three uppercase ASCII letters then at least one space |
| negative zero | `-0.00` | normalizes to `0.00`; a zero carries no sign |

Everything else is `E_DECIMAL_FORMAT`. Explicitly invalid: **US grouping `1,234,567.89`** (these
feeds are Swiss and a comma is not a grouping separator here), a leading `+`, an exponent, two
signs, grouping in anything but groups of three, and the empty string.

A declared `decimal_separator` may be `.` or `,`; anything else makes every value in that file
`E_DECIMAL_FORMAT`. A declared `thousands_separator` is accepted **in addition to** the two
apostrophes, never instead of them.

### 1.2 Inputs are never rounded

A value carrying more fraction digits than its field allows is `E_MONEY_SCALE`. It is never
silently rounded, because rounding an input is how a supplier gets paid the wrong amount. An amount
that cannot be expressed exactly in the minor units of its currency is
`E_MONEY_NOT_INTEGER_MINOR`. A money value delivered as a JSON **number** rather than a string is
also `E_MONEY_NOT_INTEGER_MINOR`: by the time a number has parsed, the leading zeros and the exact
scale are already gone.

Minor-unit scales: CHF, EUR, USD, GBP, SEK, DKK, NOK, HUF and IDR are 2; **JPY is 0**. An unknown
currency is `E_CURRENCY_UNKNOWN`.

### 1.3 Rounding happens exactly once

At the FX conversion, and nowhere else. It is **half away from zero** (commercial rounding), to the
minor-unit scale of the target currency:

```
amount_chf = round_half_away_from_zero(gross * rate / rate_factor, 2)
```

`108.225` becomes `108.23`. `-0.005` becomes `-0.01`. Banker's rounding is wrong here, and a graded
assertion depends on the difference.

### 1.4 Pinned graded amounts

| Case | Input | Rate | Factor | Result | What a wrong implementation produces |
|---|---|---|---|---|---|
| GBP invoice | `100.00` | `1.082250` | 1 | **108.23** | `108.22` with banker's rounding or a `float64` path |
| JPY invoice | `250000` | `0.556300` | 100 | **1390.75** | `139075.00` when `RateFactorFrom` is ignored |
| EUR on the DST switch, 2026-03-29 | `1345.63` | `0.931000` | 1 | **1252.78** | `1244.03` when the FX date is truncated to UTC: 8.75 CHF too little |
| credit note | `-100.00` | `1.082250` | 1 | **-108.23** | `-108.22` when rounding is not away from zero |
| exact rate | `1000.00` | `0.500000` | 1 | **500.00** | a rescale that drifts |

`docs/soap.md` carries the FX probe table: which row of the live table wins for each of these, and
why.

---

## 2. Identifiers

**Identity is exact string equality everywhere, after trimming surrounding whitespace. Nothing is
ever normalized by stripping or adding leading zeros.**

- **Supplier numbers are opaque strings.** The creditor master carries both `0000000417` and
  `0000417` as two **different** creditors: the short one is a migration remnant the customer never
  cleaned up. Padding or stripping zeros merges two creditors and pays the wrong supplier. The ERP
  accepts one to ten digits on write and resolves identity by existence in the master, never by
  shape.
- **Purchase order numbers are the one documented normalization.** The same order legitimately
  arrives as `4500001234`, `PO-4500001234` or space-padded (`" 4500002042 "` is what the ERP's own
  list returns), and it also arrives zero-padded: 258 of the invoice feed's references are spelled
  `04500001999` for the order the master calls `4500001999`. Rule: **trim, drop a leading `PO-`, and
  if what remains is all digits compare it numerically**, so `04500001999` and `4500001999` are one
  order; otherwise compare the trimmed string exactly, case-sensitively. This is the ONE identifier
  that is normalized, and the contrast with the two below is the point. A reference with a line
  suffix, `4500001234/00010`, resolves to the header `4500001234` and the line `00010`, and the line
  number keeps its leading zeros because it is a string key.
- **Cost centers are exact too, and the leading zero carries meaning:** `0815` (Werk Wil) and `815`
  (Vertrieb DACH) are two different cost centers.
- The ERP's supplier JSON additionally carries **`legacy_id` as a real JSON number**. It is a
  convenience field and must never be used as a key.

Composite natural keys join their parts with **U+001F** (unit separator): an invoice is
`supplier_number` + U+001F + `supplier_invoice_number`, an invoice line adds U+001F + `line_no`, an
FX row is `base` + U+001F + `quote` + U+001F + `valid_from` + U+001F + `rate_type`. In a URL path
the twin also accepts `|` as a typeable alias for U+001F.

---

## 3. Units of measure

A **unit price is a rate, not a booked amount**, so it is not held to the currency's minor unit: up to
six fraction digits are accepted, and the customer's own KRED-EXP layout specifies two to four for
Einzelpreis. More than six is `E_MONEY_SCALE`. What IS held to the minor unit is every amount that
gets booked: the gross amount, the VAT amount and the line amount.

`GET /erp/v1/uom-conversions` returns the whole table in one page. A base quantity is
`alt_quantity * numerator / denominator`, exact, rescaled to **3 decimals**. An entry with an empty
`material` is the generic rule for that alternative unit; an entry with a material overrides it for
that material.

| `material` | `alt_uom` | `numerator` | `denominator` | `base_uom` |
|---|---|---|---|---|
| | `EA` | 1 | 1 | `EA` |
| | `PCE` | 1 | 1 | `EA` |
| | `STK` | 1 | 1 | `EA` |
| | `CTN` | 12 | 1 | `EA` |
| | `KG` | 1 | 1 | `KG` |
| | `TON` | 1000 | 1 | `KG` |
| | `L` | 1 | 1 | `L` |
| | `M` | 1 | 1 | `M` |
| | `STD` | 1 | 1 | `STD` |
| | `H` | 1 | 1 | `STD` |
| | `PAU` | 1 | 1 | `PAU` |
| | `M2` | 1 | 1 | `M2` |
| | `G` | 1 | 1000 | `KG` |
| `MAT-1000-3` | `CTN` | 1000 | 3 | `EA` |
| `MAT-EIGHTH` | `CTN` | 1 | 8 | `EA` |

- **`PAL` is deliberately absent.** A `PAL` line is `EXC_UOM_UNMAPPABLE`, never a guess.
- `MAT-EIGHTH` in `CTN` converts exactly at three decimals (1 CTN = `0.125` EA) and is accepted with
  the warning `W_UOM_CONVERTED`.
- `MAT-1000-3` in `CTN` does not (1 CTN = 333.333... EA), so it is
  `EXC_UOM_UNCONVERTIBLE`, never a silent round.
- A conversion where `numerator == denominator` is a no-op and raises no warning.

The twin judges units only when a conversion table is loaded. Postings carry base-unit quantities.

`STD`, `PAU` and `M2` are the German-language units the subsidiary's service and lump-sum lines carry,
and `STK` appears in the group ERP's purchase orders as well. They are first-class values of the
twin's canonical model, so no unit mapping table is needed anywhere: what needs a table is the
CONVERSION, and that is the table above.

`TON` and `PAL` are valid units too, which is why a line carrying either is accepted at ingest and
judged at matching: `TON` converts, `PAL` does not. That distinction matters to you: a business
exception a clerk can act on is a different outcome from a record-level parse reject, and the
published exception set is the graded one.

---

## 3a. The company code of a legacy delivery

The subsidiary's `Mandant` in the Vorlaufsatz is its own client number and is **not** the group ERP's
company code. The twin maps it when it applies a legacy delivery:

| `Mandant` | company code |
|---|---|
| `0100` | `CH10` |
| `0200` | `CH20` |

An unmapped `Mandant` is a record-level `E_ENUM_UNKNOWN` on `company_code`, never a passthrough: a
company code that reaches the matching engine unmapped fails against every cost center in the group
and buries the one exception you were meant to see under a hundred identical ones.

The mapping is the TWIN's, not the parser's. Your `kredexp-2.1` importer reports what the file says,
and its golden projection is unaffected by this table.

---

## 4. The match rules, in order

Run on every commit, over the invoices that batch changed, in ascending natural-key order. **The
first failing rule wins, and exactly one blocking exception is raised per invoice.**

| # | Rule | Exception on failure |
|---|---|---|
| 0 | The invoice already exists at version >= 2, i.e. this supplier invoice number was re-delivered with **different** content | `EXC_DUPLICATE_INVOICE_AMENDED` |
| 1 | The supplier exists in the twin | `EXC_SUPPLIER_UNKNOWN` |
| 1 | The supplier is not `blocked` | `EXC_SUPPLIER_BLOCKED` |
| 2 | If `po_number` is non-empty: the order exists | `EXC_PO_NOT_FOUND` |
| 2 | ... belongs to the same supplier | `EXC_PO_SUPPLIER_MISMATCH` |
| 2 | ... is not `CLOSED` or `CANCELLED` | `EXC_PO_CLOSED` |
| 3 | `gross_amount == sum(line_amount) + vat_amount`, **exactly**, no tolerance | `EXC_TOTALS_MISMATCH` |
| 4 | Cost center: the invoice's, else the purchase order's. It must exist, not be blocked, match the invoice's `company_code`, and be valid on the posting date in its half-open `[valid_from, valid_to)` window | `EXC_COST_CENTER_UNKNOWN` |
| 5 | If `currency != "CHF"`: the company code has an FX table at all (CH20 does not), and a `DAILY` row for `(currency, CHF)` whose half-open `[valid_from, valid_to)` interval contains the posting date | `EXC_FX_RATE_MISSING` |
| 6 | Every line's unit maps to a base unit | `EXC_UOM_UNMAPPABLE` |
| 6 | ... and converts exactly at three decimals | `EXC_UOM_UNCONVERTIBLE` |
| 7 | Convert and emit an immutable proposal with `status: "pending"` | none |

**The posting date is the invoice's `document_date`.** That is a deliberate, documented deviation: a
real AP run posts on the run date, but a run date is a clock reading and a graded amount cannot
depend on one.

**The FX interval is evaluated on the Europe/Zurich calendar date of the posting date**, against the
`valid_from` and `valid_to` instants the SOAP table delivers. It is **half-open**: `valid_from` is
included, `valid_to` is excluded. An open-ended `valid_to` (`xsi:nil="true"`) matches everything
from `valid_from` onward. This is where the 2026-03-29 DST boundary bites; see `docs/soap.md`.

CH20 has no exchange rate table in this landscape, so a foreign-currency CH20 invoice is
`EXC_FX_RATE_MISSING` even when a rate for its currency and date is on file for CH10. Converting it
with another subsidiary's rate would post an amount nobody quoted.

### 4.1 The complete, closed set of blocking exception codes

Read back live from `GET /admin/v1/exceptions` in the `codes` field. There are twelve: eleven the
matching raises, and one an acknowledgment raises.

```
EXC_COST_CENTER_UNKNOWN        EXC_DUPLICATE_INVOICE_AMENDED  EXC_FX_RATE_MISSING
EXC_PO_CLOSED                  EXC_PO_NOT_FOUND               EXC_PO_SUPPLIER_MISMATCH
EXC_SUPPLIER_BLOCKED           EXC_SUPPLIER_UNKNOWN           EXC_TOTALS_MISMATCH
EXC_UOM_UNCONVERTIBLE          EXC_UOM_UNMAPPABLE             E_ACK_REJECTED
```

`E_ACK_REJECTED` is the twelfth and it is not a matching verdict: it is what the twin records when
you acknowledge a proposal as `rejected`, carrying the ERP's own code in `details`. It appears in
the same queue and in the same `codes` field, so a client that hard-codes eleven is wrong about the
one code its own postings produce.

Two further codes are raised at the ack stage, against a `proposal` rather than an invoice:
`E_ACK_CONFLICT` (two different ERP document numbers acknowledged for one proposal, the double-post
detector) and `E_ACK_REJECTED` (the ERP refused the posting; the exception carries the ERP's own
code in its details and the invoice returns to the open queue).

The graded exception set is read from the twin's own open queue, keyed by
`(subject_key, code)`, and compared as a **symmetric difference**: a missing pair and a spurious pair
cost exactly the same, so over-rejecting everything scores as badly as under-rejecting.

### 4.2 The amended-invoice rule

Re-delivery of the same supplier invoice number with **different** content is **not** a second
posting. The twin raises one blocking `EXC_DUPLICATE_INVOICE_AMENDED` carrying `gross_amount_new`,
`gross_amount_previous`, `gross_amount_delta`, the `proposal_id` and the `erp_document_number` the
first version was already posted under. Auto-posting an amended invoice a second time is a duplicate
in a customer's ledger, and there is no reversal endpoint. A byte-identical re-delivery is a
different thing entirely: it is `skipped_unchanged`, or a whole-batch `replayed`, and changes
nothing.

---

## 5. Record-level codes

### 5.1 Errors, which reject the record and continue the file

| Code | Meaning |
|---|---|
| `E_KEY_MISSING` | an empty natural-key component |
| `E_KEY_NOT_STRING` | a natural key delivered as a JSON number |
| `E_FIELD_REQUIRED` | an empty mandatory field |
| `E_FIELD_INVALID` | well-formed alone, wrong in context |
| `E_ENUM_UNKNOWN` | a value outside a closed enumeration |
| `E_CURRENCY_UNKNOWN` | a currency outside the fixed table |
| `E_DATE_FORMAT` | not an RFC 3339 calendar date, nor the declared pattern. Never guessed |
| `E_UOM_UNKNOWN` | a unit outside the known set |
| `E_NUMBER_FORMAT` | a non-monetary integer field that is not a plain integer |
| `E_DECIMAL_FORMAT` | outside the shapes of section 1.1 |
| `E_MONEY_SCALE` | more fraction digits than the field allows |
| `E_MONEY_NOT_INTEGER_MINOR` | not exact in the minor units of its currency, or delivered as a JSON number |
| `E_ENCODING` | a byte the declared encoding cannot represent, reported at the offending record and never replaced by U+FFFD |
| `E_MALFORMED_DOCUMENT` | a structural break with no safe resync point. Fatal for the file |
| `ENCODING_CONFLICT` | an XML prolog and a manifest that name different encodings. The prolog wins, the disagreement is still reported |
| `E_VERSION_CONFLICT` | a failed `if_version`, `If-Match` or `If-None-Match` precondition |

### 5.2 Warnings

| Code | Meaning |
|---|---|
| `ambiguous_field_semantics` | a field the source specification leaves genuinely undecided. Always carried with the value verbatim |
| `W_UOM_CONVERTED` | a line quantity was converted to its base unit. The conversion is exact; the warning exists because a converted quantity is not the one the sender wrote |
| `W_ACK_REPLAY` | a `posted` ack repeating the document number already acknowledged. Correct behavior for an at-least-once connector |
| `W_UNDECLARED_FILE` | the batch directory holds a data file the manifest does not name |
| `E_FIELD_UNKNOWN` | a field the record shape does not define. A warning despite the prefix: an extra column is usually a sender adding a field |
| `trailer_not_verified` | the trailer could not be checked because a record was rejected, so the computed sums are known to be incomplete |
| `diagnostics_truncated` | more than 1'000 findings on one file. Always sorts last |

### 5.3 Warnings are never exceptions

A warning never enters the open exception queue and never changes an exit code. It travels on the
record, onto the proposal, into the receipt and into the twin UI. An invoice carrying
`ambiguous_field_semantics` **still produces a proposal**: silence is what is punished here, not
caution.

### 5.4 The three documented ambiguities

`docs/customer/KRED-EXP-2.1.md` is genuinely self-contradictory in three places. A correct
implementation **surfaces** each one rather than guessing. Each silent guess is an automatic
disqualifier.

1. **`Skonto`** (Kopfsatz field 13). The field table calls it an amount, the glossary calls it a
   percentage rate. Carry it verbatim in `discount_raw`, never convert it, and emit
   `ambiguous_field_semantics` once per document with a non-empty Skonto. Converting it to money
   changes what a supplier is paid.
2. **An empty `Kostenstelle` on a Positionssatz.** The specification does not say whether it
   inherits from the Kopfsatz. Leave it empty and emit `ambiguous_field_semantics`.
3. **The sign convention for `GU` (Gutschrift).** The specification defines a trailing minus for
   negative amounts *and*, separately, a credit-note document type. Deriving a sign flip from
   `Belegart` contradicts the trailer sums, which is the available evidence. **Trust the explicit
   sign; never derive one from the document type.**

---

## 6. Data minimization, graded

Supplier bank data (`iban`, `bic`), any IBAN-shaped string, client secrets and issued bearer tokens
must never appear in stdout, your connector's logs, `run.json`, `postings.csv`, `exceptions.csv` or
any state file.

**Masking is acceptable and preferred over omission**, so the record stays recognizable to whoever
reads the report: `CH93****2957`.

The assertion matches on **full values only**: the full seeded IBANs, the full client secrets, the
full issued tokens. No fragment matching and no entropy heuristics, so a masked form passes as
promised and a scored assertion cannot produce a false positive on correct work.
