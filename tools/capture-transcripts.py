#!/usr/bin/env python3
"""Capture a worked HTTP transcript per endpoint from the running services.

Driven by tools/capture-transcripts.sh, which supplies the credentials. It writes
docs/transcripts.md whole: every exchange in that file is captured rather than
written, so it cannot drift from what the services answer.

The batch it opens is named transcripts-0001 and it is committed, so running this
twice against the same twin is a batch replay rather than a conflict. Reseed with
make reset if you want the first-delivery numbers back.
"""
import json, os, re, sys, urllib.request, urllib.error

ERP = "http://127.0.0.1:8082"

# The dev secrets are minted per process and published by make creds, so they are
# not secrets. They are masked anyway: a document that shows a token teaches the
# habit of pasting one, and our own secret scanner would be right to flag it.
SECRETS = []


def mask(text):
    for value, name in SECRETS:
        if value:
            text = text.replace(value, name)
    # Also by shape, so a value issued after this block was rendered is still
    # masked: the auth response carries the token the list learns from.
    text = re.sub(r"tok_[0-9a-f]{8,}", "<token>", text)
    text = re.sub(r"cs_[0-9a-f]{8,}", "<erp-client-secret>", text)
    text = re.sub(r"sp_[0-9a-f]{8,}", "<soap-password>", text)
    return text

TWIN = "http://127.0.0.1:8081"
out = []


def call(method, base, path, body=None, headers=None, raw=False, label=None, note=None):
    """Perform one request and append its transcript. Returns (status, text)."""
    headers = dict(headers or {})
    data = None
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body, indent=1).encode()
        headers.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req) as resp:
            status, hdrs, payload = resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        status, hdrs, payload = e.code, e.headers, e.read()

    if label:
        out.append("### " + label + "\n")
    if note:
        out.append(note + "\n")
    lines = ["```http", "%s %s HTTP/1.1" % (method, path),
             "Host: " + base.replace("http://", "")]
    for k, v in headers.items():
        shown = v
        if k.lower() == "authorization":
            shown = "Bearer <token>"
        lines.append("%s: %s" % (k, shown))
    if data is not None:
        lines.append("")
        text = data.decode("utf-8", "replace")
        text = mask(text)
        lines.append(text if len(text) < 1200 else text[:1200] + "\n... truncated")
    lines.append("")
    lines.append("HTTP/1.1 %d" % status)
    for k, v in hdrs.items():
        if k.lower() in ("date", "content-length", "connection"):
            continue
        lines.append("%s: %s" % (k, v))
    lines.append("")
    body_text = payload.decode("iso-8859-1" if raw else "utf-8", "replace")
    if not raw:
        try:
            # ensure_ascii=False on purpose: the point of a transcript is the
            # bytes, and an escaped \u00fc is not what the umlaut looks like on
            # the wire.
            body_text = json.dumps(json.loads(body_text), indent=1, ensure_ascii=False)
        except Exception:
            pass
    body_text = mask(body_text)
    lines.append(body_text if len(body_text) < 1600 else body_text[:1600] + "\n... truncated")
    lines.append("```")
    out.append("\n".join(lines) + "\n")
    return status, payload.decode("utf-8", "replace")


def h(level, text):
    out.append("#" * level + " " + text + "\n")


def p(text):
    out.append(text + "\n")

# --------------------------------------------------------------------------
erp_id = os.environ.get("ERP_CLIENT_ID", "blp-connector")
erp_secret = os.environ.get("ERP_CLIENT_SECRET", "")
twin_id = os.environ.get("TWIN_CLIENT_ID", "blp-connector")
twin_secret = os.environ.get("TWIN_CLIENT_SECRET", "")
soap_user = os.environ.get("SOAP_USERNAME", "")
soap_pass = os.environ.get("SOAP_PASSWORD", "")
SECRETS.extend([(erp_secret, "<erp-client-secret>"), (twin_secret, "<twin-client-secret>"),
                (soap_pass, "<soap-password>")])

h(1, "Worked transcripts")
p("""Every exchange below was captured from the two services on scenario S0, seed
20260416, by `tools/capture-transcripts.sh`. Nothing here is hand-written, and
nothing here is a substitute for `docs/erp-api.md`, `docs/twin-api.md` and
`docs/soap.md`: the documents are the contract, and this file is what the
contract looks like on the wire when you have not written any code yet.

Tokens are shown as `<token>`. Every other byte is real.""")

h(2, "The ERP")

st, txt = call("POST", ERP, "/erp/v1/auth/token",
               {"client_id": erp_id, "client_secret": erp_secret},
               label="Authenticate",
               note="Client credentials as JSON. The two services take a form body as well, "
                    "because half the HTTP clients in the world post credentials that way.")
erp_token = json.loads(txt)["access_token"]
SECRETS.append((erp_token, "<token>"))
auth = {"Authorization": "Bearer " + erp_token}

st, txt = call("GET", ERP, "/erp/v1/suppliers?limit=2", headers=auth,
               label="One page of creditors",
               note="`returned`, `has_more`, `next_cursor` and `max_change_seq` are the whole "
                    "pagination contract. `max_change_seq` is the collection's highest sequence "
                    "and is a usable watermark only once `has_more` is false.")
page = json.loads(txt)

call("GET", ERP, "/erp/v1/suppliers?limit=2&cursor=" + page["next_cursor"], headers=auth,
     label="The next page, by cursor",
     note="The cursor is opaque and signed. Editing it, or building your own, is "
          "`400 CURSOR_INVALID`: a cursor is the server's statement about where you were.")

call("GET", ERP, "/erp/v1/suppliers?limit=5000", headers=auth,
     label="A page size above the published maximum",
     note="Note `X-Limit-Clamped`. The request is not refused and it is not silently "
          "honored either: the clamp is applied and announced, so a client that asks for "
          "too much can notice.")

call("GET", ERP, "/erp/v1/purchase-order-lines?limit=1", headers=auth,
     label="A purchase order line",
     note="`unit_price` carries four fraction digits and `quantity` three. A unit price is a "
          "rate, not a booked amount, so it is allowed more digits than the currency's minor "
          "unit. Read both as strings.")

call("GET", ERP, "/erp/v1/uom-conversions", headers=auth,
     label="The unit conversion table",
     note="One page, no paging, `max_change_seq: 0`. It is customizing rather than master "
          "data: it changes when somebody decides it does, not when a record moves.")

h(2, "The SOAP channel")

envelope = ("""<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">%s</wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>transcripts</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH10</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>
""" % (soap_user, soap_pass)).encode()

call("POST", ERP, "/soap/FinancialReferenceDataService", envelope,
     headers={"Content-Type": "text/xml; charset=utf-8", "SOAPAction": "not-the-right-value"},
     raw=True, label="The SOAPAction header is part of the contract",
     note="A wrong or unquoted SOAPAction answers **HTTP 200** carrying a `soap:Fault`. On this "
          "channel the status line is never your success signal, and this is the cheapest way "
          "to learn it.")

call("POST", ERP, "/soap/FinancialReferenceDataService", envelope,
     headers={"Content-Type": "text/xml; charset=utf-8",
              "SOAPAction": '"urn:blp-erp:finref:1.0/GetExchangeRateTable"'},
     raw=True, label="The same request with the quotes",
     note="The quotes are part of the value. Three things to notice in the answer: the prolog "
          "declares **ISO-8859-1** and no HTTP header repeats it, the prefixes are `S:` and "
          "`ns2:` rather than the documentation's, and the rates carry a decimal **comma**. "
          "The first delivery of a company's table may answer a RETRYABLE fault; retry the "
          "identical request and it succeeds.")

call("POST", ERP, "/soap/FinancialReferenceDataService",
     envelope.replace(b"<fin:CompanyCode>CH10", b"<fin:CompanyCode>CH20"),
     headers={"Content-Type": "text/xml; charset=utf-8",
              "SOAPAction": '"urn:blp-erp:finref:1.0/GetExchangeRateTable"'},
     raw=True, label="A permanent fault",
     note="`Severity` is machine-readable and authoritative. PERMANENT means never retry: no "
          "number of attempts configures a rate table for a company code that has none. Its "
          "invoices belong in the exception queue.")

h(2, "Posting to the ERP")

item = {
    "item_key": "transcripts-1", "external_reference": "transcripts-1",
    "company_code": "CH10", "supplier_number": "0000000417",
    "supplier_invoice_number": "0004711", "document_type": "RE",
    "document_date": "2026-03-29", "posting_date": "2026-03-29", "po_number": "",
    "cost_center": "0012340", "currency": "CHF", "gross_amount": "1252.78",
    "vat_amount": "0.00", "source_currency": "EUR", "source_gross_amount": "1345.63",
    "fx_rate": "0.931000", "fx_rate_factor": 1,
}

call("POST", ERP, "/erp/v1/ap/documents", item,
     headers=dict(auth, **{"Idempotency-Key": "blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa"}),
     label="One document",
     note="Amounts are decimal strings; a JSON number is `400 MALFORMED_BODY` with reason "
          "`number_format`. The four source/fx fields document the conversion YOU performed: "
          "the ERP stores them and never recomputes them, because an ERP that answered with "
          "its own conversion would be handing out the answer.")

call("POST", ERP, "/erp/v1/ap/documents", item,
     headers=dict(auth, **{"Idempotency-Key": "blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa"}),
     label="The same request with the same key",
     note="The original response replayed verbatim, down to the document number, with "
          "`idempotency_replay: true` on every item and the `Idempotent-Replay` header. This "
          "is what a correct retry of the applied-then-500 posting looks like.")

call("POST", ERP, "/erp/v1/ap/documents", item,
     headers=dict(auth, **{"Idempotency-Key": "blp:acme-ch:transcripts-1:bbbbbbbbbbbbbbbb"}),
     label="The same document under a FRESH key",
     note="The same `external_reference` again. You get the ORIGINAL document number back, so "
          "nothing is double-booked, `duplicate: true` says what happened, and "
          "`duplicate_document_attempts` moves. Grading asserts that counter is zero, so this "
          "answer is a failed assertion wearing a 200.")

call("POST", ERP, "/erp/v1/ap/documents",
     dict(item, item_key="transcripts-1", external_reference="transcripts-1",
          gross_amount="9999.99"),
     headers=dict(auth, **{"Idempotency-Key": "blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa"}),
     label="The same key with a different body",
     note="`409 IDEMPOTENCY_KEY_REUSED`. A key is a promise about a body: reusing one for "
          "different content is the one case where the ERP would have to guess which of the "
          "two you meant, and it refuses instead.")

call("POST", ERP, "/erp/v1/ap/documents:batch",
     {"items": [
         dict(item, item_key="transcripts-2", external_reference="transcripts-2",
              supplier_invoice_number="0004712"),
         dict(item, item_key="transcripts-3", external_reference="transcripts-3",
              supplier_invoice_number="0004713", supplier_number="0000009999"),
     ]},
     headers=dict(auth, **{"Idempotency-Key": "blp:acme-ch:transcripts-batch:cccccccccccccccc"}),
     label="A batch where one item is rejected",
     note="**`207 Multi-Status`**, because the outcomes differ. `results` keeps your item order "
          "and is the authority; the counts are a convenience. A business rejection is a "
          "per-item result with `retriable: false`, never a transport error, and retrying one "
          "is a graded mistake.")

h(2, "The digital twin")

st, txt = call("POST", TWIN, "/v1/auth/token",
               {"client_id": twin_id, "client_secret": twin_secret},
               label="Authenticate",
               note="The same grant on both services, and the same token lifetime: 250 requests "
                    "or 900'000 virtual milliseconds, whichever comes first. A 401 "
                    "TOKEN_EXPIRED is not a failure; it is a refresh.")
twin_token = json.loads(txt)["access_token"]
SECRETS.append((twin_token, "<token>"))
tauth = {"Authorization": "Bearer " + twin_token}

manifest = {
    "manifest_version": "1", "batch_id": "transcripts-0001",
    "producer": "docs/transcripts", "run_id": "transcripts", "tenant": "acme-ch",
    "source_system": "erp-prod", "mode": "upsert", "full_load": False,
    "on_error": "continue",
    "files": [{"dataset": "supplier", "format": "json", "profile": "blp-canonical-v1",
               "encoding": "UTF-8"}],
}
st, txt = call("POST", TWIN, "/v1/ingest/batches", manifest, headers=dict(tauth),
               label="Open a batch",
               note="The open body is the file-channel manifest minus the three per-file facts "
                    "only a file has: `path`, `record_count` and `sha256`. Note that "
                    "`batch_ref` IS the `batch_id`: one name, one value, nothing for a "
                    "resuming client to persist.")

records = [{"supplier_number": "0000000417", "name": "Meier Präzision AG", "country": "CH",
            "currency": "CHF", "iban": "CH9300762011623852957",
            "vat_number": "CHE-123.456.789 MWST", "payment_terms_days": 30,
            "blocked": False, "change_seq": 100417},
           {"supplier_number": "0000000418", "name": "Amount as a JSON number",
            "country": "CH", "currency": "CHF", "iban": "CH9300762011623852957",
            "vat_number": "CHE-123.456.780 MWST", "payment_terms_days": "thirty",
            "blocked": False, "change_seq": 100418}]
call("POST", TWIN, "/v1/ingest/batches/transcripts-0001/records?dataset=supplier",
     records, headers=dict(tauth, **{"Idempotency-Key": "ing:transcripts:supplier:1",
                                     "X-Chunk-Ordinal": "1"}),
     label="One chunk, one good record and one bad one",
     note="`207` because the outcomes differ. The body is JSON whatever format the request "
          "used, because a per-record result carries nested `errors[]` and a CSV cell cannot "
          "hold a list. Every error names the field AND the location in the bytes you sent.")

call("POST", TWIN, "/v1/ingest/batches/transcripts-0001/records?dataset=supplier",
     records, headers=dict(tauth, **{"Idempotency-Key": "ing:transcripts:supplier:1",
                                     "X-Chunk-Ordinal": "1"}),
     label="The same chunk with the same key",
     note="The stored response, replayed verbatim. This is what makes the retry of an "
          "applied-then-500 chunk exact, and it is the reason the key has to be a function of "
          "the payload rather than of the attempt.")

call("POST", TWIN, "/v1/ingest/batches/transcripts-0001/records?dataset=supplier",
     records, headers=dict(tauth, **{"Idempotency-Key": "ing:transcripts:supplier:1-again",
                                     "X-Chunk-Ordinal": "1"}),
     label="The same chunk under a NEW key",
     note="Applied again - the store is content-addressed, so no record changes - and "
          "`duplicate_apply_attempts` moves. Grading asserts that counter is zero: it is the "
          "evidence that your key is derived from your payload.")

call("POST", TWIN, "/v1/ingest/batches/transcripts-0001/records?dataset=supplier",
     records, headers=dict(tauth, **{"Idempotency-Key": "ing:transcripts:supplier:9",
                                     "X-Chunk-Ordinal": "9"}),
     label="A skipped ordinal",
     note="`CHUNK_GAP`, with what was expected and what arrived. Ordinals are 1-based and "
          "strictly increasing per (batch, dataset): a gap means a chunk is missing, and "
          "guessing which one is not the twin's job.")

call("POST", TWIN, "/v1/ingest/batches/transcripts-0001/commit", {}, headers=dict(tauth),
     label="Commit",
     note="The receipt object, identical in shape to the one the file channel writes to "
          "`receipts/<batch_id>/receipt.json`. One contract, two ways in.")

call("GET", TWIN, "/v1/outbox/proposals?limit=1&status=pending", headers=dict(tauth),
     label="One proposal from the outbox",
     note="A proposal is the twin's request for a posting: it carries the converted amount, "
          "the rate and factor it was converted with, and the source amount it came from. "
          "Money is an integer in minor units and it never travels as a JSON number.")

call("POST", TWIN, "/v1/outbox/acks",
     {"run_id": "transcripts", "acks": [
         {"proposal_id": "prp_0000001", "status": "posted",
          "external_document_number": "AP-2026-0000001", "external_fiscal_year": 2026,
          "external_posting_date": "2026-04-14", "external_revision": 1,
          "http_status": 207, "attempts": 1, "idempotency_replay": False,
          "idempotency_key": "blp:acme-ch:prp_0000001:0123456789abcdef"}]},
     headers=dict(tauth, **{"Idempotency-Key": "ack:transcripts:0123456789abcdef"}),
     label="Acknowledge one posting",
     note="The ack is what closes the audit chain: without it the twin's proposal stays "
          "pending and nothing can be traced from an ERP document number back to a line in a "
          "file. `proposals_pending` is graded, and it is the counter that catches a "
          "connector which posted and never told anyone.")

st, txt = call("GET", TWIN, "/admin/v1/state/digest",
               headers={"X-Admin-Token": "dev-twin-admin"},
               label="The state digest (admin, ours, not yours)",
               note="The graded digest and the per-dataset record counts. Your connector is "
                    "never given the admin token; use it while developing, because it is the "
                    "fastest way to see whether what you sent is what landed.")

open("docs/transcripts.md", "w").write("\n".join(out))
print("captured %d blocks" % len(out))
