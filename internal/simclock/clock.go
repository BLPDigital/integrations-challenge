// Package simclock provides the deterministic execution substrate shared by the
// erp and miniblp services: a virtual clock, a token bucket, a hard request
// quota, seed-keyed fault injection, an in-memory access-token lifecycle and
// request accounting.
//
// The package never reads the wall clock, never sleeps, never iterates a map in
// a way that reaches an output, and contains no floating point. Time only moves
// when a request declares a simulated cost, so a run is a pure function of its
// configuration and its call sequence. Costs are carried in hundredths of a
// virtual millisecond (vms100) because the published cost table has fractional
// per-record components; the fixed-point accumulator keeps them exact.
//
// A server holds one Governor. Every non-admin request calls
// Governor.Admit exactly once, which advances the clock, charges the bucket and
// the quota, assigns the request sequence number and returns the Decision the
// handler must honor. Admin endpoints bypass the Governor entirely: they cost
// nothing and count nothing.
package simclock

// Vms100PerVms is the fixed-point scale of the virtual clock: costs and clock
// readings are expressed in hundredths of a virtual millisecond.
const Vms100PerVms = 100

// A Clock is a monotone virtual clock measured in hundredths of a virtual
// millisecond. It has no synchronization of its own; callers hold the Governor
// mutex.
type Clock struct{ vms100 int64 }

// Advance adds delta hundredths of a virtual millisecond to the clock and
// returns the new reading. Advance panics on a negative delta: the clock is
// monotone by construction.
func (c *Clock) Advance(delta int64) int64 {
	if delta < 0 {
		panic("simclock: negative clock advance")
	}
	c.vms100 += delta
	return c.vms100
}

// Now100 returns the current reading in hundredths of a virtual millisecond.
func (c *Clock) Now100() int64 { return c.vms100 }

// Now returns the current reading in whole virtual milliseconds, truncated.
// This is the value reported as virtual_clock_ms and logged as vms.
func (c *Clock) Now() int64 { return c.vms100 / Vms100PerVms }
