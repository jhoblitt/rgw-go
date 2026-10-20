package postpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDepth is the deepest nesting of arrays and objects a policy may have,
// the limit Go's decoder sets. json_spirit sets none, recursing at each
// level. readValue checks it as well, since Decoder.Token enforces it only
// when encoding/json is built on encoding/json/v2, as Go 1.27 builds it by
// default.
const maxDepth = 10000

type kind uint8

const (
	kindString kind = iota
	kindScalar      // a number, true, false or null
	kindArray
	kindObject
)

// node is a JSON value as JSONObj holds it.
type node struct {
	kind kind
	// text is a string's contents, or a scalar's text as json_spirit
	// writes it.
	text string
	// names are an object's member names, and elems its values or an
	// array's elements, in document order with duplicate names kept, as
	// json_spirit's vector-backed Object keeps them.
	names []string
	elems []*node
}

// readDocument reads the first JSON value of doc, leaving whatever follows
// it unread as json_spirit's read does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L572-L589 at v19.2.7
// and v20.2.4). Go's decoder reads RFC 8259 JSON, where json_spirit's
// grammar
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L462-L519 at v19.2.7
// and v20.2.4) also takes stray commas, C white space, raw control bytes and
// any byte in a string, \x, octal and unknown escapes, \u escapes whose
// digits are not hex, and numbers with a plus sign, a leading zero, or a
// dot at either end. The value read must also be UTF-8,
// which Go's decoder would otherwise repair, and hold no lone UTF-16
// surrogate escape, which json_spirit writes as three bytes of no
// character and Go's decoder as U+FFFD.
func readDocument(doc []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	n, err := readValue(dec, 0)
	if err != nil {
		return nil, err
	}
	read := doc[:dec.InputOffset()]
	if !utf8.Valid(read) {
		return nil, errors.New("a string that is not UTF-8")
	}
	if loneSurrogate(read) {
		return nil, errors.New("a lone UTF-16 surrogate escape")
	}
	return n, nil
}

// readValue reads one value from dec inside depth arrays and objects.
func readValue(dec *json.Decoder, depth int) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("reading the policy: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		if depth == maxDepth {
			return nil, fmt.Errorf("arrays and objects nested deeper than %d", maxDepth)
		}
		n := &node{kind: kindArray}
		if t == '{' {
			n.kind = kindObject
		}
		for dec.More() {
			if n.kind == kindObject {
				name, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("reading the policy: %w", err)
				}
				s, ok := name.(string)
				if !ok {
					return nil, fmt.Errorf("a member named by %v", name)
				}
				n.names = append(n.names, s)
			}
			e, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			n.elems = append(n.elems, e)
		}
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("reading the policy: %w", err)
		}
		return n, nil
	case string:
		return &node{kind: kindString, text: t}, nil
	case json.Number:
		text, err := numberText(string(t))
		if err != nil {
			return nil, err
		}
		return &node{kind: kindScalar, text: text}, nil
	case bool:
		return &node{kind: kindScalar, text: strconv.FormatBool(t)}, nil
	case nil:
		return &node{kind: kindScalar, text: "null"}, nil
	}
	return nil, fmt.Errorf("the token %v", tok)
}

// loneSurrogate reports whether b, JSON Go's decoder has read, holds a
// \u escape of a UTF-16 surrogate that is not half of an escaped pair. Every
// backslash of such JSON is in a string and starts an escape.
func loneSurrogate(b []byte) bool {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		r, ok := escapedUnit(b, i)
		if !ok {
			continue
		}
		i += 4
		switch {
		case 0xdc00 <= r && r <= 0xdfff:
			return true
		case 0xd800 <= r && r <= 0xdbff:
			lo, ok := escapedUnit(b, i+2)
			if i+1 >= len(b) || b[i+1] != '\\' || !ok || lo < 0xdc00 || lo > 0xdfff {
				return true
			}
			i += 6
		}
	}
	return false
}

// escapedUnit is the UTF-16 code unit of the u escape at b[i], the byte
// after its backslash.
func escapedUnit(b []byte, i int) (rune, bool) {
	if i+4 >= len(b) || b[i] != 'u' {
		return 0, false
	}
	v, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(v), true
}

// child is a node's child as JSONObj files it, under its member name, or ""
// for an array's element
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L142-L167 at v19.2.7
// and v20.2.4).
type child struct {
	name string
	node *node
}

// children are n's children in the order JSONObj::find_first() walks them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L103-L108 at v19.2.7
// and v20.2.4): by name, which std::multimap orders bytewise, with equal
// names in the order they were added, the document's. An array's are all
// named "", so its order is the document's; a string or scalar has none.
func (n *node) children() []child {
	cs := make([]child, len(n.elems))
	for i, e := range n.elems {
		cs[i].node = e
		if n.kind == kindObject {
			cs[i].name = n.names[i]
		}
	}
	if n.kind == kindObject {
		slices.SortStableFunc(cs, func(a, b child) int { return strings.Compare(a.name, b.name) })
	}
	return cs
}

// find is the child JSONObj::find_first(name) points at
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L110-L116 at v19.2.7
// and v20.2.4): the first member of n named name, as libstdc++'s
// multimap::find answers the first of equal keys.
func (n *node) find(name string) *node {
	if n.kind != kindObject {
		return nil
	}
	if i := slices.Index(n.names, name); i >= 0 {
		return n.elems[i]
	}
	return nil
}

// data is JSONObj::get_data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L169-L182 at v19.2.7
// and v20.2.4): a string's contents, or json_spirit::write_string's text of
// any other value.
func (n *node) data() string {
	if n.kind == kindString || n.kind == kindScalar {
		return n.text
	}
	var b strings.Builder
	n.write(&b)
	return b.String()
}

// write is write_string without options
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_writer_template.h#L371-L381 at v19.2.7
// and v20.2.4), the text its Generator writes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_writer_template.h#L160-L354 at v19.2.7
// and v20.2.4): no white space, members in document order.
func (n *node) write(b *strings.Builder) {
	switch n.kind {
	case kindString:
		writeString(b, n.text)
	case kindScalar:
		b.WriteString(n.text)
	default:
		open, closing := byte('['), byte(']')
		if n.kind == kindObject {
			open, closing = '{', '}'
		}
		b.WriteByte(open)
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == kindObject {
				writeString(b, n.names[i])
				b.WriteByte(':')
			}
			e.write(b)
		}
		b.WriteByte(closing)
	}
}

// writeString is Generator::output of a string, add_esc_chars without
// raw_utf8
// (https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_writer_template.h#L50-L103 at v19.2.7
// and v20.2.4): the quote, the backslash, and backspace, form feed, newline,
// carriage return and tab escaped, and any other byte iswprint refuses
// written as a u escape of the byte. In the C locale, which radosgw never
// leaves, iswprint takes 0x20 to 0x7e alone, so each byte of a character
// past ASCII is escaped alone.
func writeString(b *strings.Builder, s string) {
	const hex = "0123456789ABCDEF"
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if 0x20 <= c && c <= 0x7e {
				b.WriteByte(c)
				continue
			}
			b.WriteString(`\` + "u00")
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	b.WriteByte('"')
}
