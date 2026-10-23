#!/usr/bin/env python3
"""Compare two or more grader output trees of the same scenarios.

The determinism requirement: every scenario, run more than once with the same
seed, has to produce identical digests, identical counters and identical scores.
Runs that each pass their own assertions do NOT prove that. They prove each run
matched the pinned digest, which is one value out of everything a run produces.
This compares the artifacts themselves.

    tools/compare-runs.py OUT1 OUT2 [OUT3 ...]

Each OUT is a --out directory of `grade run`, holding run_<id>/<SCENARIO>/. The
scenario set is discovered from the trees and has to be the same in all of them,
so a run that never happened is a failure and not a silent skip.

Volatile by design, normalized before comparing, and nothing else is:

  - the output directory itself and the ephemeral ports, which differ per run,
  - the admin tokens, which are drawn per run,
  - the scan counters (`inbox_scans`, `scans`, `received_scan`). The scan driver
    polls the twin on a real ticker while the connector runs, so a faster machine
    performs more scans. See internal/grader/run.go: an extra scan changes which
    scan number a receipt records and nothing else, and it is excluded from the
    state digest and from every scored assertion.

Exit 0 when every scenario is identical everywhere, 1 otherwise, naming the files
that differed.
"""
import glob
import os
import re
import sys

# What a run produces that has to be reproducible: the graded state, both
# services' accounting, the ERP's full request log (the reference connector is
# sequential, so even the order is fixed) and the connector's own three reports.
ARTIFACTS = [
    "evidence/twin-digest.json",
    "evidence/twin-metrics.json",
    "evidence/erp-metrics.json",
    "evidence/erp-documents.json",
    "evidence/erp-requests.json",
    "evidence/erp-soap-calls.json",
    "evidence/twin-exceptions.json",
    "evidence/twin-proposals.json",
    "evidence/twin-batches.json",
    "evidence/twin-run.json",
    "evidence/expected-exceptions.json",
    "reports/run.json",
    "reports/postings.csv",
    "reports/exceptions.csv",
]

# connector-runs.json is deliberately absent: it carries the argv, the captured
# stream paths and wall_ms, which are the harness's own record of a real process.

PORT = re.compile(r"127\.0\.0\.1:\d+")
TOKEN = re.compile(r"\b[0-9a-f]{32}\b")
SCANS = re.compile(r'"(inbox_scans|scans|received_scan)":\s*\d+')
DIGEST = re.compile(r'"digest"\s*:\s*"([0-9a-f]{12})')
# Lines of the scenario log that report real time or a real path.
NOISE = ("services up", "connector phase", "grade: ")


def normalize(text, root):
    text = text.replace(root, "<OUT>")
    text = PORT.sub("127.0.0.1:<PORT>", text)
    text = SCANS.sub(r'"\1": <SCANS>', text)
    return TOKEN.sub("<TOKEN>", text)


def scenarios(root):
    found = {}
    for path in sorted(glob.glob(os.path.join(root, "run_*", "*"))):
        if os.path.isdir(path):
            found[os.path.basename(path)] = path
    return found


def read(path, root):
    if not os.path.exists(path):
        return None
    with open(path, "rb") as fh:
        return normalize(fh.read().decode("utf-8", "replace"), root)


def outcome(root, scenario):
    """The scored outcome as selfcheck printed it, minus the timing lines."""
    path = os.path.join(root, scenario + ".log")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8", errors="replace") as fh:
        kept = [ln for ln in fh if not ln.startswith(NOISE)]
    return normalize("".join(kept), root)


def main(roots):
    if len(roots) < 2:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    found = [scenarios(r) for r in roots]
    for root, f in zip(roots, found):
        if not f:
            print(f"{root}: holds no run_*/<SCENARIO> directory")
            return 1
    names = sorted(found[0])
    problems = 0
    for root, f in zip(roots[1:], found[1:]):
        missing = sorted(set(names) - set(f))
        extra = sorted(set(f) - set(names))
        if missing or extra:
            print(f"{root}: scenario set differs from {roots[0]}: "
                  f"missing {missing or '-'}, extra {extra or '-'}")
            problems += 1
    if problems:
        return 1

    for name in names:
        dirs = [f[name] for f in found]
        differing = []
        for rel in ARTIFACTS:
            blobs = [read(os.path.join(d, rel), r) for d, r in zip(dirs, roots)]
            if all(b is None for b in blobs):
                continue
            if any(b != blobs[0] for b in blobs[1:]):
                differing.append(rel)
        outs = [outcome(r, name) for r in roots]
        if any(o != outs[0] for o in outs[1:]):
            differing.append("the scored outcome")
        digest = "?"
        head = read(os.path.join(dirs[0], "evidence/twin-digest.json"), roots[0])
        if head:
            hit = DIGEST.search(head)
            if hit:
                digest = hit.group(1)
        if differing:
            problems += 1
            print(f"{name}: NOT REPRODUCIBLE across {len(roots)} runs, "
                  f"{len(differing)} differing: {', '.join(differing)}")
        else:
            print(f"{name}: identical across {len(roots)} runs "
                  f"({len(ARTIFACTS)} artifacts, digest {digest}...)")

    print()
    if problems:
        print(f"{problems} scenario(s) are not reproducible")
        return 1
    print(f"all {len(names)} scenarios reproducible across {len(roots)} runs")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
