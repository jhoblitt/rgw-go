package op

import (
	"math"
	"strings"
)

// cSpace is the white space isspace and strtol accept in the C locale, which
// radosgw runs in.
const cSpace = " \t\n\v\f\r"

// ParseValueAndBound is radosgw's parse_value_and_bound (rgw_op.h:2633-2662
// at v19.2.6, :2820-2849 at v20.2.4), which parses the max-keys, max-parts and
// max-uploads list limits. An empty input is def. Any other input is read as
// the C code reads it: only up to its first NUL, by strtol in base 10, which
// skips leading white space and saturates at the long limits. The value is
// clamped to upper, then to lower. Every value the result takes is held in a
// C int, which keeps the low 32 bits, so 2147483648 turns negative before the
// clamp. Input with no digits, or with anything but white space after them,
// is ErrInvalidArgument.
func ParseValueAndBound(input string, lower, upper, def int64) (int, error) {
	if input == "" {
		return int(cInt(def)), nil
	}
	s, _, _ := strings.Cut(input, "\x00")
	v, rest, ok := strtol10(s)
	if !ok || strings.TrimLeft(rest, cSpace) != "" {
		return 0, ErrInvalidArgument
	}
	out := cInt(v)
	if out > upper {
		out = cInt(upper)
	}
	if out < lower {
		out = cInt(lower)
	}
	return int(out), nil
}

// strtol10 is strtol(s, &end, 10) in the C locale: leading white space, an
// optional sign, then digits, saturating at the int64 limits. rest follows
// the digits; ok is false when there are none.
func strtol10(s string) (v int64, rest string, ok bool) {
	t := strings.TrimLeft(s, cSpace)
	neg := strings.HasPrefix(t, "-")
	if neg || strings.HasPrefix(t, "+") {
		t = t[1:]
	}
	n := 0
	for n < len(t) && '0' <= t[n] && t[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, "", false
	}
	for i := range n {
		d := int64(t[i] - '0')
		if neg {
			if v < (math.MinInt64+d)/10 {
				return math.MinInt64, t[n:], true
			}
			v = v*10 - d
			continue
		}
		if v > (math.MaxInt64-d)/10 {
			return math.MaxInt64, t[n:], true
		}
		v = v*10 + d
	}
	return v, t[n:], true
}

// cInt is v as a C int holds it once a long is assigned to one: the low 32
// bits, as GCC converts.
func cInt(v int64) int64 {
	return int64(int32(v)) //nolint:gosec // parse_value_and_bound assigns a long to an int
}
