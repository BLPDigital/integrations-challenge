#!/bin/sh
# Fail if anything in scope reads the wall clock or an unseeded random source.
#
# Every graded outcome is a pure function of (scenario, seed, the requests made),
# so a real timestamp or an unseeded draw anywhere in the two services or their
# libraries makes a run unreproducible. This is the one check that keeps that
# true, and it lives in a script because it had drifted into two copies: the
# Makefile's version narrowed the scope and filtered comment lines, the CI
# workflow's inline copy did neither, and the CI step therefore failed on a
# pristine checkout for nine hits that were all legitimate.
#
# Scope is the arguments, or the packages a candidate has if there are none.
# internal/grader is deliberately out of scope: it supervises real OS processes,
# so its health deadlines, port waits and elapsed-time measurements are wall
# clock by necessity and none of them reaches a service. internal/simclock owns
# the virtual clock, and internal/seed/rng.go owns the seeded PRNG.
set -eu

scope=${*:-"cmd internal/model internal/store internal/httpx internal/miniblp internal/erp internal/importer internal/seed"}

hits=$(grep -rn 'time\.Now()\|math/rand' $scope --include='*.go' 2>/dev/null \
	| grep -v '_test.go' \
	| grep -vE ':[0-9]+:[[:space:]]*//' \
	| grep -v 'internal/simclock' \
	| grep -v 'internal/seed/rng.go' \
	|| true)

if [ -n "$hits" ]; then
	echo "wall clock or unseeded randomness in scope:" >&2
	echo "$hits" >&2
	exit 1
fi
