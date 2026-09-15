#!/bin/sh
# Verify that only the candidate-writable paths were modified.
#
# BUILD-SPEC 16.3: the two services, the grader, the fixtures and the docs are
# ours. A candidate who edits the twin's matching engine, the ERP's rate limiter
# or a golden fixture would be grading themselves, so the tree is checksummed and
# the grader additionally rebuilds both services from the pristine tree.
#
# Portable POSIX sh: no bashisms, and no tool that is not on a Mac as well as on
# Linux.
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

MANIFEST=${MANIFEST:-PROTECTED.sha256}

if [ ! -f "$MANIFEST" ]; then
	echo "verify-pristine: $MANIFEST is missing; run tools/regen-protected.sh" >&2
	exit 1
fi

# The paths a candidate owns. Anything under these is theirs to change; anything
# else must match the manifest.
is_writable() {
	case "$1" in
	connector/*) return 0 ;;
	internal/importer/kredexp/*) return 0 ;;
	internal/importer/all/all.go) return 0 ;;
	DECISIONS.md) return 0 ;;
	PROTECTED.sha256) return 0 ;;
	esac
	return 1
}

fail=0
modified=""
missing=""

# Every line of the manifest is "<sha256>  <path>".
while read -r want path; do
	[ -n "${want:-}" ] || continue
	if is_writable "$path"; then continue; fi
	if [ ! -f "$path" ]; then
		missing="$missing $path"
		fail=1
		continue
	fi
	have=$(sha256_of "$path")
	if [ "$have" != "$want" ]; then
		modified="$modified $path"
		fail=1
	fi
done < "$MANIFEST"

if [ "$fail" -ne 0 ]; then
	echo "verify-pristine: files outside the candidate-writable set were changed." >&2
	[ -z "$modified" ] || { echo "  modified:" >&2; for f in $modified; do echo "    $f" >&2; done; }
	[ -z "$missing" ] || { echo "  missing:" >&2; for f in $missing; do echo "    $f" >&2; done; }
	echo >&2
	echo "  You may change: connector/**, internal/importer/kredexp/**," >&2
	echo "  one blank-import line in internal/importer/all/all.go, and DECISIONS.md." >&2
	echo "  Restore the rest with: git checkout -- <path>" >&2
	exit 1
fi

echo "verify-pristine: OK"
