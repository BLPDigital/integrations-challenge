#!/bin/sh
# Drop the example batch into the twin's inbox, scan, and print the receipt.
#
# It proves the atomic drop protocol end to end in one command: staging
# directory, rename, scan, DONE, receipt.
set -eu
TWIN_PORT=${1:-8081}
TWIN="http://127.0.0.1:$TWIN_PORT"
SRC=${2:-examples/batch}
INBOX=${3:-var/miniblp/inbox}

if [ ! -d "$SRC" ]; then
	echo "smoke-file: $SRC does not exist" >&2
	exit 1
fi
batch="smoke-$(cksum "$SRC/manifest.json" | cut -d' ' -f1)"
staging="$INBOX/incoming/.staging-$batch"
rm -rf "$staging" "$INBOX/incoming/$batch"
mkdir -p "$staging"
cp "$SRC"/* "$staging"/
# The manifest carries the batch id, so rewrite it to this drop's id.
sed -i.bak "s/\"batch_id\": *\"[^\"]*\"/\"batch_id\": \"$batch\"/" "$staging/manifest.json" && rm -f "$staging/manifest.json.bak"
mv "$staging" "$INBOX/incoming/$batch"
echo "published $INBOX/incoming/$batch"

curl -fsS -X POST -H 'X-Admin-Token: dev-twin-admin' "$TWIN/admin/v1/inbox/scan" > /dev/null
echo "scanned"
if [ -f "$INBOX/receipts/$batch/DONE" ]; then
	echo "== receipt"
	cat "$INBOX/receipts/$batch/receipt.json"
	echo
	echo "== records.csv"
	head -5 "$INBOX/receipts/$batch/records.csv"
else
	echo "no DONE sentinel yet; the scan may not have picked the batch up" >&2
	exit 1
fi
