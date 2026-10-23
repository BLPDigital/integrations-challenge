#!/bin/sh
# Regenerate PROTECTED.sha256 from the tracked tree.
#
# Run this after a deliberate change to our own files, and never as a way around
# a verify-pristine failure.
set -eu
# sha256 of one file, on GNU coreutils, on BSD and macOS, and on anything that
# has openssl. macOS ships shasum and no sha256sum, so the coreutils spelling
# alone made `make check` fail on a Mac at its last step, which is the first
# thing a candidate on a Mac would meet.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed 's/.*[= ]//'
	else
		echo "no sha256 tool found: install coreutils, or use a shell with shasum or openssl" >&2
		exit 1
	fi
}

out=${1:-PROTECTED.sha256}
tmp=$(mktemp)
git ls-files -z | tr '\0' '\n' | while read -r f; do
	[ -f "$f" ] || continue
	case "$f" in
	PROTECTED.sha256) continue ;;
	esac
	printf '%s  %s\n' "$(sha256_of "$f")" "$f" >> "$tmp"
done
LC_ALL=C sort -k2 "$tmp" > "$out"
rm -f "$tmp"
echo "$out: $(wc -l < "$out") files"
