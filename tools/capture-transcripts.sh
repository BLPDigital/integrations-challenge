#!/bin/sh
# Regenerate docs/transcripts.md from the running services.
#
# Every exchange in that document is captured, never written by hand, so it can
# never drift from what the services actually answer. Run it after any change to
# a wire contract:
#
#     make up
#     tools/capture-transcripts.sh
#
# It needs both services on their default dev ports and the credentials make
# creds prints. Dev credentials and issued tokens are masked by value and by
# shape, so the document never carries one.
set -eu

cd "$(dirname "$0")/.."

if ! curl -fsS -o /dev/null http://127.0.0.1:8082/healthz 2>/dev/null; then
	echo "the ERP is not running on 8082; start it with: make up" >&2
	exit 1
fi
if ! curl -fsS -o /dev/null http://127.0.0.1:8081/healthz 2>/dev/null; then
	echo "the twin is not running on 8081; start it with: make up" >&2
	exit 1
fi

# The credentials the services were started with. The ERP mints its own and
# publishes them on its admin surface; the twin takes its pair from the
# environment make up used.
creds=$(curl -fsS -H 'X-Admin-Token: dev-erp-admin' \
	http://127.0.0.1:8082/erp-admin/v1/credentials)
ERP_CLIENT_ID=$(printf '%s' "$creds" | sed -n 's/.*"client_id":"\([^"]*\)".*/\1/p')
ERP_CLIENT_SECRET=$(printf '%s' "$creds" | sed -n 's/.*"client_secret":"\([^"]*\)".*/\1/p')
SOAP_USERNAME=$(printf '%s' "$creds" | sed -n 's/.*"soap_username":"\([^"]*\)".*/\1/p')
SOAP_PASSWORD=$(printf '%s' "$creds" | sed -n 's/.*"soap_password":"\([^"]*\)".*/\1/p')
TWIN_CLIENT_ID=${TWIN_CLIENT_ID:-blp-connector}
TWIN_CLIENT_SECRET=${TWIN_CLIENT_SECRET:-dev-twin-secret}
export ERP_CLIENT_ID ERP_CLIENT_SECRET SOAP_USERNAME SOAP_PASSWORD \
	TWIN_CLIENT_ID TWIN_CLIENT_SECRET

python3 tools/capture-transcripts.py "$@"
echo "wrote docs/transcripts.md"
