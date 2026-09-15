// Package httpx carries the HTTP plumbing shared by the erp and miniblp
// services: the canonical error body, four-format content negotiation on both
// the request and the response side, an idempotency store, the middleware that
// wires the simclock Governor into a handler chain, bearer and admin-token
// authentication, and opaque tamper-evident pagination cursors.
//
// The package never reads the wall clock, never sleeps, never lets map
// iteration order reach an output and never parses a number into a float64.
// Every byte it writes is a pure function of the request and of the Governor's
// state, so two runs of the same scenario produce byte-identical transcripts.
//
// Determinism rules that callers must not break:
//
//   - Response bodies are marshalled from structs, sorted key sets or the
//     caller's own byte slices; a map is never ranged over into an output.
//   - Accept quality values are parsed as fixed-point thousandths, not floats.
//   - Idempotency eviction follows insertion order, never map order.
//   - Cursors are signed with a server secret and carry no counter, so the same
//     logical position always encodes to the same string.
package httpx
