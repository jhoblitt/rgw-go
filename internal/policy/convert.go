package policy

import (
	"encoding/binary"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && asciiEqualFold(s[:len(prefix)], prefix)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// isCSpace is isspace(3) in the C locale.
func isCSpace(c byte) bool { return c == ' ' || '\t' <= c && c <= '\r' }

// hexValue is a hex digit's value; ok is false for any other byte.
func hexValue(c byte) (v byte, ok bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func isHexDigit(c byte) bool {
	_, ok := hexValue(c)
	return ok
}

// AsNumber is Condition::as_number (src/rgw/rgw_iam_policy.h:369-382 at
// v19.2.6, :388-401 at v20.2.4): std::stod, which must consume all of s. It
// takes glibc's strtod syntax in the C locale: leading whitespace, decimal
// and hex floats, a hex float's exponent optional, inf, infinity and nan with
// an optional payload; an overflow or an inexact underflow does not convert.
func AsNumber(s string) (float64, bool) {
	f, n, erange := strtod(s)
	if n == 0 || erange || n < len(s) {
		return 0, false
	}
	return f, true
}

// AsDate is Condition::as_date (src/rgw/rgw_iam_policy.h:384-401 at v19.2.6,
// :403-420 at v20.2.4). When std::stod consumes all of s, s is a count of
// seconds since the epoch, so a bare "2024" is 2024 seconds and not a year.
// Otherwise s is one of from_iso_8601's forms, with a literal Z and nothing
// after it: YYYY-MM, YYYY-MM-DD, YYYY-MM-DDTHHZ, YYYY-MM-DDTHH:MMZ,
// YYYY-MM-DDTHH:MM:SSZ, or seconds with one to nine fraction digits. A year
// before 1970 does not convert, and no field is range-checked: timegm carries
// month 13 into the next year and day 0 back a day. A string whose start
// std::stod cannot convert, or converts out of range, does not convert at
// all.
//
// The instant is ceph::real_time, nanoseconds since the epoch in a uint64:
// one before the epoch or past 2554-07-21T23:34:33.709551615Z wraps modulo
// 2^64, and radosgw compares the wrapped values, so the time.Time holds the
// wrapped instant. radosgw casts the seconds and the fraction to uint64_t
// separately: the seconds cast is undefined in C++ for a count of -1 or less,
// NaN, or 2^64 and above, and the fraction cast for any negative count, NaN,
// or 2^64 and above. The seconds-to-nanoseconds multiply and the sum are
// signed int64 (std::chrono::nanoseconds), which overflows, also undefined,
// for a count from about 9.2e9 seconds, year 2262, up to 2^64, and on x86-64
// for NaN and for a count of -1 or less, whose casts there give the integer
// indefinite. A count between -1 and 0, such as -0.5, overflows
// nothing and reaches only the fraction cast. AsDate computes in uint64,
// which gives what radosgw's x86-64 build gives for all of these; only the
// double-to-integer casts could differ on another architecture
// (docs/ceph-upstream-bugs.md).
func AsDate(s string) (time.Time, bool) {
	ns, ok := asDate(s)
	if !ok {
		return time.Time{}, false
	}
	const second = uint64(time.Second)
	return time.Unix(int64(ns/second), int64(ns%second)).UTC(), true //nolint:gosec // ns/second is below 2^35
}

func asDate(s string) (uint64, bool) {
	f, n, erange := strtod(s)
	switch {
	case n == 0 || erange:
		return 0, false
	case n == len(s):
		return epochNanos(f), true
	}
	return fromISO8601(s)
}

// AsBool is Condition::as_bool (src/rgw/rgw_iam_policy.h:403-420 at v19.2.6,
// :422-439 at v20.2.4): false for an empty string, for "false" in any ASCII
// case, and for a string std::stod converts whole to zero or NaN; true for
// anything else, including a number std::stod refuses as out of range.
func AsBool(s string) bool {
	if s == "" || asciiEqualFold(s, "false") {
		return false
	}
	if f, n, erange := strtod(s); n == len(s) && !erange {
		return f != 0 && !math.IsNaN(f)
	}
	return true
}

func asBool(s string) (v, ok bool) { return AsBool(s), true }

// AsBinary is Condition::as_binary (src/rgw/rgw_iam_policy.h:422-438 at
// v19.2.6, :441-457 at v20.2.4), bufferlist::decode_base64, which is
// ceph_unarmor (src/common/armor.c:99-131 at v19.2.6 and v20.2.4). It reads
// groups of four characters and fails on a short one, so padding is
// required. It takes both alphabets, '+' or '-' for 62 and '/' or '_' for 63,
// skips a newline between groups, reads '=' as zero bits, and stops at the
// first group whose third or fourth character is '=', ignoring the rest of s.
func AsBinary(s string) ([]byte, bool) {
	out := make([]byte, 0, len(s)*3/4)
	for i := 0; i < len(s); {
		if s[i] == '\n' {
			i++
			continue
		}
		if i+4 > len(s) {
			return nil, false
		}
		a, aok := armorBits(s[i])
		b, bok := armorBits(s[i+1])
		c, cok := armorBits(s[i+2])
		d, dok := armorBits(s[i+3])
		if !aok || !bok || !cok || !dok {
			return nil, false
		}
		out = append(out, a<<2|b>>4)
		if s[i+2] == '=' {
			return out, true
		}
		out = append(out, (b&15)<<4|c>>2)
		if s[i+3] == '=' {
			return out, true
		}
		out = append(out, (c&3)<<6|d)
		i += 4
	}
	return out, true
}

// armorBits is armor.c's decode_bits: a character's six bits; ok is false
// for a character outside both alphabets and '='.
func armorBits(c byte) (bits byte, ok bool) {
	switch {
	case 'A' <= c && c <= 'Z':
		return c - 'A', true
	case 'a' <= c && c <= 'z':
		return c - 'a' + 26, true
	case '0' <= c && c <= '9':
		return c - '0' + 52, true
	case c == '+' || c == '-':
		return 62, true
	case c == '/' || c == '_':
		return 63, true
	case c == '=':
		return 0, true
	}
	return 0, false
}

// MaskedIP is rgw::IAM::MaskedIP (src/rgw/rgw_iam_policy.h:328-337 at
// v19.2.6, :347-356 at v20.2.4): a 128-bit address and a prefix length.
// Addr holds the address big-endian; an IPv4 address sits in the low 32 bits,
// Addr[12:].
type MaskedIP struct {
	V6     bool
	Addr   [16]byte
	Prefix uint
}

func (m MaskedIP) width() uint {
	if m.V6 {
		return 128
	}
	return 32
}

// ParseMaskedIP is Condition::as_network (src/rgw/rgw_iam_policy.cc:1008-1068
// at v19.2.6, :1012-1072 at v20.2.4). s is IPv6 when it holds a colon
// anywhere. The prefix after the first '/' is strtoul's, so it may be signed,
// follow whitespace, or be empty for 0, and it is truncated to 32 bits before
// it is checked against 32 or 128. The address is glibc's inet_pton, which
// takes neither a zone nor an octet with a leading zero, and reads up to the
// first NUL.
func ParseMaskedIP(s string) (MaskedIP, bool) {
	if s == "" {
		return MaskedIP{}, false
	}
	m := MaskedIP{V6: strings.Contains(s, ":")}
	addr, prefix, slash := strings.Cut(s, "/")
	if !slash {
		m.Prefix = m.width()
	} else {
		v, n, _ := rgwtext.Strtoull(prefix)
		if n < len(prefix) && prefix[n] != 0 {
			return MaskedIP{}, false
		}
		m.Prefix = uint(uint32(v)) //nolint:gosec // as_network stores strtoul's unsigned long in an unsigned int
		if m.Prefix > m.width() {
			return MaskedIP{}, false
		}
	}
	addr = rgwtext.CString(addr)
	var ok bool
	if m.V6 {
		m.Addr, ok = inetPton6(addr)
	} else {
		var a [4]byte
		a, ok = inetPton4(addr)
		copy(m.Addr[12:], a[:])
	}
	if !ok {
		return MaskedIP{}, false
	}
	return m, true
}

// String is MaskedIP's operator<< (src/rgw/rgw_iam_policy.cc:811-840 at
// v19.2.6, :830-859 at v20.2.4): an IPv6 address as eight hextets in
// lower-case hex without leading zeros or "::", an IPv4 address as a dotted
// quad, then "/" and the prefix.
func (m MaskedIP) String() string {
	var b strings.Builder
	if m.V6 {
		for i := 0; i < 16; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(strconv.FormatUint(uint64(binary.BigEndian.Uint16(m.Addr[i:])), 16))
		}
	} else {
		for i := 12; i < 16; i++ {
			if i > 12 {
				b.WriteByte('.')
			}
			b.WriteString(strconv.FormatUint(uint64(m.Addr[i]), 10))
		}
	}
	b.WriteByte('/')
	b.WriteString(strconv.FormatUint(uint64(m.Prefix), 10))
	return b.String()
}

// Equal is MaskedIP's operator== (src/rgw/rgw_iam_policy.h:341-346 at
// v19.2.6, :360-365 at v20.2.4): both addresses shifted right by the larger
// of the two host-part widths, each 32 or 128 less its prefix, then compared
// whole. An IPv4 and an IPv6 address can be equal: "::1" equals "0.0.0.1".
// radosgw asserts the shift is not negative, as ParseMaskedIP's prefixes
// ensure; Equal takes a wider prefix as the full width.
func (m MaskedIP) Equal(o MaskedIP) bool {
	shift := max(m.width()-min(m.Prefix, m.width()), o.width()-min(o.Prefix, o.width()))
	mh, ml := shiftRight(m.Addr, shift)
	oh, ol := shiftRight(o.Addr, shift)
	return mh == oh && ml == ol
}

func shiftRight(a [16]byte, n uint) (hi, lo uint64) {
	hi, lo = binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(a[8:])
	if n >= 64 {
		return 0, hi >> (n - 64)
	}
	return hi >> n, lo>>n | hi<<(64-n)
}

// inetPton4 is glibc's inet_pton4 (resolv/inet_pton.c, 2.34): four decimal
// octets of at most 255, no leading zeros.
func inetPton4(s string) (a [4]byte, ok bool) {
	var tmp [4]byte
	tp, octets, sawDigit := 0, 0, false
	for i := range len(s) {
		ch := s[i]
		switch {
		case isDigit(ch):
			v := uint(tmp[tp])*10 + uint(ch-'0')
			if sawDigit && tmp[tp] == 0 {
				return a, false
			}
			if v > 255 {
				return a, false
			}
			tmp[tp] = byte(v) //nolint:gosec // at most 255
			if !sawDigit {
				octets++
				if octets > 4 {
					return a, false
				}
				sawDigit = true
			}
		case ch == '.' && sawDigit:
			if octets == 4 {
				return a, false
			}
			tp++
			sawDigit = false
		default:
			return a, false
		}
	}
	if octets < 4 {
		return a, false
	}
	return tmp, true
}

// inetPton6 is glibc's inet_pton6 (resolv/inet_pton.c, 2.34): up to eight
// groups of at most four hex digits, one "::" standing for at least one zero
// group, and an IPv4 address in the last 32 bits; no zone.
func inetPton6(s string) (a [16]byte, ok bool) {
	var tmp [16]byte
	tp, colonp := 0, -1
	if s == "" {
		return a, false
	}
	i := 0
	if s[0] == ':' {
		i++
		if i == len(s) || s[i] != ':' {
			return a, false
		}
	}
	curtok, xdigits := i, 0
	var val uint16
	for i < len(s) {
		ch := s[i]
		i++
		if d, ok := hexValue(ch); ok {
			if xdigits == 4 {
				return a, false
			}
			val = val<<4 | uint16(d)
			xdigits++
			continue
		}
		if ch == ':' {
			curtok = i
			if xdigits == 0 {
				if colonp >= 0 {
					return a, false
				}
				colonp = tp
				continue
			}
			if i == len(s) || tp+2 > len(tmp) {
				return a, false
			}
			binary.BigEndian.PutUint16(tmp[tp:], val)
			tp += 2
			xdigits, val = 0, 0
			continue
		}
		if ch == '.' && tp+4 <= len(tmp) {
			if v4, ok := inetPton4(s[curtok:]); ok {
				copy(tmp[tp:], v4[:])
				tp += 4
				xdigits = 0
				break
			}
		}
		return a, false
	}
	if xdigits > 0 {
		if tp+2 > len(tmp) {
			return a, false
		}
		binary.BigEndian.PutUint16(tmp[tp:], val)
		tp += 2
	}
	if colonp >= 0 {
		if tp == len(tmp) {
			return a, false
		}
		n := tp - colonp
		copy(tmp[len(tmp)-n:], tmp[colonp:tp])
		clear(tmp[colonp : len(tmp)-n])
		tp = len(tmp)
	}
	if tp != len(tmp) {
		return a, false
	}
	return tmp, true
}

// strtod is glibc's strtod(3) in the C locale, as std::stod calls it: f is
// the value, n the bytes consumed (0 when nothing converts), and erange the
// ERANGE that makes std::stod throw.
func strtod(s string) (f float64, n int, erange bool) {
	i := 0
	for i < len(s) && isCSpace(s[i]) {
		i++
	}
	start := i
	sign := 1.0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		if s[i] == '-' {
			sign = -1
		}
		i++
	}
	switch rest := s[i:]; {
	case hasPrefixFold(rest, "infinity"):
		return math.Inf(int(sign)), i + len("infinity"), false
	case hasPrefixFold(rest, "inf"):
		return math.Inf(int(sign)), i + len("inf"), false
	case hasPrefixFold(rest, "nan"):
		return math.NaN(), i + len("nan") + nanPayload(rest[len("nan"):]), false
	}

	hex := false
	if j := i + 2; j < len(s) && s[i] == '0' && s[i+1]|0x20 == 'x' &&
		(isHexDigit(s[j]) || j+1 < len(s) && s[j] == '.' && isHexDigit(s[j+1])) {
		hex, i = true, j
	}
	digit := isDigit
	if hex {
		digit = isHexDigit
	}
	intStart := i
	for i < len(s) && digit(s[i]) {
		i++
	}
	intDigits, fracDigits := s[intStart:i], ""
	if i < len(s) && s[i] == '.' {
		i++
		fracStart := i
		for i < len(s) && digit(s[i]) {
			i++
		}
		fracDigits = s[fracStart:i]
	}
	if intDigits == "" && fracDigits == "" {
		return 0, 0, false
	}

	expChar, exp := byte('e'), ""
	if hex {
		expChar = 'p'
	}
	if i < len(s) && s[i]|0x20 == expChar {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			exp, i = s[i+1:j], j
		}
	}

	text := s[start:i]
	if hex && exp == "" {
		text += "p0"
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return f, i, true
	}
	if math.Abs(f) > 0x1p-1022 {
		return f, i, false
	}
	return f, i, underflows(intDigits+fracDigits, len(fracDigits), exp, hex, f)
}

// nanPayload is the length of the "(n-char-sequence)" strtod takes after
// "nan", or 0 when s does not start with a closed one.
func nanPayload(s string) int {
	if s == "" || s[0] != '(' {
		return 0
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ')':
			return i + 1
		case isDigit(c) || 'a' <= c|0x20 && c|0x20 <= 'z' || c == '_':
		default:
			return 0
		}
	}
	return 0
}

// underflows is glibc's ERANGE for a result at or below the least normal
// double: round_and_return sets it when the result is inexact and the value,
// rounded to 53 bits with an unbounded exponent, is below 2^-1022, the
// tininess-after-rounding x86 uses (stdlib/strtod_l.c, 2.34). digits are the
// mantissa's digits without its point, fracLen of them after it, and exp the
// exponent's decimal text, a power of 10 for a decimal mantissa and of 2 for
// a hex one.
func underflows(digits string, fracLen int, exp string, hex bool, f float64) bool {
	sig := strings.TrimLeft(digits, "0")
	if sig == "" {
		return false
	}
	if f == 0 {
		return true
	}
	base, perDigit, radix := int64(10), int64(1), 10
	if hex {
		base, perDigit, radix = 2, 4, 16
	}
	e := saturatingAtoi(exp) - perDigit*int64(fracLen)
	// Past 800 significant digits only whether one is non-zero matters: a
	// subnormal double's decimal expansion has at most 767, and the rounding
	// threshold below 2^-1022 has 769, so a value with more is inexact and
	// lies on the side of that threshold its first 800 digits do.
	const keep = 800
	sticky := false
	if len(sig) > keep {
		sticky = strings.Trim(sig[keep:], "0") != ""
		e += perDigit * int64(len(sig)-keep)
		sig = sig[:keep]
	}
	trimmed := strings.TrimRight(sig, "0")
	e += perDigit * int64(len(sig)-len(trimmed))
	num, _ := new(big.Int).SetString(trimmed, radix)
	pow := new(big.Int).Exp(big.NewInt(base), big.NewInt(max(e, -e)), nil)
	v := new(big.Rat)
	if e >= 0 {
		v.SetInt(num.Mul(num, pow))
	} else {
		v.SetFrac(num, pow)
	}
	tiny := new(big.Float).SetPrec(53).SetRat(v).Cmp(big.NewFloat(0x1p-1022)) < 0
	inexact := sticky || v.Cmp(new(big.Rat).SetFloat64(math.Abs(f))) != 0
	return tiny && inexact
}

// saturatingAtoi parses an optionally signed run of decimal digits, holding
// still short of overflow: an exponent that large has already made the value
// zero or infinite.
func saturatingAtoi(s string) int64 {
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg, s = s[0] == '-', s[1:]
	}
	var v int64
	for i := range len(s) {
		if v < 1<<40 {
			v = v*10 + int64(s[i]-'0')
		}
	}
	if neg {
		return -v
	}
	return v
}

// epochNanos is as_date's conversion of a count of seconds to real_time,
// seconds(uint64_t(d)) + nanoseconds(uint64_t((d - uint64_t(d)) * 1e9)). The
// seconds-to-nanoseconds multiply and the add are signed int64 in radosgw
// (std::chrono::nanoseconds); where they overflow, as AsDate lists, GCC's
// imul and add wrap exactly as this uint64 arithmetic does.
func epochNanos(d float64) uint64 {
	sec := toUint64(d)
	frac := toUint64((d - float64(sec)) * 1e9)
	return sec*uint64(time.Second) + frac
}

// toUint64 is a C cast of a double to uint64_t as GCC compiles it for x86-64
// (11.5, 13.5 and 15.3 alike): cvttsd2si for a value below 2^63, and for one
// at or above it cvttsd2si of the value less 2^63 with the top bit flipped.
// The cast is undefined in C++ for a negative, NaN or too large value; this
// is what that code does with one.
func toUint64(d float64) uint64 {
	if d >= 0x1p63 {
		return cvttsd2si(d-0x1p63) ^ 1<<63
	}
	return cvttsd2si(d)
}

// cvttsd2si truncates d to an int64, or answers 0x8000000000000000, the
// integer indefinite, for NaN or a value outside int64's range.
func cvttsd2si(d float64) uint64 {
	if d >= -0x1p63 && d < 0x1p63 {
		return uint64(int64(d)) //nolint:gosec // the instruction's two's-complement result
	}
	return 1 << 63
}

// fromISO8601 is ceph::from_iso_8601 with ws_terminates false
// (src/common/iso_8601.cc:51-151 at v19.2.6 and v20.2.4), in real_time's
// nanoseconds.
func fromISO8601(s string) (uint64, bool) {
	p := 0
	digits := func(n int) (int, bool) {
		v := 0
		for range n {
			if p == len(s) || !isDigit(s[p]) {
				return 0, false
			}
			v = v*10 + int(s[p]-'0')
			p++
		}
		return v, true
	}
	dateEnd := func() bool { return p == len(s) }
	timeEnd := func() bool { return p == len(s)-1 && s[p] == 'Z' }

	year, ok := digits(4)
	if !ok || year < 1970 {
		return 0, false
	}
	month, day, hour, minute, sec := 1, 1, 0, 0, 0
	nanos := func(frac int) (uint64, bool) {
		t := strptime.Tm{Year: year, Mon: month - 1, Mday: day, Hour: hour, Min: minute, Sec: sec}.Timegm().Unix()
		if t == -1 {
			return 0, false
		}
		return uint64(t)*uint64(time.Second) + uint64(frac), true //nolint:gosec // from_time_t converts time_t to real_time's unsigned nanoseconds
	}
	if dateEnd() {
		return nanos(0)
	}
	for _, field := range []struct {
		delim byte
		v     *int
		end   func() bool
	}{
		{'-', &month, dateEnd},
		{'-', &day, dateEnd},
		{'T', &hour, timeEnd},
		{':', &minute, timeEnd},
		{':', &sec, timeEnd},
	} {
		if p == len(s) || s[p] != field.delim {
			return 0, false
		}
		p++
		if *field.v, ok = digits(2); !ok {
			return 0, false
		}
		if field.end() {
			return nanos(0)
		}
	}
	if p == len(s) || s[p] != '.' {
		return 0, false
	}
	p++
	frac, scale := 0, 100_000_000
	for range 9 {
		d, ok := digits(1)
		if !ok {
			return 0, false
		}
		frac += d * scale
		scale /= 10
		if timeEnd() {
			return nanos(frac)
		}
	}
	return 0, false
}
