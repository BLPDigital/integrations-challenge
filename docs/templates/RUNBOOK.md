# RUNBOOK (optional, worth zero points)

> Optional. Worth **zero points**, mentioned only because a reviewer with two submissions of equal
> score will prefer the one that could be handed to an operations team on a Friday. Half a page is
> plenty. Copy to `connector/RUNBOOK.md` if you want it.

## What this run does

*One paragraph: what it reads, what it writes, what it changes in the ERP, and what it never does.*

## How to run it

```
# nightly
connector/run.sh run --run-id <id> \
  --erp-base-url http://127.0.0.1:8082 --twin-base-url http://127.0.0.1:8081 \
  --erp-export-dir var/erp/export --twin-drop-dir var/miniblp/inbox/incoming \
  --state-dir var/connector/state --report-dir var/connector/reports
```

*Which environment variables must be set, where the state directory lives, and what a normal run
prints.*

## What it does on failure

| Exit code | Meaning | Operator action |
|---|---|---|
| 0 | clean | none |
| 2 | completed with business exceptions | read `exceptions.csv`, hand the rows to AP |
| 3 | hard failure | *what you want them to do* |

*Also: what is safe to re-run, what is left behind mid-run, and why re-running is safe.*

## When a nightly run fails, check in this order

1. *the cheapest check that most often explains it*
2. *the second*
3. *the third*

*For each, name the file or URL to look at and what a healthy answer looks like: report files, the
twin's exception queue and receipts, the ERP's request log, the state directory.*

## What needs a human, always

*The conditions you deliberately never automate, and who they go to.*
