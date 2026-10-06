package op

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
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
	s := rgwtext.CString(input)
	v, end, _ := rgwtext.Strtoll(s)
	if end == 0 || strings.TrimLeft(s[end:], cSpace) != "" {
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

// cInt is v as a C int holds it once a long is assigned to one: the low 32
// bits, as GCC converts.
func cInt(v int64) int64 {
	return int64(int32(v)) //nolint:gosec // parse_value_and_bound assigns a long to an int
}
