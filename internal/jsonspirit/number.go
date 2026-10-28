package jsonspirit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxPow10 bounds the fractions and exponents read: within it every power of
// ten used is a normal double whose glibc value is known.
const maxPow10 = 300

// number is number_: strict_real_p | int64_p | uint64_p
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L515-L519 at v19.2.7
// and v20.2.4), its text as the writer's output_int and its output of a
// double write the value
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_writer_template.h#L213-L252 at v19.2.7
// and v20.2.4).
func (p *parser) number() (string, bool) {
	if d, ok := p.strictReal(); ok || p.err != nil {
		return formatReal(d), ok
	}
	if v, ok := p.int64(); ok {
		return strconv.FormatInt(v, 10), true
	}
	if v, ok := p.uint64(); ok {
		return strconv.FormatUint(v, 10), true
	}
	return "", false
}

// formatReal is Generator::output(double) without remove_trailing_zeros,
// append_double(os, d, 17): std::showpoint with setprecision(17), which is
// printf's %#.17g.
func formatReal(d float64) string { return fmt.Sprintf("%#.17g", d) }

// digitRun is the length of the run of decimal digits at s[i:].
func digitRun(s string, i int) int {
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	return j - i
}

// accumulate is extract_int with positive_accumulate<double, 10>
// (boost/spirit/home/classic/core/primitives/impl/numerics.ipp): each digit
// multiplies by ten and adds, rounding at each step, and a value past
// DBL_MAX fails.
func accumulate(digits string) (float64, bool) {
	var n float64
	for i := range len(digits) {
		d := float64(digits[i] - '0')
		if n > math.MaxFloat64/10 {
			return 0, false
		}
		n = float64(n * 10)
		if n > math.MaxFloat64-d {
			return 0, false
		}
		n = float64(n + d)
	}
	return n, true
}

// strictReal is real_parser_impl::parse_main (numerics.ipp) with
// strict_real_parser_policies<double>: an optional sign, digits, and a dot
// or an exponent, at least one of which must be present. The value is built
// as the parser builds it, not correctly rounded: the integer and fraction
// digits accumulated as doubles, the fraction scaled by pow(10, -digits) and
// the whole by pow(10, exponent), so 0.3 is 3 * 0.1, 0.30000000000000004.
// An integer part past DBL_MAX matches nothing, as the digit it overflows at
// is no dot. A real whose value depends on the host's libm is refused: an
// exponent past ±maxPow10 or a fraction of more than maxPow10 digits, whose
// powers of ten the glibcPow10 table does not hold, the exponent 126, and a
// value that is not finite, which json_spirit writes as inf or nan.
func (p *parser) strictReal() (float64, bool) {
	i := p.pos
	neg := false
	if i < len(p.s) && (p.s[i] == '+' || p.s[i] == '-') {
		neg = p.s[i] == '-'
		i++
	}
	intDigits := digitRun(p.s, i)
	n, ok := accumulate(p.s[i : i+intDigits])
	if !ok {
		return 0, false
	}
	got := intDigits > 0
	i += intDigits
	if neg {
		n = -n
	}
	eHit := false
	if i < len(p.s) && p.s[i] == '.' {
		i++
		fracDigits := digitRun(p.s, i)
		if fracDigits > maxPow10 {
			p.refuse("a run of %d digits", fracDigits)
			return 0, false
		}
		switch {
		case fracDigits > 0:
			f, _ := accumulate(p.s[i : i+fracDigits])
			f = float64(f * glibcPow10(-fracDigits))
			if neg {
				n -= f
			} else {
				n += f
			}
			i += fracDigits
		case !got:
			return 0, false
		}
		eHit = i < len(p.s) && (p.s[i] == 'e' || p.s[i] == 'E')
	} else {
		if !got {
			return 0, false
		}
		eHit = i < len(p.s) && (p.s[i] == 'e' || p.s[i] == 'E')
		if !eHit {
			return 0, false
		}
	}
	if eHit {
		neg, digits, next, ok := parseExponent(p.s, i+1)
		if !ok {
			return 0, false
		}
		digits = strings.TrimLeft(digits, "0")
		exp := 0
		if len(digits) <= 3 {
			for _, c := range []byte(digits) {
				exp = exp*10 + int(c-'0')
			}
		}
		if len(digits) > 3 || exp > maxPow10 {
			p.refuse("exponent of magnitude %.40s", digits)
			return 0, false
		}
		if neg {
			exp = -exp
		}
		if exp == 126 {
			p.refuse("exponent 126, whose glibc pow depends on the host CPU")
			return 0, false
		}
		n *= glibcPow10(exp)
		i = next
	}
	if math.IsInf(n, 0) || math.IsNaN(n) {
		p.refuse("a non-finite number")
		return 0, false
	}
	p.pos = i
	return n, true
}

// parseExponent is ureal_parser_policies<double>::parse_exp_n, an
// int_parser<double> at i: an optional sign and any run of digits, which
// the caller bounds.
func parseExponent(s string, i int) (neg bool, digits string, next int, ok bool) {
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	n := digitRun(s, i)
	return neg, s[i : i+n], i + n, n > 0
}

// int64 is int64_p: int_parser<int64_t>, an optional sign and digits,
// failing on overflow.
func (p *parser) int64() (int64, bool) {
	i := p.pos
	neg := false
	if i < len(p.s) && (p.s[i] == '+' || p.s[i] == '-') {
		neg = p.s[i] == '-'
		i++
	}
	n := digitRun(p.s, i)
	if n == 0 {
		return 0, false
	}
	text := p.s[i : i+n]
	if neg {
		text = "-" + text
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	p.pos = i + n
	return v, true
}

// uint64 is uint64_p: uint_parser<uint64_t>, digits without a sign,
// failing on overflow.
func (p *parser) uint64() (uint64, bool) {
	n := digitRun(p.s, p.pos)
	if n == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(p.s[p.pos:p.pos+n], 10, 64)
	if err != nil {
		return 0, false
	}
	p.pos += n
	return v, true
}

// glibcPow10 is pow(10, k) as the glibc of the Ceph images computes it for
// |k| <= maxPow10: the correctly rounded power except at k = 23 and
// k = 210, where glibc returns the next double up (checked with math.pow in
// the v19.2.6 and v20.2.4 images against float("1e<k>")). The table assumes
// glibc on an FMA-capable host: glibc picks its pow by CPU, and without FMA
// k = 126 also differs, so strictReal refuses that exponent.
func glibcPow10(k int) float64 {
	v, err := strconv.ParseFloat("1e"+strconv.Itoa(k), 64)
	if err != nil {
		panic(fmt.Sprintf("jsonspirit: 1e%d: %v", k, err))
	}
	if k == 23 || k == 210 {
		return math.Nextafter(v, math.Inf(1))
	}
	return v
}
