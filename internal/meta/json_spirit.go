package meta

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// This file transcribes the parts of json_spirit (src/json_spirit, v4.05 as
// vendored by ceph) that JSONParser::parse and JSONObj::init use, so that
// JSONFormattable::set stores what radosgw stores when RGWZoneParams::decode
// converts a pre-v12 tier config. Line references are to ceph main, whose
// json_spirit matches v19.2.6 and v20.2.x apart from a guard in
// substitute_esc_chars that changes nothing here, and to boost 1.87 classic
// spirit as built into the v19 packages.
//
// The grammar (json_spirit_reader_template.h:468-524) is lenient: whitespace
// is C isspace, arrays and objects accept stray commas, parsing stops after
// the first value with any trailing text left unread, and numbers are tried
// as strict_real_p, then int64_p, then uint64_p. A throw anywhere, for
// example an object without its closing brace, fails the whole parse.
//
// The few forms whose C++ result depends on behavior not transcribed here
// fail with denc.ErrMalformed rather than store a guess: \x and octal string
// escapes, digit runs over 300 digits, exponents beyond ±300 and non-finite
// results.

// spiritNode is a json_spirit::Value as JSONObj sees it: a string, a scalar
// already rendered by write_string, an array or an object with its members
// in document order.
type spiritNode struct {
	kind   byte // 's' string, 'v' other scalar, 'a' array, 'o' object
	text   string
	arr    []spiritNode
	keys   []string
	values []spiritNode
}

type spiritParser struct {
	s   string
	pos int
	// threw stands for the exceptions the grammar throws, which read_range
	// catches to report failure.
	threw bool
	// err is a form this transcription does not reproduce.
	err error
}

// stopped reports whether parsing has ended early.
func (p *spiritParser) stopped() bool { return p.threw || p.err != nil }

// parseJSON mirrors JSONParser::parse (src/common/ceph_json.cc): the node
// and true when json_spirit reads val, and a denc.ErrMalformed error for a
// form not transcribed. A top-level scalar other than a string counts only
// when write_string of it is as long as val.
func parseJSON(val string) (spiritNode, bool, error) {
	p := spiritParser{s: val}
	n, ok := p.value()
	if p.err != nil {
		return spiritNode{}, false, p.err
	}
	if !ok || p.threw || (n.kind == 'v' && len(n.text) != len(val)) {
		return spiritNode{}, false, nil
	}
	return n, true, nil
}

func (p *spiritParser) malformed(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf("%w: legacy tier config value %q: %s", denc.ErrMalformed, p.s, fmt.Sprintf(format, args...))
	}
}

// fail records a throw.
func (p *spiritParser) fail() { p.threw = true }

// skipWS is the space_p skipper: C isspace in the "C" locale.
func (p *spiritParser) skipWS() {
	for p.pos < len(p.s) && strings.IndexByte(" \t\n\v\f\r", p.s[p.pos]) >= 0 {
		p.pos++
	}
}

// value is value_: string | number | object | array | true | false | null.
// It reports false for no match, which leaves pos where it was.
func (p *spiritParser) value() (spiritNode, bool) {
	p.skipWS()
	if p.stopped() || p.pos >= len(p.s) {
		return spiritNode{}, false
	}
	switch p.s[p.pos] {
	case '"':
		if str, ok := p.str(); ok {
			return spiritNode{kind: 's', text: str}, true
		}
		return spiritNode{}, false
	case '{':
		return p.object(), !p.stopped()
	case '[':
		return p.array(), !p.stopped()
	}
	if text, ok := p.number(); ok || p.err != nil {
		return spiritNode{kind: 'v', text: text}, ok
	}
	for _, lit := range []string{"true", "false", "null"} {
		if strings.HasPrefix(p.s[p.pos:], lit) {
			p.pos += len(lit)
			return spiritNode{kind: 'v', text: lit}, true
		}
	}
	return spiritNode{}, false
}

// object is object_: '{' !members_ ('}' | throw), members_ being
// pair_ >> *(',' >> pair_ | ','), and pair_ a string, then ':' or throw,
// then a value or throw.
func (p *spiritParser) object() spiritNode {
	p.pos++ // '{'
	n := spiritNode{kind: 'o'}
	if p.pair(&n) {
		for !p.stopped() {
			save := p.pos
			p.skipWS()
			if p.pos >= len(p.s) || p.s[p.pos] != ',' {
				p.pos = save
				break
			}
			p.pos++
			p.pair(&n)
		}
	}
	p.closing('}')
	return n
}

// pair reads one member into n, reporting whether a key string matched.
func (p *spiritParser) pair(n *spiritNode) bool {
	save := p.pos
	p.skipWS()
	if p.pos >= len(p.s) || p.s[p.pos] != '"' {
		p.pos = save
		return false
	}
	key, ok := p.str()
	if !ok {
		p.pos = save
		return false
	}
	p.skipWS()
	if p.pos >= len(p.s) || p.s[p.pos] != ':' {
		p.fail()
		return true
	}
	p.pos++
	v, ok := p.value()
	if !ok {
		p.fail()
		return true
	}
	n.keys = append(n.keys, key)
	n.values = append(n.values, v)
	return true
}

// array is array_: '[' !elements_ (']' | throw), elements_ being
// value_ >> *(',' >> value_ | ',').
func (p *spiritParser) array() spiritNode {
	p.pos++ // '['
	n := spiritNode{kind: 'a'}
	if v, ok := p.value(); ok {
		n.arr = append(n.arr, v)
		for !p.stopped() {
			save := p.pos
			p.skipWS()
			if p.pos >= len(p.s) || p.s[p.pos] != ',' {
				p.pos = save
				break
			}
			p.pos++
			if v, ok := p.value(); ok {
				n.arr = append(n.arr, v)
			}
		}
	}
	p.closing(']')
	return n
}

func (p *spiritParser) closing(c byte) {
	if p.stopped() {
		return
	}
	p.skipWS()
	if p.pos >= len(p.s) || p.s[p.pos] != c {
		p.fail()
		return
	}
	p.pos++
}

// str is string_: confix_p('"', *lex_escape_ch_p, '"'), where a backslash
// takes the next character with it, then get_str's unescaping. The string
// keeps its bytes as they are, UTF-8 or not, as std::string does.
func (p *spiritParser) str() (string, bool) {
	i := p.pos + 1
	for {
		if i >= len(p.s) {
			return "", false
		}
		switch p.s[i] {
		case '"':
			content := p.s[p.pos+1 : i]
			p.pos = i + 1
			return substituteEscChars(content), true
		case '\\':
			if i+1 >= len(p.s) {
				return "", false
			}
			if e := p.s[i+1]; e == 'x' || e == 'X' || (e >= '0' && e <= '7') {
				// escape_char_parse reads a hex or octal number here, with an
				// overflow check on a signed char, and get_str reads \x as two
				// hex digits: not transcribed.
				p.malformed("\\%c escape", e)
				return "", false
			}
			i += 2
		default:
			i++
		}
	}
}

// substituteEscChars is substitute_esc_chars
// (json_spirit_reader_template.h:133-172) with append_esc_char_and_incr_iter
// (:95-131): the JSON escapes, \u with four hex digits, a non-hex digit
// counting as 0, encoded by encode_utf8 even for surrogates, and any other
// escaped character dropped with its backslash. A backslash in the last
// position is kept.
func substituteEscChars(s string) string {
	if len(s) < 2 || !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	start := 0
	for i := 0; i < len(s)-1; i++ {
		if s[i] != '\\' {
			continue
		}
		b.WriteString(s[start:i])
		i++
		switch c := s[i]; c {
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '\\', '/', '"':
			b.WriteByte(c)
		case 'u':
			if len(s)-i >= 5 {
				var uc uint16
				for _, h := range []byte(s[i+1 : i+5]) {
					uc = uc<<4 | uint16(hexToNum(h))
				}
				i += 4
				b.Write(encodeUTF8(uc))
			}
		}
		start = i + 1
	}
	b.WriteString(s[start:])
	return b.String()
}

// hexToNum is json_spirit's hex_to_num: a non-hex character is 0.
func hexToNum(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// encodeUTF8 is encode_utf8 (src/common/utf8.c) for the code points a \u
// escape can name, surrogates included.
func encodeUTF8(u uint16) []byte {
	switch {
	case u <= 0x7F:
		return []byte{byte(u)}
	case u <= 0x7FF:
		return []byte{0xC0 | byte(u>>6), 0x80 | byte(u&0x3F)}
	default:
		return []byte{0xE0 | byte(u>>12), 0x80 | byte(u>>6&0x3F), 0x80 | byte(u&0x3F)} //nolint:gosec // u is at most 0xFFFF, so u>>12 fits a byte
	}
}

// maxDigits bounds the digit runs and exponents transcribed: below it no
// accumulation overflows and every power of ten used is a normal double.
const maxDigits = 300

// number is number_: strict_real_p | int64_p | uint64_p, rendered as
// Generator::output writes the value.
func (p *spiritParser) number() (string, bool) {
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

// formatReal is Generator::output(double) without remove_trailing_zeros:
// append_double(os, d, 17) (json_spirit_writer_template.h:105-109, :252),
// std::showpoint with setprecision(17), which is printf's %#.17g.
func formatReal(d float64) string { return fmt.Sprintf("%#.17g", d) }

// digitRun returns the length of the run of decimal digits at i.
func (p *spiritParser) digitRun(i int) int {
	j := i
	for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
		j++
	}
	return j - i
}

// accumulate is extract_int with positive_accumulate<double, 10>
// (boost/spirit/home/classic/core/primitives/impl/numerics.ipp): each digit
// multiplies by ten and adds, rounding at each step.
func accumulate(digits string) float64 {
	var n float64
	for i := range len(digits) {
		n = float64(n * 10)
		n += float64(digits[i] - '0')
	}
	return n
}

// strictReal is real_parser_impl::parse_main (numerics.ipp) with
// strict_real_parser_policies<double>: an optional sign, digits, and a dot
// or an exponent, at least one of which must be present. The value is built
// as the parser builds it, not correctly rounded: the integer and fraction
// digits accumulated as doubles, the fraction scaled by pow(10, -digits) and
// the whole by pow(10, exponent).
func (p *spiritParser) strictReal() (float64, bool) {
	i := p.pos
	neg := false
	if i < len(p.s) && (p.s[i] == '+' || p.s[i] == '-') {
		neg = p.s[i] == '-'
		i++
	}
	intDigits := p.digitRun(i)
	if intDigits > maxDigits {
		p.malformed("a run of %d digits", intDigits)
		return 0, false
	}
	n := accumulate(p.s[i : i+intDigits])
	got := intDigits > 0
	i += intDigits
	if neg {
		n = -n
	}
	eHit := false
	if i < len(p.s) && p.s[i] == '.' {
		i++
		fracDigits := p.digitRun(i)
		if fracDigits > maxDigits {
			p.malformed("a run of %d digits", fracDigits)
			return 0, false
		}
		switch {
		case fracDigits > 0:
			f := float64(accumulate(p.s[i:i+fracDigits]) * glibcPow10(-fracDigits))
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
		// Beyond ±300 the C++ stores inf, 0 or nan for most mantissas.
		digits = strings.TrimLeft(digits, "0")
		if len(digits) > 3 || atoi(digits) > maxDigits {
			p.malformed("exponent of magnitude %s", digits)
			return 0, false
		}
		exp := atoi(digits)
		if neg {
			exp = -exp
		}
		if exp == 126 {
			p.malformed("exponent 126, whose glibc pow depends on the host CPU")
			return 0, false
		}
		n *= glibcPow10(exp)
		i = next
	}
	if math.IsInf(n, 0) || math.IsNaN(n) {
		p.malformed("a non-finite number")
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
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return neg, s[start:i], i, i > start
}

// int64 is int64_p: int_parser<int64_t>, an optional sign and digits,
// failing on overflow.
func (p *spiritParser) int64() (int64, bool) {
	i := p.pos
	neg := false
	if i < len(p.s) && (p.s[i] == '+' || p.s[i] == '-') {
		neg = p.s[i] == '-'
		i++
	}
	n := p.digitRun(i)
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
func (p *spiritParser) uint64() (uint64, bool) {
	n := p.digitRun(p.pos)
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
// |k| <= maxDigits: the correctly rounded power except at k = 23 and
// k = 210, where glibc returns the next double up (checked with math.pow in
// the v19.2.6 and v20.2.4 images against float("1e<k>")). The table assumes
// glibc on an FMA-capable host: glibc picks its pow by CPU, and without FMA
// k = 126 also differs, so strictReal rejects that exponent.
func glibcPow10(k int) float64 {
	v, err := strconv.ParseFloat("1e"+strconv.Itoa(k), 64)
	if err != nil {
		panic(fmt.Sprintf("meta: 1e%d: %v", k, err))
	}
	if k == 23 || k == 210 {
		return math.Nextafter(v, math.Inf(1))
	}
	return v
}
