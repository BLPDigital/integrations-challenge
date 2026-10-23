// Package importer is the file-reading framework of the digital twin: the
// [Format] interface every importer satisfies, the registry that finds one by
// id, the [Diagnostic] vocabulary they report in, the [Result] they return, and
// the documented [Projection] the golden tests compare.
//
// The load-bearing rule of the whole package: a defect in the DATA is never an
// error. A truncated line, a misspelled currency, an amount with four decimals
// where two are allowed, a missing trailer - each is a [Diagnostic] on the
// [Result], and Parse still returns a non-nil Result. The error return is
// reserved for programming and environment faults: a nil or cancelled context,
// an unreadable stream, an impossible internal state. A caller therefore always
// has something to report, and a bad file can never take a batch down with it.
//
// Three further properties hold for every registered format and are asserted by
// the tests in this package:
//
//   - Determinism. Identical raw bytes and identical [Options] always produce a
//     byte-identical [Projection]. Nothing here reads a clock, a locale, a
//     random source or the filesystem, and no map is ever ranged over into an
//     output.
//   - Exactness. Money and quantities are [model.Decimal]; no float64 appears
//     on any parsing path. An input is never rounded: a value carrying more
//     fraction digits than its field allows is [CodeMoneyScale], because
//     rounding an input is how a supplier gets paid the wrong amount.
//   - Bounded memory. [ParseStream] is the entry point the twin uses, and a
//     format that implements [StreamParser] never materializes the file. The
//     []byte [Format.Parse] the interface mandates is defined in terms of it.
//
// The three canonical formats the twin ships live in
// internal/importer/canonical. The legacy KRED-EXP 2.1 importer lives in
// internal/importer/kredexp and is the candidate's task. Both are reached by
// blank-importing internal/importer/all, which is the one place a format is
// wired in.
package importer
