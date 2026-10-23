// Package seed is the deterministic dataset generator for the integration
// challenge. Everything both services serve, and every trap the grader scores,
// originates here: master data, the SOAP exchange rate table, the legacy
// KRED-EXP delivery bytes and the golden expected-exception set.
//
// The contract is [Generate]. Identical (scenario, seed) pairs yield
// byte-identical output, including the legacy file bytes, so a scenario can be
// replayed three times in CI and compared byte for byte.
//
// # How determinism is achieved
//
// There is no wall clock anywhere in this package: every date is a constant of
// the scenario, and no output ever depends on the time of generation. All
// pseudo-randomness comes from a single [math/rand.Rand] constructed explicitly
// from the scenario seed (see rng.go); it is drawn in one fixed phase order,
// documented at [Generate]. Maps are used for lookups only and are never ranged
// over on a path that reaches an output, a hash or an ordering; every slice that
// leaves this package is explicitly sorted or built in a deterministic order.
// No float64 appears in any money or quantity path: amounts and quantities are
// [github.com/fatjonblp/coding_challange_integrations/internal/model.Decimal].
//
// # What is deliberately wrong in the data
//
// Most of this package exists to plant defects on purpose. Supplier numbers
// collide when leading zeros are stripped, cost center "0815" and cost center
// "815" are two different cost centers, purchase order numbers arrive in three
// different spellings, exchange rate rows are superseded out of document order,
// and the legacy delivery is genuine CP1252 with quoted semicolons, embedded
// CRLFs and trailing minus signs. Each planted defect is commented at its
// construction site with the behavior it is meant to catch.
package seed
