package jsonspirit

import (
	"slices"
	"strings"
)

// Kind is a value's json_spirit type, as far as JSONObj tells them apart.
type Kind uint8

// The kinds of value.
const (
	// String is a string, whose text is its contents.
	String Kind = iota
	// Scalar is a number, true, false or null, whose text is json_spirit's.
	Scalar
	Array
	Object
)

// Node is a value as JSONObj holds it.
type Node struct {
	kind Kind
	// text is a string's contents, or a scalar's text as json_spirit writes
	// it back.
	text string
	// names are an object's member names, and elems its values or an
	// array's elements, in document order with duplicate names kept, as
	// json_spirit's vector-backed Object keeps them.
	names []string
	elems []*Node
}

// Kind returns n's kind.
func (n *Node) Kind() Kind { return n.kind }

// Child is a node's child as JSONObj files it: under its member name, or ""
// for an array's element
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L142-L167 at v19.2.7
// and v20.2.4).
type Child struct {
	Name string
	Node *Node
}

// Children are n's children in the order JSONObj::find_first() walks them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L103-L108 at v19.2.7
// and v20.2.4): by name, which std::multimap orders bytewise, equal names in
// the order they were added, the document's. An array's are all named "", so
// its order is the document's; a string or scalar has none.
func (n *Node) Children() []Child {
	cs := make([]Child, len(n.elems))
	for i, e := range n.elems {
		cs[i].Node = e
		if n.kind == Object {
			cs[i].Name = n.names[i]
		}
	}
	if n.kind == Object {
		slices.SortStableFunc(cs, func(a, b Child) int { return strings.Compare(a.Name, b.Name) })
	}
	return cs
}

// Find is the child JSONObj::find_first(name) points at, or nil when it
// points at the end
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L110-L116 at v19.2.7
// and v20.2.4): the first of n's children named name, as libstdc++'s
// multimap::find answers the first of equal keys, so an array's first
// element for "".
func (n *Node) Find(name string) *Node {
	switch {
	case n.kind == Object:
		if i := slices.Index(n.names, name); i >= 0 {
			return n.elems[i]
		}
	case n.kind == Array && name == "" && len(n.elems) > 0:
		return n.elems[0]
	}
	return nil
}

// Data is JSONObj::get_data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L70 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/ceph_json.h#L75 at v20.2.4),
// the text JSONObj::init gives a value
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L169-L182 at v19.2.7
// and v20.2.4): a string's contents, or json_spirit::write_string's text of
// any other value.
func (n *Node) Data() string {
	if n.kind == String || n.kind == Scalar {
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
func (n *Node) write(b *strings.Builder) {
	switch n.kind {
	case String:
		writeString(b, n.text)
	case Scalar:
		b.WriteString(n.text)
	default:
		open, closing := byte('['), byte(']')
		if n.kind == Object {
			open, closing = '{', '}'
		}
		b.WriteByte(open)
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == Object {
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
