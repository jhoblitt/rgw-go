// Package jsonspirit reads JSON as ceph's JSONParser reads it: with the
// grammar of json_spirit 4.05 as ceph vendors it (src/json_spirit), numbers
// built as boost's classic spirit builds them, and the tree as JSONObj files
// it, each node with its get_data text and its children in JSONObj's order.
// json_spirit and src/common/ceph_json.cc are the same at v19.2.7 and
// v20.2.4.
//
// The grammar
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L462-L519 at v19.2.7
// and v20.2.4) is lenient: white space is C isspace, arrays and objects take
// stray commas, a string any byte, a number a sign, leading zeros and a dot
// at either end, and reading stops after the first value with whatever
// follows it unread. A throw anywhere, an object without its closing brace
// for one, fails the whole read.
//
// Parse refuses with an error what it does not read as json_spirit does:
// \x and \X escapes, a real whose value json_spirit takes from the host's
// libm, and nesting deeper than MaxDepth.
package jsonspirit

import (
	"fmt"
	"strings"
)

// MaxDepth is the deepest nesting of arrays and objects Parse reads.
// json_spirit sets no limit and recurses once per level, as Parse does, so
// without one a document of brackets would grow a goroutine's stack toward
// the runtime's limit, past which it ends the process.
const MaxDepth = 10000

// Parse is JSONParser::parse(buf, len(buf))
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L236-L265 at v19.2.7
// and v20.2.4): json_spirit's read of buf's first value
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L572-L624 at v19.2.7
// and v20.2.4), then, for a lone number, true, false or null, the check that
// json_spirit's text for it is as long as buf. ok reports whether JSONParser
// takes buf, and root is then its value. A caller passes buf as it passes
// JSONParser the buffer and length, a NUL it appends included.
//
// A non-nil err is a form Parse refuses rather than read otherwise than
// json_spirit does, its text saying which: a \x or \X escape, which
// json_spirit reads or refuses by the CPU's char signedness; a real whose
// value comes from the host's libm, one with an exponent past ±300, a
// fraction of more than 300 digits or the exponent 126, or whose value is
// not finite; and nesting deeper than MaxDepth.
func Parse(buf string) (root *Node, ok bool, err error) {
	p := parser{s: buf}
	n, ok := p.value()
	if p.err != nil {
		return nil, false, p.err
	}
	if !ok || p.threw || n.kind == Scalar && len(n.text) != len(buf) {
		return nil, false, nil
	}
	return n, true, nil
}

type parser struct {
	s     string
	pos   int
	depth int
	// threw stands for the exceptions the grammar throws, which read_range
	// catches to report failure.
	threw bool
	// err is a form Parse refuses.
	err error
}

// stopped reports whether reading has ended early.
func (p *parser) stopped() bool { return p.threw || p.err != nil }

// refuse records the first form Parse refuses.
func (p *parser) refuse(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// cSpace is what isspace takes in the C locale, the set space_p skips.
const cSpace = " \t\n\v\f\r"

// skipSpace is the space_p skipper.
func (p *parser) skipSpace() {
	for p.pos < len(p.s) && strings.IndexByte(cSpace, p.s[p.pos]) >= 0 {
		p.pos++
	}
}

// value is value_: string_ | number_ | object_ | array_ | "true" | "false"
// | "null". It reports false for no match, which leaves pos where it was but
// for white space skipped.
func (p *parser) value() (*Node, bool) {
	p.skipSpace()
	if p.stopped() || p.pos >= len(p.s) {
		return nil, false
	}
	switch p.s[p.pos] {
	case '"':
		if s, ok := p.str(); ok {
			return &Node{kind: String, text: s}, true
		}
		return nil, false
	case '{', '[':
		if p.depth == MaxDepth {
			p.refuse("arrays and objects nested deeper than %d", MaxDepth)
			return nil, false
		}
		p.depth++
		var n *Node
		if p.s[p.pos] == '{' {
			n = p.object()
		} else {
			n = p.array()
		}
		p.depth--
		return n, !p.stopped()
	}
	if text, ok := p.number(); ok || p.err != nil {
		return &Node{kind: Scalar, text: text}, ok
	}
	for _, lit := range [...]string{"true", "false", "null"} {
		if strings.HasPrefix(p.s[p.pos:], lit) {
			p.pos += len(lit)
			return &Node{kind: Scalar, text: lit}, true
		}
	}
	return nil, false
}

// object is object_: '{' !members_ ('}' | throw), members_ being
// pair_ >> *(',' >> pair_ | ','), and pair_ a string, then ':' or throw,
// then a value or throw.
func (p *parser) object() *Node {
	p.pos++ // '{'
	n := &Node{kind: Object}
	if p.pair(n) {
		for !p.stopped() && p.comma() {
			p.pair(n)
		}
	}
	p.closing('}')
	return n
}

// pair reads one member into n, reporting whether its name matched.
func (p *parser) pair(n *Node) bool {
	save := p.pos
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != '"' {
		p.pos = save
		return false
	}
	name, ok := p.str()
	if !ok {
		p.pos = save
		return false
	}
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != ':' {
		p.threw = true
		return true
	}
	p.pos++
	v, ok := p.value()
	if !ok {
		p.threw = true
		return true
	}
	n.names = append(n.names, name)
	n.elems = append(n.elems, v)
	return true
}

// array is array_: '[' !elements_ (']' | throw), elements_ being
// value_ >> *(',' >> value_ | ',').
func (p *parser) array() *Node {
	p.pos++ // '['
	n := &Node{kind: Array}
	if v, ok := p.value(); ok {
		n.elems = append(n.elems, v)
		for !p.stopped() && p.comma() {
			if v, ok := p.value(); ok {
				n.elems = append(n.elems, v)
			}
		}
	}
	p.closing(']')
	return n
}

// comma consumes a ',' after white space, or leaves pos where it was.
func (p *parser) comma() bool {
	save := p.pos
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == ',' {
		p.pos++
		return true
	}
	p.pos = save
	return false
}

// closing is a compound's closing c, or the throw in its place.
func (p *parser) closing(c byte) {
	if p.stopped() {
		return
	}
	p.skipSpace()
	if p.pos >= len(p.s) || p.s[p.pos] != c {
		p.threw = true
		return
	}
	p.pos++
}

// str is string_: confix_p('"', *lex_escape_ch_p, '"'), where a backslash
// takes the next byte with it, then get_str's unescaping. The string keeps
// its bytes as they are, UTF-8 or not, as std::string does. An octal escape
// is no exception: escape_char_parse takes as many octal digits as a char
// holds, or, past that, the backslash and the first digit alone, and either
// way get_str drops the backslash and the first digit, as it drops any escape
// it does not know.
func (p *parser) str() (string, bool) {
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
			if e := p.s[i+1]; e == 'x' || e == 'X' {
				// escape_char_parse lexes the hex digits after either with an
				// overflow check on a char, signed on x86-64 and unsigned on
				// aarch64, so whether the string lexes depends on the CPU
				// (docs/ceph-upstream-bugs.md).
				p.refuse(`\%c escape`, e)
				return "", false
			}
			i += 2
		default:
			i++
		}
	}
}

// substituteEscChars is substitute_esc_chars
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L133-L167 at v19.2.7
// and v20.2.4) with append_esc_char_and_incr_iter
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L95-L131 at v19.2.7
// and v20.2.4): the JSON escapes, \u with four hex digits, a non-hex digit
// counting as 0, encoded by encode_utf8 even for a surrogate, and any other
// escaped byte dropped with its backslash. A backslash in the last position
// is kept.
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

// hexToNum is hex_to_num
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L51-L58 at v19.2.7
// and v20.2.4): a byte that is not a hex digit is 0.
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
