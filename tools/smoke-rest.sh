#!/bin/sh
# Authenticate against both services and read one page from each.
#
# It exists so a candidate can prove the plumbing in ten seconds, and so the
# credentials and header names are demonstrated rather than described.
set -eu
ERP_PORT=${1:-8082}
TWIN_PORT=${2:-8081}
ERP="http://127.0.0.1:$ERP_PORT"
TWIN="http://127.0.0.1:$TWIN_PORT"

say() { printf '\n== %s\n' "$1"; }

say "ERP credentials (admin surface, ours, never the connector's)"
creds=$(curl -fsS -H 'X-Admin-Token: dev-erp-admin' "$ERP/erp-admin/v1/credentials")
echo "$creds"
secret=$(printf '%s' "$creds" | sed -n 's/.*"client_secret":"\([^"]*\)".*/\1/p')

say "ERP token"
tok=$(curl -fsS -X POST -H 'Content-Type: application/json' \
	-d "{\"client_id\":\"blp-connector\",\"client_secret\":\"$secret\"}" \
	"$ERP/erp/v1/auth/token")
echo "$tok"
access=$(printf '%s' "$tok" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

say "ERP suppliers, first two records"
curl -fsS -H "Authorization: Bearer $access" "$ERP/erp/v1/suppliers?limit=2"
echo

say "ERP suppliers with a limit above the cap: note X-Limit-Clamped"
curl -fsS -D - -o /dev/null -H "Authorization: Bearer $access" "$ERP/erp/v1/suppliers?limit=9999" \
	| grep -i '^x-limit-clamped' || echo "(no clamp header, which means the limit was inside the cap)"

say "twin token"
curl -fsS -X POST -H 'Content-Type: application/json' \
	-d '{"client_id":"blp-connector","client_secret":"dev-twin-secret"}' \
	"$TWIN/v1/auth/token"
echo

say "twin state digest (admin surface)"
curl -fsS -H 'X-Admin-Token: dev-twin-admin' "$TWIN/admin/v1/state/digest"
echo
