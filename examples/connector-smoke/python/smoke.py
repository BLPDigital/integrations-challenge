#!/usr/bin/env python3
"""A smoke test, not a solution.

It satisfies the CLI contract, authenticates against both servers, reads one
paginated page from the ERP, drops one tiny batch into the twin through the
atomic protocol, writes a schema-valid run.json, and exits 3 with a message
saying what it is.

Its whole purpose is to prove your plumbing before you invest in semantics. Read
it once, then throw it away.

    python3 examples/connector-smoke/python/smoke.py run \
        --run-id smoke --erp-base-url http://127.0.0.1:8082 \
        --twin-base-url http://127.0.0.1:8081 \
        --twin-drop-dir var/miniblp/inbox \
        --state-dir /tmp/smoke-state --report-dir /tmp/smoke-reports
"""

import argparse
import hashlib
import json
import os
import sys
import urllib.request

EXIT_CLEAN, EXIT_EXCEPTIONS, EXIT_FAILURE = 0, 2, 3


def post_json(url, payload, headers=None):
    body = json.dumps(payload).encode()
    h = {"Content-Type": "application/json"}
    h.update(headers or {})
    req = urllib.request.Request(url, data=body, method="POST", headers=h)
    with urllib.request.urlopen(req) as resp:
        raw = resp.read()
        return resp.status, (json.loads(raw) if raw else None)


def get_json(url, headers=None):
    req = urllib.request.Request(url, headers=headers or {})
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("subcommand", nargs="?", default="run")
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--erp-base-url", required=True)
    ap.add_argument("--twin-base-url", required=True)
    ap.add_argument("--twin-drop-dir", required=True)
    ap.add_argument("--state-dir", required=True)
    ap.add_argument("--report-dir", required=True)
    ap.add_argument("--config", default="")
    ap.add_argument("--no-chaos", action="store_true")
    args = ap.parse_args()

    erp_id = os.environ.get("ERP_CLIENT_ID", "blp-connector")
    erp_secret = os.environ.get("ERP_CLIENT_SECRET", "")
    twin_id = os.environ.get("TWIN_CLIENT_ID", "blp-connector")
    twin_secret = os.environ.get("TWIN_CLIENT_SECRET", "")

    os.makedirs(args.report_dir, exist_ok=True)
    os.makedirs(args.state_dir, exist_ok=True)

    _, tok = post_json(args.erp_base_url + "/erp/v1/auth/token",
                       {"client_id": erp_id, "client_secret": erp_secret})
    erp_token = tok["access_token"]
    page = get_json(args.erp_base_url + "/erp/v1/suppliers?limit=250",
                    {"Authorization": "Bearer " + erp_token})
    print("suppliers page: %d records, has_more=%s, max_change_seq=%s"
          % (page["returned"], page["has_more"], page["max_change_seq"]))

    _, ttok = post_json(args.twin_base_url + "/v1/auth/token",
                        {"client_id": twin_id, "client_secret": twin_secret})
    print("twin token acquired, expires after %s requests" % ttok["expires_after_requests"])

    # One tiny batch through the atomic drop protocol.
    batch = "smoke-" + args.run_id
    incoming = os.path.join(args.twin_drop_dir, "incoming")
    staging = os.path.join(incoming, ".staging-" + batch)
    os.makedirs(staging, exist_ok=True)
    csv = ("supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\r\n"
           "0000000417,Meier Praezision AG,CH,CHF,CH9300762011623852957,CHE-123.456.789 MWST,30,false,100417\r\n")
    with open(os.path.join(staging, "suppliers.csv"), "w", newline="") as f:
        f.write(csv)
    manifest = {
        "manifest_version": "1", "batch_id": batch, "producer": "smoke/1.0",
        "run_id": args.run_id, "tenant": "acme-ch", "source_system": "erp-prod",
        "mode": "upsert", "full_load": False, "on_error": "continue",
        "files": [{
            "path": "suppliers.csv", "dataset": "supplier", "format": "csv",
            "profile": "blp-canonical-v1", "encoding": "UTF-8", "record_count": 1,
            "sha256": hashlib.sha256(csv.encode()).hexdigest(),
            "csv": {"delimiter": ",", "quote": "\"", "escape": "double", "header": True,
                    "line_ending": "CRLF", "decimal_separator": ".", "thousands_separator": "",
                    "date_format": "RFC3339", "null_token": "", "trim": "both"},
        }],
    }
    with open(os.path.join(staging, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2)
    os.rename(staging, os.path.join(incoming, batch))
    print("batch published: %s" % batch)
    print("the twin's scanner is driven by the harness, so the receipt appears "
          "in %s/receipts/%s/ once it runs" % (args.twin_drop_dir, batch))

    run = {
        "run_id": args.run_id, "connector_version": "smoke-1.0", "exit_code": EXIT_FAILURE,
        "channel_used": "file", "formats_used": ["csv"],
        "erp_requests": 2, "erp_429s": 0, "erp_5xx_retried": 0,
        "twin_requests": 1, "twin_429s": 0,
        # One record was published, so one record is what the report accounts for.
        # records_read is not "rows the HTTP response contained": the published
        # invariant is records_read == ingested + rejected + skipped_unchanged,
        # which makes it "records this run took into the pipeline and can account
        # for". The 240 rows the page returned are printed above and deliberately
        # dropped, so counting them here would report 239 silent drops.
        "batches_written": 1, "records_read": 1, "records_ingested": 1,
        "records_rejected": 0, "records_skipped_unchanged": 0,
        "proposals_read": 0, "proposals_posted": 0, "proposals_rejected": 0,
        "proposals_pending": 0, "duplicate_document_attempts": 0,
        "watermark_before": {}, "watermark_after": {}, "full_load": False,
    }
    with open(os.path.join(args.report_dir, "run.json"), "w") as f:
        json.dump(run, f, indent=2)
    for name, header in (
        ("postings.csv", "proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number,"
                         "source_batch_id,source_file_or_chunk,source_line_or_ordinal,"
                         "idempotency_key,erp_document_number,erp_status,http_status,attempts,"
                         "idempotency_replay"),
        ("exceptions.csv", "subject_key,subject_type,stage,code,field,message,source_batch_id,"
                           "source_file_or_chunk,source_line_or_ordinal"),
    ):
        with open(os.path.join(args.report_dir, name), "w") as f:
            f.write(header + "\n")

    print()
    print("This is a SMOKE TEST, not a solution: it pulled one page, dropped one "
          "batch, and posted nothing.")
    return EXIT_FAILURE


if __name__ == "__main__":
    sys.exit(main())
