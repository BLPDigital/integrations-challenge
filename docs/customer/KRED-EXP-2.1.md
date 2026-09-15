> **Note for the reader (added by BLP Digital, not part of the customer document).** This is an
> English translation of the German original `Schnittstellenbeschreibung_KRED-EXP_v2.1.docx`
> (Steinbach Industrie AG, IT Applications). Field names are left in German because they appear in
> the sender's configuration and in every support ticket. An English glossary follows each table.
> The translation is unofficial. In case of doubt the German original applies.
> Sections marked "(unchanged since v1.4)" were carried over by the customer without review.

# Schnittstellenbeschreibung: Kreditoren-Sammelexport (KRED-EXP)

| | |
|---|---|
| Document | Schnittstellenbeschreibung KRED-EXP |
| Version | 2.1 |
| Status | freigegeben (released) |
| Last edited | 2019-11-04 |
| Owner | Steinbach Industrie AG, IT Applications, Wil SG |
| Contact | ap-schnittstellen@steinbach-industrie.example (group mailbox, no individual support) |
| Distribution | receiving partners only |

## Change history (Änderungshistorie)

| Version | Date | Change |
|---|---|---|
| 1.4 | 2011-06-20 | Initial release for the payment run interface |
| 2.0 | 2016-03-02 | Positionssätze introduced, Vorlaufsatz added |
| 2.1 | 2019-11-04 | Feld `Steuercode` added to Positionssatz. Feld `Lieferschein` removed (was Feld 7 in v2.0, no longer supplied, do not use). TODO: describe the handling of Gutschriften in more detail (open since 2019-11) |

## 1. Purpose and scope

The Kreditoren-Sammelexport transfers supplier invoices (Kreditorenbelege) that were captured in the
subsidiary's local accounting system to a receiving partner system for further processing. One file
contains one export run. A run contains all invoices captured since the previous run.

Out of scope: master data (Lieferantenstamm, Sachkonten, Kostenstellen) is not part of this
interface. Master data is maintained centrally in the group ERP and must be read from there.

## 2. Transport and file handling (unchanged since v1.4)

- Transport: SFTP, directory `/out/kred/`. Read access only.
- File name: `KRED_<Mandant>_<JJJJMMTT>_<Lauf>.txt`, for example `KRED_0100_20260131_001.txt`.
  `<Lauf>` is a three digit run counter that restarts at `001` every day.
- After the data file is completely written, the sender writes an empty sentinel file with the same
  base name and the extension `.ok`. Do not read a data file before its `.ok` file exists.
- Files remain in the directory for 30 days and are not deleted by the sender.
- If a run was rejected by the receiver, the same file is delivered again unchanged, under the same
  name. Re-delivery of an already processed file is possible and is not signalled.
- Maximum file size in normal operation is about 20 MB. There is no technical limit.

## 3. General format rules

| Property | Value |
|---|---|
| Zeichensatz (character set) | Windows-1252 (CP1252). No byte order mark. |
| Feldtrennzeichen (field separator) | Semicolon `;` |
| Satzende (record terminator) | CRLF |
| Textbegrenzer (text delimiter) | Double quote `"`, only for fields that contain `;`, `"` or a line break. A `"` inside a quoted field is doubled. |
| Kopfzeile (column header row) | none |
| Feldauffüllung (padding) | none. Fields are not padded to a fixed width. |
| Dezimaltrennzeichen (decimal separator) | Point `.` |
| Tausendertrennzeichen (thousands separator) | Apostroph (apostrophe), optional |
| Datumsformat (date format) | `TTMMJJ` (six digits, two digit year). Two digit years 70 to 99 are 19xx, years 00 to 69 are 20xx. |
| Zeitformat (time format) | `TTMMJJHHMM` (ten digits), used only in the Vorlaufsatz |
| Negative Beträge (negative amounts) | Minus sign after the last digit, for example `1'234.50-` |

Every record starts with the field `Satzart` (record type). The Satzart is always written in capital
letters. Valid Satzarten are `VORLAUF`, `KOPF`, `POS` and `NACHLAUF`. A file contains exactly one
Vorlaufsatz as the first record and exactly one Nachlaufsatz as the last record. Between them, each
Kopfsatz is followed by its Positionssätze. Positionssätze of different Belege are never interleaved.

Amount fields (Betragsfelder) always carry exactly two decimals, including `0.00`. `Einzelpreis`
carries two to four decimals. `Menge` carries up to three decimals.

## 4. Vorlaufsatz (`VORLAUF`)

| Feld | Feldbezeichnung | Type | Length | M/O | Remark |
|---|---|---|---|---|---|
| 1 | Satzart | A | 7 | M | constant `VORLAUF` |
| 2 | Formatversion | A | 3 | M | `2.1` |
| 3 | Mandant | N | 4 | M | company code, leading zeros are part of the value |
| 4 | Erstellungszeitpunkt | A | 10 | M | `TTMMJJHHMM`, local time Europe/Zurich |
| 5 | Währung | A | 3 | M | default currency of the run, ISO 4217 |
| 6 | Absender | A | 10 | M | sender id, currently always `SUBSG01` |
| 7 | Empfänger | A | 10 | M | receiver id as agreed |
| 8 | Testkennzeichen | A | 1 | M | `P` = Produktion, `T` = Test |

Glossary: Formatversion = format version, Mandant = company code, Erstellungszeitpunkt = creation
timestamp, Währung = currency, Absender = sender, Empfänger = receiver, Testkennzeichen = test
indicator.

## 5. Kopfsatz (`KOPF`)

| Feld | Feldbezeichnung | Type | Length | M/O | Remark |
|---|---|---|---|---|---|
| 1 | Satzart | A | 4 | M | constant `KOPF` |
| 2 | Belegnummer | A | 20 | M | supplier invoice number exactly as printed on the invoice. Leading zeros are part of the number and must be preserved. |
| 3 | Lieferantennummer | A | 10 | M | Kreditorennummer in the group ERP, zero padded |
| 4 | Belegart | A | 2 | M | `RE` = Rechnung, `GU` = Gutschrift |
| 5 | Belegdatum | D | 6 | M | invoice date, `TTMMJJ` |
| 6 | Eingangsdatum | D | 6 | O | date of receipt in the subsidiary, `TTMMJJ` |
| 7 | Bestellnummer | A | 20 | O | purchase order number, optionally with line suffix, for example `4500001234/00010` |
| 8 | Währung | A | 3 | O | if empty, the currency of the Vorlaufsatz applies |
| 9 | Bruttobetrag | B | 18 | M | invoice total including VAT |
| 10 | MWST-Betrag | B | 18 | O | VAT amount. Empty means `0.00`. |
| 11 | MWST-Code | A | 4 | O | tax code of the local system, for example `V81`, `V26`, `V0` |
| 12 | Zahlungsziel | N | 3 | O | payment term in days |
| 13 | Skonto | B | 5 | O | Skontobetrag in der Währung des Belegs (discount amount in the currency of the document) |
| 14 | Skontotage | N | 3 | O | discount period in days |
| 15 | Kostenstelle | A | 10 | O | cost center for the whole document, zero padded |
| 16 | Text | A | 80 | O | free text from invoice capture, quoted if required |

Glossary: Belegnummer = document number, Lieferantennummer = supplier number, Belegart = document
type, Belegdatum = document date, Eingangsdatum = date of receipt, Bestellnummer = purchase order
number, Bruttobetrag = gross amount, MWST-Betrag = VAT amount, MWST-Code = tax code, Zahlungsziel =
payment term in days, **Skonto = Skontosatz in Prozent (early payment discount rate in percent)**,
Skontotage = discount days, Kostenstelle = cost center, Text = free text.

Rule: `Bruttobetrag` equals the sum of the `Positionsbetrag` of all Positionssätze of the Beleg plus
`MWST-Betrag`.

Gutschriften (`GU`) are delivered with the sign convention of section 3. See also the open TODO in
the change history.

## 6. Positionssatz (`POS`)

| Feld | Feldbezeichnung | Type | Length | M/O | Remark |
|---|---|---|---|---|---|
| 1 | Satzart | A | 3 | M | constant `POS` |
| 2 | Belegnummer | A | 20 | M | same value as Feld 2 of the preceding Kopfsatz |
| 3 | Positionsnummer | N | 5 | M | zero padded, ascending in steps of 10, unique within the Beleg |
| 4 | Sachkonto | A | 10 | M | G/L account, zero padded |
| 5 | Kostenstelle | A | 10 | O | cost center of the position |
| 6 | Menge | Q | 15 | M | quantity, up to three decimals |
| 7 | Einheit | A | 4 | M | unit of measure, for example `STK`, `KG`, `STD`, `M2`, `PAU` |
| 8 | Einzelpreis | B | 18 | M | unit price, two to four decimals |
| 9 | Positionsbetrag | B | 18 | M | line amount excluding VAT. Authoritative. Rounding differences against `Menge` times `Einzelpreis` are possible and are not an error. |
| 10 | Steuercode | A | 4 | O | tax code of the position. If empty, the `MWST-Code` of the Kopfsatz applies. |
| 11 | Text | A | 80 | O | position text, quoted if required |

Glossary: Positionsnummer = line number, Sachkonto = G/L account, Menge = quantity, Einheit = unit of
measure, Einzelpreis = unit price, Positionsbetrag = line amount, Steuercode = tax code.

## 7. Nachlaufsatz (`NACHLAUF`)

| Feld | Feldbezeichnung | Type | Length | M/O | Remark |
|---|---|---|---|---|---|
| 1 | Satzart | A | 8 | M | constant `NACHLAUF` |
| 2 | Anzahl Kopfsätze | N | 8 | M | number of Kopfsätze in the file |
| 3 | Anzahl Positionssätze | N | 8 | M | number of Positionssätze in the file |
| 4 | Summe Bruttobetrag | B | 18 | M | sum of Feld 9 of all Kopfsätze, over all currencies |
| 5 | Summe Positionsbetrag | B | 18 | M | sum of Feld 9 of all Positionssätze |

Glossary: Anzahl Sätze = number of records, Summe = total.

The receiver has to verify the Nachlaufsatz. A file whose control totals do not match must not be
posted. In that case the sender is informed by e-mail (group mailbox, see document control) and
delivers the run again.

## 8. Example (Anhang A)

```
VORLAUF;2.1;0100;3101260612;CHF;SUBSG01;BLP;P
KOPF;0004711;0000105;RE;150126;180126;4500001234/00010;CHF;1'345.63;100.83;V81;30;2.00;10;0012340;"Wartung Presse 3; Rahmenvertrag 2026"
POS;0004711;00010;0006400;;12.000;STK;45.5000;546.00;;"Ersatzteile, Charge ""A-12"""
POS;0004711;00020;0006810;0012350;6.500;STD;105.0000;682.50;V81;Montage
POS;0004711;00030;0006810;0012350;1.000;PAU;16.3000;16.30;;Kleinmaterial
NACHLAUF;1;3;1'345.63;1'244.80
```

## 9. Open points and known deviations

1. The Skonto field is documented as an amount in the record layout and as a percentage rate in the
   glossary. The value in Anhang A (`2.00` with `Skontotage` `10`) is compatible with both readings.
   This has been open with the application owner since 2019 and is not expected to be clarified.
   Receivers are asked to agree the reading bilaterally and to document it.
2. Feld 2 of the Nachlaufsatz is called "Anzahl Kopfsätze" in the record layout and "Anzahl Sätze" in
   the glossary. Both wordings occur in older correspondence.
3. Whether an empty `Kostenstelle` in the Positionssatz means "use the Kostenstelle of the Kopfsatz"
   or "no cost center assigned" is not defined in this document. The local system allows both.
4. `Zahlungsziel` is counted from a base date that is not named in this document.
5. The `Testkennzeichen` marks test deliveries. What the receiver does with a test delivery is not
   part of this interface.
6. Exports produced before 2020 may still use the v2.0 layout of the Positionssatz (ten fields,
   without `Steuercode`). Such files are not re-exported and are out of scope for new receivers.
7. Text fields are entered manually. Typographic characters from the Windows character set
   (typographic apostrophe, en dash, euro sign) occur regularly and are transferred unchanged.
