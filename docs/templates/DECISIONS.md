# DECISIONS

> Copy this file to `DECISIONS.md` at the repository root and replace every italic prompt.
>
> **ONE PAGE. Hard cap.** Roughly 500 words, four headings, no architecture essay. A short specific
> note about a contradiction you found is worth more than a long one about your layering. We read
> this before we read your code, and the review conversation is built from it. If your connector
> needs a runtime we would not guess, name it in the first two lines.

## 1. My choices: format, state and the idempotency key

*Which wire format you picked and what it cost you. What you persist between runs, and exactly when
you write it. How your idempotency key is built, and what must never be in it. What your next run
does after a kill between the ERP accepting a posting and your acknowledgment reaching the twin.*

## 2. The customer's request and my answer

*What happens to the unmatched invoices tonight. Two or three sentences the Head of Shared Services
would accept. What you would change so she does not get "a report with hundreds of rows" again.*

## 3. Where I stopped

*What is finished, what is half-finished, what is not started, and what a reviewer will see fail if
they run it. Name the scenario or the assertion. An accurate stop line scores; an optimistic one
costs more than it saves.*

## 4. What I would do next

*The next three things, in priority order, with a rough cost each, and why in that order. Include
anything you believe we got wrong, with the evidence.*

---

## What the rubric scores here

20 points, scored by two reviewers independently:

- **Up to 10 for the dimensions you engage**, 2 each. Engaging five of the six is full marks, so pick
  the ones your work actually touched rather than writing a line about each.
- **5 for the customer's request**: what happens to the unmatched invoices, what you tell her, and
  what you would change for her.
- **5 for an honest stop line.**

A write-up that contradicts the servers' own counters scores zero for the block, so check
`run.json` against `GET /erp-admin/v1/metrics` and `GET /admin/v1/metrics` before you write numbers
down.

The six dimensions:

1. Idempotency: how your key is derived, why it is stable across runs, what a repeated run does.
2. Failure taxonomy: what is retriable, what is an exception, what is a hard failure, and why exit
   code 2 is not a failure.
3. Efficiency: page sizes, batching, how you stay inside the quota, what you do about 429 and 503.
4. Identifiers and data: leading zeros, purchase order numbers, masked bank data, what you refused
   to guess.
5. State and recovery: what you persist, where the watermark lives, what a second run does, what a
   crash mid-run leaves behind.
6. Format choice: why that wire format, what it cannot express, and what you would pick at 100 times
   the volume.
