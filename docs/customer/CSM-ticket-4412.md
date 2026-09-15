# CSM-4412 — Steinbach Industrie AG: AP invoice automation, phase 1

| | |
|---|---|
| Customer | Steinbach Industrie AG (subsidiary, Wil SG) — part of the Steinbach Group |
| Group ERP | central, accessed through the group's integration facade (REST) plus the old financial reference data service (SOAP) |
| Requested by | Customer Success (Zurich) on behalf of the customer's Head of Shared Services |
| Priority | High — the customer's AP team is at 3 FTE and losing one at the end of the quarter |
| Target | first unattended nightly run in production within 3 weeks of kickoff |
| Related | CGL-2210 (access request, done), CGL-2231 (SFTP credentials, done) |

## What the customer wants

The subsidiary captures supplier invoices in a local accounting system and exports them nightly as a
`KRED-EXP` file to SFTP (interface description attached, version 2.1 from 2019, the customer confirms
it is still accurate "apart from the open points at the end"). Today an accountant retypes those
invoices into the group ERP by hand, matching each one against the purchase order and the cost
center, and converting foreign currency invoices with the rate from the group's rate table.

Phase 1 scope, as agreed on the call: pull the master data the matching needs out of the group ERP,
ingest the nightly `KRED-EXP` delivery into our digital twin, match invoices against purchase orders
and cost centers, convert foreign currency amounts to CHF using the group's own rate table, and post
the matched invoices back into the group ERP so the AP team only touches the ones that need a human.

Phase 2 (not in scope now): duplicate detection across the subsidiary and the group, and the payment
run itself.

## Volumes the customer quoted

About 5'000 invoices per delivery in the first migration run, roughly 1'100 per night afterwards.
Master data around 12'000 suppliers and 8'000 open purchase orders. They expect "well under an hour"
for a nightly run and they want the run to be re-runnable if it dies, because their SFTP window and
the ERP's maintenance window overlap on Sundays.

## Notes from the call, verbatim where it matters

The Head of Shared Services was clear about wanting volume through fast, and said this twice:

> "Bucht einfach alles, den Rest räumen wir nachher im SAP auf. Hauptsache die Rechnungen sind im
> System, sonst zahlen wir wieder zu spät."
>
> ("Just post everything, we will clean up the rest in SAP afterwards. The main thing is that the
> invoices are in the system, otherwise we will be paying late again.")

She also mentioned that last year's attempt with an external contractor was abandoned "because the
tool kept stopping on stupid details" and that she does not want to see a report with hundreds of
rows again.

Their IT contact for the interface is the group mailbox from the interface description. There is no
individual support contact and the person who wrote the 2019 document has left the company.

## Open with the customer

- The `Skonto` field question from the interface description was raised on the call. Nobody in the
  room knew the answer. The Head of Shared Services said "das ist doch immer Prozent gewesen" but
  the accountant next to her disagreed.
- The subsidiary sometimes re-sends a delivery unchanged when they think something went wrong. They
  do not tell us when they do.
- Some invoices belong to a second subsidiary (`CH20`) that the group has not finished onboarding.
  The customer is aware and says those "will come later".

## What we owe them

A first run they can watch, a list of what needed a human and why, and the ability to trace any
posted document back to the line in the delivery file it came from. Their auditors asked for that
last point explicitly after the 2024 audit.
