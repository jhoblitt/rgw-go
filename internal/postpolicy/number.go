package postpolicy

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxPow10 bounds the powers of ten a real may need: within it every power
// is a normal double whose glibc value is known.
const maxPow10 = 300

// numberText is the text JSONObj holds for lit, a number Go's decoder read:
// the value json_spirit's number_ rule makes of it, strict_real_p, then
// int64_p, then uint64_p
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L515-L519 at v19.2.7
// and v20.2.4), written as the writer's output_int and its output of a
// double write it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_writer_template.h#L213-L252 at v19.2.7
// and v20.2.4). A number with a fraction or an exponent is a real, written
// as printf's %#.17g, which append_double's showpoint and precision 17 are;
// any other is an int64, or else a uint64, in decimal; an integer neither
// holds matches no alternative, which json_spirit refuses.
func numberText(lit string) (string, error) {
	if !strings.ContainsAny(lit, ".eE") {
		if v, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return strconv.FormatInt(v, 10), nil
		}
		if v, err := strconv.ParseUint(lit, 10, 64); err == nil {
			return strconv.FormatUint(v, 10), nil
		}
		return "", fmt.Errorf("the integer %.40s fits neither int64 nor uint64", lit)
	}
	d, err := spiritReal(lit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%#.17g", d), nil
}

// spiritReal is the double strict_real_p builds for lit, as
// real_parser_impl::parse_main in boost's classic spirit builds it
// (boost/spirit/home/classic/core/primitives/impl/numerics.ipp in boost
// 1.82): the integer digits accumulated one at a time in a double, then the
// fraction's digits likewise, scaled by pow(10, -digits) and added, then the
// whole multiplied by pow(10, exponent). Its rounding is not strtod's: 0.3
// is 3 * 0.1, 0.30000000000000004. A form whose double depends on the
// host's libm is refused: an exponent or a fraction past maxPow10 digits,
// exponent 126, and a result that is not finite, which json_spirit writes as
// inf or nan.
func spiritReal(lit string) (float64, error) {
	s, neg := strings.CutPrefix(lit, "-")
	intDigits, s := digitRun(s)
	n, ok := accumulate(intDigits)
	if !ok {
		return 0, fmt.Errorf("the number %.40s overflows a double", lit)
	}
	if neg {
		n = -n
	}
	if rest, dot := strings.CutPrefix(s, "."); dot {
		var fracDigits string
		fracDigits, s = digitRun(rest)
		f, ok := accumulate(fracDigits)
		if !ok || len(fracDigits) > maxPow10 {
			return 0, fmt.Errorf("the number %.40s has a fraction of %d digits", lit, len(fracDigits))
		}
		f = float64(f * pow10(-len(fracDigits)))
		if neg {
			n = float64(n - f)
		} else {
			n = float64(n + f)
		}
	}
	if s != "" {
		s = s[1:]
		expNeg := strings.HasPrefix(s, "-")
		s = strings.TrimLeft(s, "+-")
		e, ok := accumulate(s)
		if !ok || e > maxPow10 || e == 126 && !expNeg {
			return 0, fmt.Errorf("the number %.40s has an exponent whose power the host's libm decides", lit)
		}
		k := int(e)
		if expNeg {
			k = -k
		}
		n = float64(n * pow10(k))
	}
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, fmt.Errorf("the number %.40s is not finite as a double", lit)
	}
	return n, nil
}

// digitRun splits s after its leading decimal digits.
func digitRun(s string) (digits, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// accumulate is extract_int with positive_accumulate<double, 10>
// (numerics.ipp): each digit multiplies by ten and adds, rounding at each
// step, and a value past DBL_MAX fails the parse.
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

// pow10 is pow(10, k) as the glibc of the Ceph images computes it for
// |k| <= maxPow10: the correctly rounded power but at k = 23 and k = 210,
// where it is the next double up, and at k = 126, which depends on whether
// the CPU has FMA and spiritReal refuses (internal/meta/json_spirit.go:499,
// where rgw-go checked it against both releases' images).
func pow10(k int) float64 {
	v, err := strconv.ParseFloat("1e"+strconv.Itoa(k), 64)
	if err != nil {
		panic(fmt.Sprintf("postpolicy: 1e%d: %v", k, err))
	}
	if k == 23 || k == 210 {
		return math.Nextafter(v, math.Inf(1))
	}
	return v
}
