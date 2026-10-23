#!/bin/sh
# Validate the three report files of the most recent grading run.
#
# It is the local version of the reconciliation the grader runs, so a candidate
# finds a shape or balance error in seconds instead of at grading time.
set -eu
dir=${1:-}
if [ -z "$dir" ]; then
	dir=$(ls -dt grading/out/*/*/reports 2>/dev/null | head -1 || true)
fi
if [ -z "$dir" ] || [ ! -d "$dir" ]; then
	echo "validate-reports: no report directory found; run a scenario first (make scenario S=S0)" >&2
	exit 1
fi
echo "== $dir"
fail=0
for f in run.json postings.csv exceptions.csv; do
	if [ ! -f "$dir/$f" ]; then echo "  MISSING $f"; fail=1; else
		printf '  %-16s %s bytes\n' "$f" "$(wc -c < "$dir/$f" | tr -d ' ')"
	fi
done
[ "$fail" -eq 0 ] || exit 1

# The header contracts, verbatim from docs/spec.md.
want_post='proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number,source_batch_id,source_file_or_chunk,source_line_or_ordinal,idempotency_key,erp_document_number,erp_status,http_status,attempts,idempotency_replay'
want_exc='subject_key,subject_type,stage,code,field,message,source_batch_id,source_file_or_chunk,source_line_or_ordinal'
head_post=$(head -1 "$dir/postings.csv" | tr -d '\r')
head_exc=$(head -1 "$dir/exceptions.csv" | tr -d '\r')
[ "$head_post" = "$want_post" ] || { echo "  postings.csv header differs"; echo "    want $want_post"; echo "    got  $head_post"; fail=1; }
[ "$head_exc" = "$want_exc" ] || { echo "  exceptions.csv header differs"; echo "    want $want_exc"; echo "    got  $head_exc"; fail=1; }

# The per-run balances of BUILD-SPEC 17.6, computed with awk so this script needs
# nothing but a shell.
python3 - "$dir" <<'PY' || fail=1
import csv, json, sys
d = sys.argv[1]
run = json.load(open(d + "/run.json"))
problems = []
def bal(name, total, parts):
    got = sum(run.get(p, 0) for p in parts)
    if run.get(total, 0) != got:
        problems.append(f"{total}={run.get(total)} but {'+'.join(parts)}={got}")
bal("records_read", "records_read", ["records_ingested", "records_rejected", "records_skipped_unchanged"])
bal("proposals_read", "proposals_read", ["proposals_posted", "proposals_rejected", "proposals_pending"])
rows = list(csv.DictReader(open(d + "/postings.csv")))
posted = [r for r in rows if r["erp_status"] == "posted"]
if len(posted) != run.get("proposals_posted", 0):
    problems.append(f"postings.csv has {len(posted)} posted rows but run.json says {run.get('proposals_posted')}")
missing_key = [r for r in rows if not r["idempotency_key"]]
if missing_key:
    problems.append(f"{len(missing_key)} postings.csv rows carry no idempotency key")
no_doc = [r for r in posted if not r["erp_document_number"]]
if no_doc:
    problems.append(f"{len(no_doc)} posted rows carry no ERP document number")
for p in problems:
    print("  " + p)
sys.exit(1 if problems else 0)
PY

[ "$fail" -eq 0 ] && echo "validate-reports: OK" || { echo "validate-reports: FAILED"; exit 1; }
