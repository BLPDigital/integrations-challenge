# Example reports

Generated from the **S0 smoke seed only**, on purpose: a scored scenario's
expected report is an answer key, and shipping one would replace the exercise
with a copying exercise.

They are here for the SHAPE. The columns, the field names and the balances are
the contract; the numbers are S0's and mean nothing for S1 or S2.

All three are complete and mutually consistent, so the validator passes on them:

    make validate-reports DIR=examples/reports

Run that against your own reports before you run a scenario. It catches the
cheap mistakes (a missing mandatory field, a renamed column, a posted-row count
that disagrees with `run.json`) in a second rather than in minute nine of a
grading run.

One thing the terminal hides: `subject_key` in `exceptions.csv` is the natural
key verbatim, and a composite one joins its parts with the ASCII unit separator
`0x1f`. `od -c` shows it; `cat` does not. That is deliberate: the key stays
reversible, and no component can ever contain the separator.

Regenerate them yourself any time:

    make up SCENARIO=S0
    make scenario S=S0
    ls grading/out/*/S0/reports/
