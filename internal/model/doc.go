// Package model is the canonical accounts-payable domain of this landscape: the
// exact decimal and money types, the master-data and invoice records, their
// natural keys and validation vocabulary, the canonical JSON encoding and the
// content hash built on it.
//
// Three properties hold throughout and are relied on by every other package:
//
//   - Exactness. Money and quantities are [Decimal], an integer unscaled value
//     with a scale, and never float64. [Decimal.MulRate] is the single
//     conversion primitive and rounds half away from zero, the commercial
//     rounding the customer's finance department expects.
//   - Determinism. Nothing here reads a clock, a random source, a locale or a
//     time zone database, and nothing depends on map iteration order. Given the
//     same input, every function in this package returns the same bytes on every
//     machine and every run. The Europe/Zurich rules are computed from the EU
//     daylight saving definition (see [ZurichOffsetMinutes]) precisely so a
//     missing tzdata cannot move a posting date.
//   - Canonical form. [CanonicalJSON] is the one encoding that gets hashed or
//     compared, and [ContentHash] is its sha256. The same logical record encoded
//     from any channel or wire format therefore yields identical bytes.
//
// Keys are strings, trimmed, case-sensitive, and their leading zeros are
// significant: supplier "0000417" is not supplier "417". Composite keys are
// joined with the unit separator (see [KeySeparator]).
//
// The deliberate ambiguities of the source data - the raw discount field, the
// empty invoice line cost center, the sign of a credit note - are represented,
// never resolved. The validators leave them alone so consumers can surface them.
package model
