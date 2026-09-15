# DECISIONS

> Copy this file to `DECISIONS.md` at the repository root and replace every italic prompt.
>
> **ONE PAGE. Hard cap.** Roughly 500 words, six headings, no architecture essay. A short specific
> note about a contradiction you found is worth more than a long one about your layering. We read
> this before we read your code, and the review conversation is built from it.

## 1. Channel chosen, and what I gave up

*Which ingest channel (file importer or REST ingest), in one sentence, plus the property of the other
channel you knowingly gave up and when that would change your mind.*

## 2. Format chosen, and what I gave up

*Which wire format (csv, xml, json, ndjson), what it cost you in the record shapes it cannot express,
and what you would have picked at 100x the volume.*

## 3. Ambiguities I found, the reading I chose, and the evidence

*One line per ambiguity: where it is (document, section, field), the two readings, which one you
implemented, and what in the data or the document makes that the defensible one. Say explicitly where
you surfaced it rather than resolved it. This section is scored on evidence, not on confidence.*

## 4. What I would ask the customer

*The questions you would send, in the order you would send them, and what you did in the meantime so
the run is not blocked on an answer. Include anything you believe we got wrong.*

## 5. Where I stopped

*What is finished, what is half-finished, what is not started, and what a reviewer will see fail if
they run it. Name the scenario or the assertion. An accurate stop line scores; an optimistic one
costs more than it saves.*

## 6. What I would do next

*The next three things, in priority order, with a rough cost each, and why in that order.*

---

## What the rubric scores here

10 points: up to 6 for the dimensions you actually engage with (one point each, listed below), 2 for
naming ambiguities with evidence, 2 for an honest stop line. A write-up that contradicts the servers'
own counters scores zero for the block, so check `run.json` against
`GET /erp-admin/v1/metrics` and `GET /admin/v1/metrics` before you write numbers down.

The seven dimensions the reviewers score independently. Engaging six of them is full marks, so pick
the ones your work actually touched rather than writing a line about each.

1. Idempotency and delivery semantics: how your key is derived, why it is stable across runs, what
   happens on a re-delivery.
2. Failure taxonomy and exception-driven behavior: what is retriable, what is an exception, what is
   a hard failure, and why exit code 2 is not a failure.
3. Efficiency and API citizenship: page sizes, batching, how you stay inside the quota, what you do
   about 429 and 503.
4. Money, encoding, identifiers and units: rounding rule and where rounding is allowed at all,
   CP1252, leading zeros, UoM conversion, what you refused to convert.
5. State, incrementality and recovery: what you persist, where the watermark lives, what a second
   run does, what a crash mid-run leaves behind.
6. Spec fidelity on `kredexp-2.1` and Go craft: which diagnostic fires where, what you did about
   the trailer, what you did not implement.
7. Judgment, communication and ownership: the SLA conflict, what you escalated, what you decided
   not to post, and what you would tell the customer.
