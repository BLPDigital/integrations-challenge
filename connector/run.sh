#!/usr/bin/env bash
#
# connector/run.sh - the entry point the grader invokes. REPLACE THIS FILE.
#
# Contract (connector/README.md):
#
#   run.sh run --run-id ID
#              --erp-base-url URL --twin-base-url URL
#              --erp-export-dir PATH --twin-drop-dir PATH
#              --state-dir PATH --report-dir PATH
#              [--config FILE] [--no-chaos]
#   run.sh --version
#
# Exit codes: 0 clean, 2 completed with business exceptions (not a failure),
# 3 hard failure. Anything else fails the scenario.
#
# Credentials come from the environment, never from the command line:
# ERP_CLIENT_ID, ERP_CLIENT_SECRET, TWIN_CLIENT_ID, TWIN_CLIENT_SECRET,
# SOAP_USERNAME, SOAP_PASSWORD. Never print them, and never write them to a
# report or a state file.
#
# This stub parses the contract, prints what it was given, and exits 3, so a
# grading run against an untouched checkout fails visibly instead of looking
# like a passing empty run.

set -euo pipefail

VERSION="0.0.0-not-implemented"

RUN_ID=""
ERP_BASE_URL=""
TWIN_BASE_URL=""
ERP_EXPORT_DIR=""
TWIN_DROP_DIR=""
STATE_DIR=""
REPORT_DIR=""
CONFIG=""
NO_CHAOS=0
COMMAND=""

while [ $# -gt 0 ]; do
  case "$1" in
    --version)         echo "blp-connector-stub $VERSION"; exit 0 ;;
    run)               COMMAND="run"; shift ;;
    --run-id)          RUN_ID="${2:-}"; shift 2 ;;
    --erp-base-url)    ERP_BASE_URL="${2:-}"; shift 2 ;;
    --twin-base-url)   TWIN_BASE_URL="${2:-}"; shift 2 ;;
    --erp-export-dir)  ERP_EXPORT_DIR="${2:-}"; shift 2 ;;
    --twin-drop-dir)   TWIN_DROP_DIR="${2:-}"; shift 2 ;;
    --state-dir)       STATE_DIR="${2:-}"; shift 2 ;;
    --report-dir)      REPORT_DIR="${2:-}"; shift 2 ;;
    --config)          CONFIG="${2:-}"; shift 2 ;;
    --no-chaos)        NO_CHAOS=1; shift ;;
    *) echo "run.sh: unknown argument: $1" >&2; exit 3 ;;
  esac
done

if [ "$COMMAND" != "run" ]; then
  echo "run.sh: usage: run.sh run --run-id ID ... (see connector/README.md)" >&2
  exit 3
fi

cat >&2 <<EOF
run.sh is not implemented yet.

  run_id          $RUN_ID
  erp_base_url    $ERP_BASE_URL
  twin_base_url   $TWIN_BASE_URL
  erp_export_dir  $ERP_EXPORT_DIR
  twin_drop_dir   $TWIN_DROP_DIR
  state_dir       $STATE_DIR
  report_dir      $REPORT_DIR
  config          ${CONFIG:-<none>}
  no_chaos        $NO_CHAOS

Replace this file with your connector, in any language. Read connector/README.md
for the report files and the exit codes, and CHALLENGE.md for the four phases.
EOF
exit 3
