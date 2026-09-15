// Package erp is the mock ERP of the integrations challenge: the system of
// record for suppliers, purchase orders, unit-of-measure conversions and
// exchange rates, the receiver of accounts payable postings, and the writer of
// the subsidiary's legacy file drop.
//
// It serves three deliberately different wire contracts over one landscape,
// because that is what a first ERP integration actually looks like:
//
//   - a REST surface under /erp/v1 where money is a plain decimal string and
//     dates are YYYY-MM-DD,
//   - one SOAP 1.1 operation at /soap/FinancialReferenceDataService where rates
//     carry the ERP host locale (decimal comma), the response is ISO-8859-1 and
//     the namespace prefixes deliberately differ from the documentation,
//   - a file export drop of genuine CP1252 KRED-EXP bytes with an .ok sentinel.
//
// Everything the service serves comes from [github.com/fatjonblp/coding_challange_integrations/internal/seed].
// Nothing is invented here and nothing is read from a clock: the whole surface is
// a pure function of (scenario, seed) plus the requests received, so two servers
// started with the same seed answer the same request script with byte-identical
// bodies and identical metrics. That is asserted by TestDeterminismTwoServers.
//
// # Determinism rules this package obeys
//
//   - No wall clock. Time is the [github.com/fatjonblp/coding_challange_integrations/internal/simclock]
//     virtual clock, which only moves when a request declares a cost.
//   - No randomness. The only variation is the seed, through seed.Generate, the
//     Governor's content-addressed fault injection and the seed-derived
//     credentials in [Credentials].
//   - No map iteration reaches a response body. Every list is an explicitly
//     sorted slice; the two maps that do get marshalled (the by-endpoint and
//     rejection counters) are sorted by encoding/json.
//   - No float64 in a money or quantity path. Amounts are
//     [github.com/fatjonblp/coding_challange_integrations/internal/model.Decimal]
//     and the one tolerance comparison is integer arithmetic over minor units.
//
// # Error bodies carry no answers
//
// Per section 17.2 of the build specification, no error body and no business
// rejection may carry a value the client was supposed to compute. A rejection
// names the field and the rule it violated, never the expected amount, rate,
// quantity or key. TestNoErrorBodyCarriesASeededValue enforces that mechanically
// over every error path in the package.
package erp
