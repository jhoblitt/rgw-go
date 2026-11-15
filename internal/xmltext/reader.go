package xmltext

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
)

// The reader follows RGWXMLParser (rgw_xml.h, rgw_xml.cc), identical at
// v19.2.6 and v20.2.4, which hands the whole body to expat in one call.

// Element is an element as radosgw's XMLObj holds it once RGWXMLParser has
// read it with expat's namespace processing off (XML_ParserCreate(nullptr),
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L158): its
// name as written, prefix included; its attributes under their names as
// written
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L66-L73); the
// text directly inside it, CDATA and references decoded and its children's
// text excluded
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L83-L87); and
// its child elements in document order.
//
// An attribute value keeps its literal tabs and line breaks, a CRLF read as
// LF, where expat turns each into a space. A DTD's attribute declarations are
// not applied: no default is added, and a value whose declared type is
// tokenized, such as NMTOKEN or ID, is not trimmed or collapsed as expat
// trims and collapses it.
type Element struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*Element
}

// FindFirst is XMLObj::find_first(name)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L146-L153):
// the first child named name, or nil. The children are a multimap, whose find
// returns the first of equal keys, which keep their insertion order.
func (e *Element) FindFirst(name string) *Element {
	for _, c := range e.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Find is XMLObj::find(name)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L123-L135):
// the children named name, in document order.
func (e *Element) Find(name string) iter.Seq[*Element] {
	return func(yield func(*Element) bool) {
		for _, c := range e.Children {
			if c.Name == name && !yield(c) {
				return
			}
		}
	}
}

// ErrNotWellFormed is a document expat refuses, which RGWXMLParser::parse
// reports as a failed parse
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L243-L248).
var ErrNotWellFormed = errors.New("xmltext: not well-formed")

// MaxElements is how many elements a document may start, each attribute
// counting as one, all of them and not the most open at once. MaxDepth is how
// many elements may be open at once, the root included, and MaxStartTag how
// many bytes one start tag may hold, "<" to ">". RGWXMLParser sets no bound on
// expat, installing only its element and character handlers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L219-L221 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L219-L221 at v20.2.4),
// so only the body's length, rgw_max_put_param_size, 1 MiB by default, bounds
// what it builds: 262,144 empty elements, 349,525 nested ones or a start tag
// of 262,142 attributes. Some of radosgw's ops parse a body before they
// authorize its sender, and rgw-go's DeleteObjects does, so a requester who
// names a bucket can have the tree built.
//
// The largest document the ops read within radosgw's limits holds about
// 69,000 elements and attributes: one lifecycle rule of the 23,000 tags 1 MiB
// holds, a tag being three elements in 39 bytes, the densest content in any
// request document. The largest an S3 client writes holds 43,000
// (rgw_lc_max_rules, 1,000, rules of S3's ten tags), then 39,000 (an ACL of
// the 5,588 grants a body holds, each grantee carrying two attributes), 30,000
// (a CompleteMultipartUpload of rgw_multipart_part_upload_limit, 10,000,
// parts) and 6,000 (a DeleteObjects of rgw_delete_multi_obj_max_num, 1,000,
// keys with every field). MaxElements is 131,072, 1.9 times the first and 3
// times the second, and a body of empty elements reaches it in 512 KiB. The
// deepest document holds 6 levels, a lifecycle rule's tag key
// (LifecycleConfiguration, Rule, Filter, And, Tag, Key), and MaxDepth is 64.
// The longest start tag is an ACL grantee's, 96 bytes with its two
// attributes, and MaxStartTag is 16 KiB: encoding/xml builds all of a tag's
// attributes before it returns the tag, so the bound is made as it reads.
const (
	MaxElements = 1 << 17
	MaxDepth    = 64
	MaxStartTag = 16 << 10
)

// ErrTooManyElements is a document that starts more than MaxElements elements
// and attributes. Parse stops building at the element that passes the bound, so
// a body of any length costs what MaxElements of them do.
var ErrTooManyElements = errors.New("xmltext: too many elements")

// ErrTooDeep is a document that nests more than MaxDepth elements, refused at
// the start tag that passes the bound.
var ErrTooDeep = errors.New("xmltext: elements nested too deeply")

// Parse is ParseFunc without an end function.
func Parse(doc []byte) (*Element, error) { return ParseFunc(doc, nil) }

// ParseFunc is RGWXMLParser::parse
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L225-L251): it
// reads doc as expat does with namespace processing off and no
// unknown-encoding handler
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L158,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L219-L221), and
// returns the root element. When end is not nil it is called as each element
// ends, after its children, as call_xml_end calls xml_end
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_xml.cc#L197-L205);
// its error fails the parse and is returned as it is. A document that starts
// more than MaxElements elements and attributes or nests more than MaxDepth is
// refused at the start tag that passes the bound, before the element is built,
// with ErrTooManyElements or ErrTooDeep, and one with a start tag of more than
// MaxStartTag bytes is refused as that tag is read, with ErrStartTagTooLong.
// Every other failure is ErrNotWellFormed.
//
// encoding/xml's RawToken reports names as written but checks less than
// expat, so ParseFunc adds expat's checks: one root, matched end tags, no
// repeated attribute, nothing but literal whitespace, comments and processing
// instructions outside the root, an XML declaration only at the very start
// and well-formed, a processing-instruction target followed by whitespace or
// "?>", and one DOCTYPE, before the root. A leading UTF-8 byte order mark is
// skipped, as expat skips it. docs/differences.md records where it still
// differs. Go refuses an XML version other than 1.0, an encoding other than
// UTF-8, a name with two colons, and entities a DTD declares. It reads what
// expat refuses: attributes not separated by whitespace, a malformed DOCTYPE,
// and a character reference to a surrogate code point, which Go's decoder
// reads as U+FFFD.
func ParseFunc(doc []byte, end func(*Element) error) (*Element, error) {
	doc = bytes.TrimPrefix(doc, utf8BOM)
	d, g := newDecoder(doc)
	type open struct {
		e    *Element
		text []byte
	}
	var root *Element
	var stack []open
	doctype := false
	elements := 0
	for {
		start := d.InputOffset()
		tok, err := g.rawToken(d)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, ErrStartTagTooLong) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotWellFormed, err)
		}
		raw := doc[start:d.InputOffset()]
		switch t := tok.(type) {
		case xml.StartElement:
			if root != nil && len(stack) == 0 {
				return nil, notWellFormed("junk after the root element")
			}
			units := 1 + len(t.Attr)
			if units > MaxElements-elements {
				return nil, fmt.Errorf("%w: more than %d elements and attributes", ErrTooManyElements, MaxElements)
			}
			if len(stack) == MaxDepth {
				return nil, fmt.Errorf("%w: more than %d deep", ErrTooDeep, MaxDepth)
			}
			elements += units
			e := &Element{Name: qname(t.Name), Attrs: make(map[string]string, len(t.Attr))}
			for _, a := range t.Attr {
				k := qname(a.Name)
				if _, dup := e.Attrs[k]; dup {
					return nil, notWellFormed("duplicate attribute " + k)
				}
				e.Attrs[k] = a.Value
			}
			if root == nil {
				root = e
			} else {
				parent := stack[len(stack)-1].e
				parent.Children = append(parent.Children, e)
			}
			stack = append(stack, open{e: e})
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, notWellFormed("end tag without a start tag")
			}
			top := stack[len(stack)-1]
			if qname(t.Name) != top.e.Name {
				return nil, notWellFormed("mismatched tag")
			}
			top.e.Text = string(top.text)
			stack = stack[:len(stack)-1]
			if end != nil {
				if err := end(top.e); err != nil {
					return nil, err
				}
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text = append(stack[len(stack)-1].text, t...)
			} else if len(bytes.Trim(raw, xmlSpace)) > 0 {
				return nil, notWellFormed("text outside the root element")
			}
		case xml.ProcInst:
			if err := checkProcInst(t, raw, start == 0); err != nil {
				return nil, err
			}
		case xml.Directive:
			if root != nil || doctype || !isDoctype(t) {
				return nil, notWellFormed("misplaced declaration")
			}
			doctype = true
		}
	}
	if root == nil {
		return nil, notWellFormed("no element found")
	}
	if len(stack) > 0 {
		return nil, notWellFormed("unclosed element")
	}
	return root, nil
}

// utf8BOM is the byte order mark expat accepts before a UTF-8 document.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// xmlSpace is XML's S production, the bytes expat takes for whitespace.
const xmlSpace = " \t\r\n"

func notWellFormed(reason string) error {
	return fmt.Errorf("%w: %s", ErrNotWellFormed, reason)
}

// qname is a name as written, prefix included.
func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// isDoctype reports whether a directive is a document type declaration.
func isDoctype(d xml.Directive) bool {
	rest, ok := bytes.CutPrefix(d, []byte("DOCTYPE"))
	return ok && len(rest) > 0 && bytes.IndexByte([]byte(xmlSpace), rest[0]) >= 0
}

// checkProcInst makes expat's checks on a processing instruction, raw as
// written: its target is followed by whitespace or by "?>"; a target that is
// xml in any case is the XML declaration, which must be written xml, open the
// document and be well-formed.
func checkProcInst(t xml.ProcInst, raw []byte, first bool) error {
	after := raw[len("<?")+len(t.Target):]
	if string(after) != "?>" && bytes.IndexByte([]byte(xmlSpace), after[0]) < 0 {
		return notWellFormed("processing instruction target " + t.Target)
	}
	if !equalFoldASCII(t.Target, "xml") {
		return nil
	}
	if t.Target != "xml" || !first {
		return notWellFormed("misplaced XML declaration")
	}
	if !xmlDecl(raw[len("<?xml") : len(raw)-len("?>")]) {
		return notWellFormed("XML declaration")
	}
	return nil
}

// xmlDecl reports whether the pseudo-attributes of an XML declaration are
// well-formed as expat's doParseXmlDecl requires: version, then optionally
// encoding, then optionally standalone, yes or no, in that order, each after
// whitespace. Go refuses a version other than 1.0 and an encoding other than
// UTF-8 when it finds them, and this refuses them however they are spaced.
func xmlDecl(b []byte) bool {
	name, val, rest, ok := pseudoAttr(b)
	if !ok || name != "version" || val != "1.0" {
		return false
	}
	if name, val, rest, ok = pseudoAttr(rest); !ok || name == "" {
		return ok
	}
	if name == "encoding" {
		if !equalFoldASCII(val, "UTF-8") {
			return false
		}
		if name, val, rest, ok = pseudoAttr(rest); !ok || name == "" {
			return ok
		}
	}
	if name != "standalone" || (val != "yes" && val != "no") {
		return false
	}
	name, _, _, ok = pseudoAttr(rest)
	return ok && name == ""
}

// pseudoAttr is expat's parsePseudoAttribute: whitespace, a name, "=" with
// optional whitespace around it, and a value of letters, digits, ".", "_" or
// "-" in single or double quotes. An empty name with ok set is the end of the
// declaration, which only whitespace may precede.
func pseudoAttr(b []byte) (name, val string, rest []byte, ok bool) {
	trimmed := bytes.TrimLeft(b, xmlSpace)
	if len(trimmed) == 0 {
		return "", "", nil, true
	}
	if len(trimmed) == len(b) {
		return "", "", nil, false
	}
	b = trimmed
	i := bytes.IndexAny(b, "="+xmlSpace)
	if i <= 0 {
		return "", "", nil, false
	}
	name, b = string(b[:i]), bytes.TrimLeft(b[i:], xmlSpace)
	eq, ok := bytes.CutPrefix(b, []byte("="))
	if !ok {
		return "", "", nil, false
	}
	b = bytes.TrimLeft(eq, xmlSpace)
	if len(b) == 0 || (b[0] != '"' && b[0] != '\'') {
		return "", "", nil, false
	}
	j := bytes.IndexByte(b[1:], b[0])
	if j < 0 {
		return "", "", nil, false
	}
	val = string(b[1 : 1+j])
	for _, c := range []byte(val) {
		if !pseudoValueByte(c) {
			return "", "", nil, false
		}
	}
	return name, val, b[2+j:], true
}

func pseudoValueByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-'
}

// equalFoldASCII reports whether a and b are equal with ASCII letters folded.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
