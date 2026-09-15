package model

import "math/bits"

// u128 is an unsigned 128-bit integer held as two uint64 halves. It exists so the
// money primitives can compute an int64*int64 product and divide it again without
// float64, math/big or a silent overflow. Only the operations MulRate and Cmp need
// are implemented.
type u128 struct {
	hi, lo uint64
}

// u128From widens v.
func u128From(v uint64) u128 { return u128{lo: v} }

// mul64 returns the exact 128-bit product a*b.
func mul64(a, b uint64) u128 {
	hi, lo := bits.Mul64(a, b)
	return u128{hi: hi, lo: lo}
}

// isZero reports whether a is zero.
func (a u128) isZero() bool { return a.hi == 0 && a.lo == 0 }

// cmp returns -1, 0 or +1 as a is less than, equal to or greater than b.
func (a u128) cmp(b u128) int {
	switch {
	case a.hi != b.hi:
		if a.hi < b.hi {
			return -1
		}
		return 1
	case a.lo != b.lo:
		if a.lo < b.lo {
			return -1
		}
		return 1
	}
	return 0
}

// add returns a+b and reports whether the sum fits in 128 bits.
func (a u128) add(b u128) (u128, bool) {
	lo, carry := bits.Add64(a.lo, b.lo, 0)
	hi, carry := bits.Add64(a.hi, b.hi, carry)
	return u128{hi: hi, lo: lo}, carry == 0
}

// sub returns a-b truncated to 128 bits. Callers must ensure the mathematical
// difference is representable; the wrapping case is used deliberately by divMod.
func (a u128) sub(b u128) u128 {
	lo, borrow := bits.Sub64(a.lo, b.lo, 0)
	hi, _ := bits.Sub64(a.hi, b.hi, borrow)
	return u128{hi: hi, lo: lo}
}

// mulPow10 returns a*10^n and reports whether the product fits in 128 bits.
// n must not be negative.
func (a u128) mulPow10(n int) (u128, bool) {
	for ; n > 0; n-- {
		low := mul64(a.lo, 10)
		carry, high := bits.Mul64(a.hi, 10) // carry is the part above 128 bits
		if carry != 0 {
			return u128{}, false
		}
		sum, ok := low.add(u128{hi: high})
		if !ok {
			return u128{}, false
		}
		a = sum
	}
	return a, true
}

// bit returns bit i of a, counting from the least significant bit.
func (a u128) bit(i uint) uint64 {
	if i >= 64 {
		return (a.hi >> (i - 64)) & 1
	}
	return (a.lo >> i) & 1
}

// setBit returns a with bit i set.
func (a u128) setBit(i uint) u128 {
	if i >= 64 {
		a.hi |= 1 << (i - 64)
		return a
	}
	a.lo |= 1 << i
	return a
}

// divMod returns the quotient and remainder of a/b. It panics if b is zero,
// which is a programming error: every caller validates its divisor first.
func (a u128) divMod(b u128) (q, r u128) {
	if b.isZero() {
		panic("model: u128 division by zero")
	}
	if a.cmp(b) < 0 {
		return u128{}, a
	}
	if b.hi == 0 {
		if a.hi == 0 {
			return u128{lo: a.lo / b.lo}, u128{lo: a.lo % b.lo}
		}
		if a.hi < b.lo { // bits.Div64 requires hi < y
			quo, rem := bits.Div64(a.hi, a.lo, b.lo)
			return u128{lo: quo}, u128{lo: rem}
		}
	}
	// Restoring binary long division. 128 iterations, no float, no allocation.
	for i := 127; i >= 0; i-- {
		carry := r.hi >> 63
		r.hi = r.hi<<1 | r.lo>>63
		r.lo = r.lo << 1
		r.lo |= a.bit(uint(i))
		// carry==1 means the shifted remainder exceeds 128 bits and is therefore
		// larger than b; the wrapping subtraction yields the correct low 128 bits.
		if carry == 1 || r.cmp(b) >= 0 {
			r = r.sub(b)
			q = q.setBit(uint(i))
		}
	}
	return q, r
}
