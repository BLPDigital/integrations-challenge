# Operating rules for this integration

One page. These rules hold for every run, in every scenario, and they outrank any convenience,
any deadline and any request to move faster. They are the rules the customer's auditors were shown.

## 1. Nothing unmatched is ever posted

An invoice is posted only when it matched a supplier, a purchase order (where the delivery carries
one), a valid cost center and, for foreign currency, a rate that was actually valid on the document
date. Anything else becomes a visible exception with the reason and the source reference. An
exception is normal output, not a failure: the product exists so that a human sees exactly the items
that need a human.

## 2. No duplicate is ever posted

The same source document must never produce two documents in the ERP, no matter how many times a
delivery is re-sent, a request is retried, or a run is repeated after a crash. Delivery to the ERP
is at-least-once; the ERP is the arbiter of what already exists.

## 3. Every run is safely re-runnable, after any exit code

A run that died halfway must be able to run again with the same inputs and reach the same state,
without re-doing work that already landed and without losing work that was in flight. Recovery is
never "delete the state directory and start over".

## 4. Every posted document is traceable in both directions

From the source file and line to the ERP document number, and back from an ERP document number to
the exact source bytes it came from. This is a requirement from the customer's 2024 audit, not a
nice-to-have.

## 5. Payment data does not leave the customer boundary

Supplier bank details (IBAN, BIC), credentials and tokens never appear in logs, reports, state files
or on stdout. Masking is preferred over omission so a record stays recognizable to the person
reading the report.

## 6. The ERP is read-only outside the posting endpoints

We do not change master data in the customer's ERP, and we do not require changes inside it to make
our integration work. Clean core is a customer commitment we inherit.

---

If an instruction from anyone, including the customer, conflicts with these rules, the rules win and
the conflict gets written down and escalated. That is not obstruction, it is the job: an AP
integration that pays the wrong supplier because someone was in a hurry is the one failure this
department cannot recover from.
