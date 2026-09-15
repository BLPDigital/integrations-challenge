package model

import (
	"math"
	"math/big"
	"testing"
)

// toBig converts a u128 into a big.Int. math/big appears in tests only, as an
// independent oracle for the hand-rolled 128-bit arithmetic; the production path
// never imports it.
func toBig(a u128) *big.Int {
	x := new(big.Int).SetUint64(a.hi)
	x.Lsh(x, 64)
	return x.Or(x, new(big.Int).SetUint64(a.lo))
}

// lcg is a deterministic 64-bit linear congruential generator so the fuzz-style
// cases below are the same on every run and on every machine.
type lcg uint64

func (s *lcg) next() uint64 {
	*s = lcg(uint64(*s)*6364136223846793005 + 1442695040888963407)
	return uint64(*s)
}

func TestU128MulPow10(t *testing.T) {
	tests := []struct {
		name string
		a    uint64
		n    int
		ok   bool
	}{
		{"zero", 0, 18, true},
		{"one", 1, 0, true},
		{"max uint64 times ten", math.MaxUint64, 1, true},
		{"max uint64 times 1e19", math.MaxUint64, 19, true},
		{"max uint64 times 1e20", math.MaxUint64, 20, false},
		{"max int64 times 1e9", math.MaxInt64, 9, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := u128From(tc.a).mulPow10(tc.n)
			if ok != tc.ok {
				t.Fatalf("mulPow10 ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			want := new(big.Int).Mul(new(big.Int).SetUint64(tc.a),
				new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tc.n)), nil))
			if toBig(got).Cmp(want) != 0 {
				t.Fatalf("mulPow10 = %v, want %v", toBig(got), want)
			}
		})
	}
}

func TestU128DivMod(t *testing.T) {
	// Fixed cases exercising every branch: both halves zero, 64-bit fast path,
	// bits.Div64 path, and the long-division path with a divisor above 2^64.
	fixed := [][2]u128{
		{u128From(0), u128From(1)},
		{u128From(7), u128From(3)},
		{u128From(math.MaxUint64), u128From(10)},
		{mul64(math.MaxUint64, math.MaxUint64), u128From(1)},
		{mul64(math.MaxUint64, math.MaxUint64), u128From(math.MaxUint64)},
		{mul64(math.MaxUint64, math.MaxUint64), mul64(math.MaxUint64, 2)},
		{mul64(1<<63, 1<<63), u128{hi: 1 << 62, lo: 12345}},
		{u128{hi: math.MaxUint64, lo: math.MaxUint64}, u128{hi: 1, lo: 0}},
		{u128{hi: 1, lo: 0}, u128{hi: math.MaxUint64, lo: math.MaxUint64}},
	}
	var s lcg = 20260831
	cases := fixed
	for i := 0; i < 400; i++ {
		a := u128{hi: s.next() >> (s.next() % 64), lo: s.next()}
		b := u128{hi: s.next() >> (s.next() % 96), lo: s.next()}
		if b.isZero() {
			b = u128From(1)
		}
		cases = append(cases, [2]u128{a, b})
	}
	for i, tc := range cases {
		a, b := tc[0], tc[1]
		q, r := a.divMod(b)
		ba, bb := toBig(a), toBig(b)
		wantQ, wantR := new(big.Int).QuoRem(ba, bb, new(big.Int))
		if toBig(q).Cmp(wantQ) != 0 || toBig(r).Cmp(wantR) != 0 {
			t.Fatalf("case %d: %v/%v = (%v,%v), want (%v,%v)",
				i, ba, bb, toBig(q), toBig(r), wantQ, wantR)
		}
	}
}

func TestU128CmpAddSub(t *testing.T) {
	a := u128{hi: 1, lo: 0}
	b := u128{hi: 0, lo: math.MaxUint64}
	if a.cmp(b) != 1 || b.cmp(a) != -1 || a.cmp(a) != 0 {
		t.Fatal("cmp is wrong across the 64-bit boundary")
	}
	if sum, ok := b.add(u128From(1)); !ok || sum.cmp(a) != 0 {
		t.Fatalf("add across the boundary = %v, %v", sum, ok)
	}
	full := u128{hi: math.MaxUint64, lo: math.MaxUint64}
	if _, ok := full.add(u128From(1)); ok {
		t.Fatal("add did not report overflow")
	}
	if got := a.sub(u128From(1)); got.cmp(b) != 0 {
		t.Fatalf("sub across the boundary = %v", got)
	}
}

func TestU128DivModByZeroPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("divMod by zero did not panic")
		}
	}()
	u128From(1).divMod(u128{})
}
