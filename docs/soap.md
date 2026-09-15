# The SOAP channel: `GetExchangeRateTable`

## Read this first. It saves an hour

**There is one operation.** You will not need SOAP tooling, a generated client, a WSDL compiler or a
SOAP library in any language. String-template the request envelope below, POST it, and parse the
response with whatever XML reader your language ships. The WSDL at
`GET /soap/FinancialReferenceDataService?wsdl` is reference material; nothing requires you to read
it, and a code generator pointed at it will cost you more time than it saves.

**HTTP status is never the success signal.** In SOAP 1.1 the fault *is* the answer: a business
refusal arrives as `HTTP 200` carrying `soap:Fault`. A client that looks at the status code will
happily treat a refusal as a rate table. Look for a `Fault` element first, always.

**Match elements by namespace URI and local name, never by prefix.** The response's prefixes are
deliberately not the documentation's: the envelope uses `S:` where this document writes `soap:`, the
payload sits in a **default** namespace where this document writes `fin:`, and `ns2:` is declared
**mid-document on individual children** rather than once at the top. Every one of those is legal
XML and all of them will break a client that string-matches `fin:Rate`.

**The numbers here are formatted differently from the REST API.** This channel speaks the ERP host
locale: **decimal comma**, six fraction digits. One row's value is surrounded by pretty-printer
whitespace, so trim before you parse. And `RateFactorFrom` is a per-unit divisor, so the rate alone
is not the conversion.

The response declares `ISO-8859-1` in the XML prolog and the HTTP `Content-Type` carries **no
charset parameter**, so hand your parser bytes, not a string you already decoded as UTF-8.
`RateProvider` contains `Zürcher Kantonalbank`. A mangled provider name is a minor deduction; an
aborted parse is a hard failure. The umlaut is confined to that one cosmetic field so it can never
cascade into a wrong amount.

---

## The request, copy-pasteable

Headers, exactly:

```
POST /soap/FinancialReferenceDataService HTTP/1.1
Content-Type: text/xml; charset=utf-8
SOAPAction: "urn:blp-erp:finref:1.0/GetExchangeRateTable"
```

| Header mistake | What you get |
|---|---|
| `Content-Type: application/soap+xml` | bare **`415`**, empty body, no fault at all. That is SOAP 1.2's media type and it never reached the SOAP 1.1 stack |
| `SOAPAction` **absent** | **`HTTP 500`** with a fault, `ERP-FX-500`, `PERMANENT` |
| `SOAPAction` present but unquoted or wrong | **`HTTP 200`** with a fault, `ERP-FX-403`, `PERMANENT`. The double quotes are part of the value |

Body:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:fin="urn:blp-erp:finref:1.0"
               xmlns:cmn="urn:blp-erp:common:1.0"
               xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
  <soap:Header>
    <wsse:Security soap:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>FINREF_SVC</wsse:Username>
        <wsse:Password>$SOAP_PASSWORD</wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soap:mustUnderstand="1">
      <cmn:CorrelationId>run_7f3c1a</cmn:CorrelationId>
    </fin:RequestContext>
  </soap:Header>
  <soap:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH10</fin:CompanyCode>
    </fin:GetExchangeRateTable>
  </soap:Body>
</soap:Envelope>
```

- Credentials come from `SOAP_USERNAME` and `SOAP_PASSWORD`, which are **not** the REST client
  credentials. A wrong or absent `UsernameToken` is fault `ERP-FX-401`.
- `cmn:CorrelationId` is mandatory and non-empty; use your `--run-id`. Absent or empty is
  `ERP-FX-400`. It must be representable in ISO-8859-1, because it is echoed into the response.
- A header element this service does not understand carrying `mustUnderstand="1"` is
  `soap:MustUnderstand` / `ERP-FX-402`, checked before the body is looked at.
- `CompanyCode` is mandatory and is honored. **`RateType`, `ValidFrom` and `ValidTo` are accepted
  and silently IGNORED** (documented v1.0 backward compatibility). The full rolling window always
  comes back, so a client that believes it filtered server-side will post rates it never asked for.
- The request parser is lenient about *your* prefixes and namespaces: write the envelope however
  you like. The response is where strictness matters.

The service accepts a request body in UTF-8, ISO-8859-1 or windows-1252.

## The response, real, captured from the running server

`--scenario S1 --seed 20260416`, `CompanyCode=CH10`. 600 rows, of which **five** are shown here;
the rest of `<ExchangeRateTable>` is elided at the marked point. Everything outside the elision is
byte-exact, including the whitespace.

```
HTTP/1.1 200 OK
Content-Type: text/xml
Transfer-Encoding: chunked
```

```xml
<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Header>
    <ResponseContext xmlns="urn:blp-erp:finref:1.0">
      <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">run_7f3c1a</ns2:CorrelationId>
      <ns2:SnapshotToken xmlns:ns2="urn:blp-erp:common:1.0">snap-405d9022cf4cbb0b</ns2:SnapshotToken>
      <RowCount>600</RowCount>
      <Truncated>false</Truncated>
    </ResponseContext>
  </S:Header>
  <S:Body>
    <GetExchangeRateTableResponse xmlns="urn:blp-erp:finref:1.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
      <CompanyCode>CH10</CompanyCode>
      <RateProvider>Zürcher Kantonalbank</RateProvider>
      <ExchangeRateTable>
        <ExchangeRate Sequence="9">
          <ns2:CurrencyFrom xmlns:ns2="urn:blp-erp:common:1.0">EUR</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="urn:blp-erp:common:1.0">CHF</ns2:CurrencyTo>
          <RateType>DAILY</RateType>
          <Rate>0,931000</Rate>
          <RateFactorFrom>1</RateFactorFrom>
          <ValidFrom>2026-03-29T00:00:00+01:00</ValidFrom>
          <ValidTo>2026-03-30T00:00:00+02:00</ValidTo>
          <Status>ACTIVE</Status>
          <Comment xsi:nil="true"/>
        </ExchangeRate>
        <ExchangeRate Sequence="4">
          <ns2:CurrencyFrom xmlns:ns2="urn:blp-erp:common:1.0">EUR</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="urn:blp-erp:common:1.0">CHF</ns2:CurrencyTo>
          <RateType>DAILY</RateType>
          <Rate>0,900000</Rate>
          <RateFactorFrom>1</RateFactorFrom>
          <ValidFrom>2026-03-29T00:00:00+01:00</ValidFrom>
          <ValidTo>2026-03-30T00:00:00+02:00</ValidTo>
          <Status>ACTIVE</Status>
          <Comment>korrigiert</Comment>
        </ExchangeRate>
        <ExchangeRate Sequence="3">
          <ns2:CurrencyFrom xmlns:ns2="urn:blp-erp:common:1.0">GBP</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="urn:blp-erp:common:1.0">CHF</ns2:CurrencyTo>
          <RateType>DAILY</RateType>
          <Rate>1,082250</Rate>
          <RateFactorFrom>1</RateFactorFrom>
          <ValidFrom>2026-03-01T00:00:00+01:00</ValidFrom>
          <ValidTo xsi:nil="true"/>
          <Status>ACTIVE</Status>
          <Comment></Comment>
        </ExchangeRate>
        <ExchangeRate Sequence="2">
          <ns2:CurrencyFrom xmlns:ns2="urn:blp-erp:common:1.0">JPY</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="urn:blp-erp:common:1.0">CHF</ns2:CurrencyTo>
          <RateType>DAILY</RateType>
          <Rate>
            0,556300
          </Rate>
          <RateFactorFrom>100</RateFactorFrom>
          <ValidFrom>2026-03-16T00:00:00+01:00</ValidFrom>
          <ValidTo>2026-03-17T00:00:00+01:00</ValidTo>
          <Status>ACTIVE</Status>
          <Comment></Comment>
        </ExchangeRate>
        <ExchangeRate Sequence="2">
          <ns2:CurrencyFrom xmlns:ns2="urn:blp-erp:common:1.0">DKK</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="urn:blp-erp:common:1.0">CHF</ns2:CurrencyTo>
          <RateType>DAILY</RateType>
          <Rate>0,122449</Rate>
          <RateFactorFrom>1</RateFactorFrom>
          <ValidFrom>2026-03-29T00:00:00+01:00</ValidFrom>
          <ValidTo>2026-03-30T00:00:00+02:00</ValidTo>
          <Status>DELETED</Status>
          <Comment></Comment>
        </ExchangeRate>
        <!-- 595 further <ExchangeRate> elements elided in this document -->
      </ExchangeRateTable>
    </GetExchangeRateTableResponse>
  </S:Body>
</S:Envelope>
```

The header carries the meaning. `SnapshotToken` identifies the snapshot and is stable across runs
for a given scenario, seed, company code and row count; put it in `run.json` as
`fx_snapshot_token`, together with `fx_correlation_id` and `fx_truncated`. **`Truncated=true` means
the table you received is incomplete, and a correct client refuses to post on it.** A hidden
scenario returns `Truncated=true`; `RowCount` alone will not tell you, because it counts the rows
you were sent, not the rows that exist.

The 600 rows cover EUR (111), USD (110), JPY (110), DKK (110), SEK (109) and GBP (50) against CHF.
582 are `DAILY` and 18 are `MONTHLY_AVG`; 490 are `ACTIVE` and 110 are `DELETED`.

## Row semantics. Get these six right and the FX work is done

| Rule | Detail |
|---|---|
| **Highest `Sequence` wins** | For an otherwise identical key `(CurrencyFrom, CurrencyTo, RateType, ValidFrom)`, the row with the highest `Sequence` attribute supersedes the others. There are two such pairs in the table and they are laid out so that **both first-wins and last-wins are wrong**: for EUR on 2026-03-29 the winner (`Sequence 9`) comes **before** the superseded row (`Sequence 4`, `Comment: korrigiert`); for USD on 2026-03-02 the superseded row (`Sequence 1`, `Comment: storniert`, rate `0,810000`) comes **before** the winner (`Sequence 8`). Document order carries no authority |
| **`Status=DELETED` is a soft delete** | Drop those rows entirely. They are still well-formed, so they parse; they are simply not rates. **DKK exists only as `DELETED`** (all 110 DKK rows), so every DKK invoice must land in `EXC_FX_RATE_MISSING` |
| **`MONTHLY_AVG` is not a daily rate** | Every currency has three `MONTHLY_AVG` rows, one per month, whose intervals span whole months. They exist for period-end reporting and must never be used for a daily conversion. They are also the reason a "nearest interval" search silently succeeds with the wrong number |
| **`xsi:nil="true"` on `ValidTo` means open ended** | The interval runs from `ValidFrom` forward with no end. This is the GBP row. Anything else unusable in `ValidTo` (absent, or an empty element) is one reject class. But a `xsi:nil` **`Comment`** must produce no complaint at all, so a blanket "nil is an error" rule fails on a perfectly good row: the EUR winner above carries exactly that |
| **Half-open interval, on the Europe/Zurich calendar date** | `[ValidFrom, ValidTo)`: `ValidFrom` included, `ValidTo` excluded. `ValidFrom` and `ValidTo` are full RFC 3339 instants with a real offset, `+01:00` in winter and `+02:00` in summer. Compare **Zurich calendar dates**, not UTC instants: the instant that starts 2026-03-29 in Zurich is `2026-03-28T23:00:00Z`, so truncating to a UTC date moves the day back and silently selects the previous day's rate |
| **`RateFactorFrom` is a per-unit divisor** | `amount_chf = round_half_away_from_zero(gross * Rate / RateFactorFrom, 2)`. It is `1` for EUR, USD, GBP, SEK and DKK and **`100` for JPY**, because JPY is quoted per 100 units. Carry `Rate` and `RateFactorFrom` as two separate values into the twin; dividing them in the connector loses precision before the multiplication that needs it |

Plus the format rule: **`Rate` uses a decimal comma** and six fraction digits, and one row's value is
wrapped in pretty-printer newlines and indentation. Trim, then replace the comma, then parse
exactly. Never through a `float64`.

## The FX probe table

Six `(currency, posting date)` pairs, computed by calling the live server and applying the rules
above. `--scenario S1 --seed 20260416`, `CompanyCode=CH10`. If your connector and the twin agree
with this table, the FX half of the challenge is correct.

| # | Currency | Posting date | Winning row | Rate | Factor | Source amount | **CHF** | A wrong implementation gets |
|---|---|---|---|---|---|---|---|---|
| 1 | GBP | 2026-03-15 | `Sequence 3`, `ValidFrom 2026-03-01T00:00:00+01:00`, `ValidTo xsi:nil` (open ended) | `1,082250` | 1 | 100.00 | **108.23** | `108.22` with banker's rounding or a `float64`; `EXC_FX_RATE_MISSING` if `xsi:nil` is treated as unusable |
| 2 | JPY | 2026-03-16 | `Sequence 2`, `2026-03-16` to `2026-03-17`, the pretty-printed row | `0,556300` | **100** | 250000 | **1390.75** | `139075.00` when `RateFactorFrom` is ignored: a factor of 100 too much. A parse failure if the surrounding whitespace is not trimmed |
| 3 | EUR | **2026-03-29** (the DST switch) | `Sequence 9`, `2026-03-29T00:00:00+01:00` to `2026-03-30T00:00:00+02:00` | `0,931000` | 1 | 1'345.63 | **1252.78** | `1244.03` when the FX date is truncated to UTC and the 2026-03-28 rate `0,924500` is picked: 8.75 CHF too little. `1211.07` when the superseded `Sequence 4` row `0,900000` is used because it came last |
| 4 | USD | 2026-03-02 | `Sequence 8`, `2026-03-02` to `2026-03-03` | `0,879540` | 1 | 1'000.00 | **879.54** | `810.00` with first-wins, from the `storniert` `Sequence 1` row that precedes it in document order |
| 5 | DKK | 2026-03-15 | **none.** All 110 DKK rows are `Status=DELETED` | | | 4'812.00 | **nothing is posted:** `EXC_FX_RATE_MISSING` | `589.22` from the `DELETED` 2026-03-29 row `0,122449`, or any other DELETED row, which posts a rate the group withdrew |
| 6 | EUR | 2026-03-15 | `Sequence 5`, `2026-03-15` to `2026-03-16`, `RateType DAILY` | `0,930039` | 1 | 1'345.63 | **1251.49** | `1252.69` from the March `MONTHLY_AVG` row `0,930929`, which covers `2026-03-01` to `2026-04-01` and looks like a match to an interval search that ignores `RateType` |

A seventh case that is not an amount: **`CompanyCode=CH20` always faults `ERP-FX-014 PERMANENT`.**
The seeded `KRED_0200` delivery contains CH20 invoices, so every candidate meets this fault. Retrying
it is a graded mistake, and a foreign-currency CH20 invoice is `EXC_FX_RATE_MISSING` however many
times you ask.

## Faults

A fault body, real, from `CompanyCode=CH20`, returned with **`HTTP 200`**:

```xml
<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Body>
    <S:Fault>
      <faultcode>S:Client</faultcode>
      <faultstring>no exchange rate table is configured for this company code</faultstring>
      <detail>
        <FinRefFault xmlns="urn:blp-erp:finref:1.0">
          <ns2:Code xmlns:ns2="urn:blp-erp:common:1.0">ERP-FX-014</ns2:Code>
          <Severity>PERMANENT</Severity>
          <Element>CompanyCode</Element>
          <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">run_7f3c1a</ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>
```

`faultcode`, `faultstring` and `detail` are unqualified, as SOAP 1.1 requires. The
machine-readable half lives inside `detail`: read `Code` and **`Severity`**, never the prose.

| `Code` | Severity | HTTP | When |
|---|---|---|---|
| `ERP-FX-014` | `PERMANENT` | 200 | `CompanyCode` has no rate table. **Never retry** |
| `ERP-FX-503` | `RETRYABLE` | 200 | the snapshot is being rebuilt. Carries `<RetryAfterSeconds>2</RetryAfterSeconds>`. Fires on the **first** delivery of each logical call, so the retry of the same `(operation, CompanyCode)` always succeeds |
| `ERP-FX-400` | `PERMANENT` | 200 | a mandatory element is missing. `Element` names it, for example `cmn:CorrelationId` or `CompanyCode` |
| `ERP-FX-401` | `PERMANENT` | 200 | the `wsse:UsernameToken` is missing or not recognized |
| `ERP-FX-402` | `PERMANENT` | 200 | an unknown header carried `mustUnderstand`. `faultcode` is `S:MustUnderstand` |
| `ERP-FX-403` | `PERMANENT` | 200 | `SOAPAction` unquoted or wrong |
| `ERP-FX-410` | `PERMANENT` | 200 | the envelope is not a SOAP 1.1 envelope this service understands |
| `ERP-FX-500` | `PERMANENT` | **500** | `SOAPAction` absent entirely. `faultcode` is `S:Server` |

**The retryable fault, in full.** The very first `GetExchangeRateTable` for `CompanyCode=CH10` in a
run returns:

```xml
<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Body>
    <S:Fault>
      <faultcode>S:Server</faultcode>
      <faultstring>the exchange rate snapshot is being rebuilt; repeat the request</faultstring>
      <detail>
        <FinRefFault xmlns="urn:blp-erp:finref:1.0">
          <ns2:Code xmlns:ns2="urn:blp-erp:common:1.0">ERP-FX-503</ns2:Code>
          <Severity>RETRYABLE</Severity>
          <RetryAfterSeconds>2</RetryAfterSeconds>
          <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">run_7f3c1a</ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>
```

Repeat the identical request and the second attempt returns the table. **Do not sleep for the two
seconds:** there is no wall clock on this server, so waiting achieves nothing except spending your
own time. Retry immediately. Each attempt costs one quota unit, so a correct run makes exactly two
CH10 calls and one CH20 call per run.

`ERP-FX-014` and `ERP-FX-503` are the two that decide points: retrying the permanent one and not
retrying the retryable one are both graded mistakes, and `Severity` is what tells them apart.
