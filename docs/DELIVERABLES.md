# Deliverables

Four things. Nothing else in the tree changes.

## 1. `connector/**`

Your connector, in any language, with any third-party libraries that language offers.

| File | Required | What it is |
|---|---|---|
| `connector/run.sh` | yes | the entry point the grader invokes, executable, honoring the CLI in [../connector/README.md](../connector/README.md) |
| `connector/setup.sh` | optional | one-time build or dependency install, run once before the scenario series with network access |
| `connector/Dockerfile` | optional | yours, if you containerize. The harness never builds it: `setup.sh` does, and `run.sh` runs it |
| your source, tests, config | yes | whatever the connector is made of |
| `connector/RUNBOOK.md` | optional | operational notes, from [templates/RUNBOOK.md](templates/RUNBOOK.md). Worth zero points |

`run.sh` must work from a clean checkout after at most one `setup.sh` or one image build, must not
require network access beyond the two local servers, and must not invent its own `--run-id`.

## 2. The Go task

```
internal/importer/kredexp/**      your implementation, standard library only
internal/importer/all/all.go     exactly one line: uncomment the blank import
```

The blank import is already in the file, commented, with the line marked. Uncommenting it is the
only edit that file needs and the only edit anywhere outside your own package.

## 3. `DECISIONS.md`

At the repository root, from [templates/DECISIONS.md](templates/DECISIONS.md), one page. Six
headings, filled in. It is worth 10 of the 100 points and it is the only part of the score a human
reads.

## 4. Nothing else modified

The candidate-writable allowlist is exactly:

```
connector/**
internal/importer/kredexp/**
internal/importer/all/all.go        (one added blank-import line)
DECISIONS.md
```

`tools/verify-pristine.sh` diffs your tree against `PROTECTED.sha256`, which carries the SHA-256 of
every tracked file. It runs inside `make check` and again in the grader before every scenario, and it
fails with an explicit file list. Check it yourself the cheap way before you hand back:

```
git status --short          # nothing outside the allowlist
make check
```

The grader assembles the tree it grades in: our files, with only the four paths above taken from
your submission, and it rebuilds both services from that tree. So editing the twin's matching
engine, the ERP's rate limiter or a golden fixture cannot change your grading run: your edit is
simply not in the tree that runs. It is also checked, against our own copy of `PROTECTED.sha256`
rather than yours, and a changed protected file appears on the scorecard as a warning with your name
next to it. It costs no points and it is a question somebody will ask you.

Do not commit `var/`, build output, virtual environments, `node_modules`, compiled binaries or
`go.sum`. Do not touch `go.mod`.

## Handing back

Commit your work with real commit messages: we read the ordering, because the sequence of decisions
is the part a finished diff hides.

**From your own repository:** push everything to the private repository you created from the clone
(see "Start here" in `README.md`), add `fatjonblp` as a collaborator with read access (Settings, then
Collaborators, then Add people) and send the link. Leave it in place until after the debrief. Keep it
private, no pull request against the source repository, please, and do not copy the exercise into a
public repository.

**From a tarball:** send back the tree as you received it plus your work, as an archive. Leave out
`var/`, `bin/`, `dist/` and anything else your build produced.

Either way: if your connector needs a runtime we would not guess (a JVM, a .NET SDK, a Node version,
a container), name it in the first two lines of `DECISIONS.md`.
