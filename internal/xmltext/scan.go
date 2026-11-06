package xmltext

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Schema is an S3 XML request body's table: the root's name and, for each
// element that holds elements, the children it may hold. A name with no
// entry in Children is a field, which holds text alone. Names are compared as
// written, a prefix part of the name, as radosgw reads them with expat's
// namespace processing off
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158 at v19.2.7 and v20.2.4).
// An element holds at most 64 children.
type Schema struct {
	Root     string
	Children map[string][]Child
	// Namespaces are the namespaces a namespace declaration may name, ""
	// among them where xmlns="" is taken.
	Namespaces []string
	// Lenient reads what the table does not name: an element it does not
	// place where it stands, read as a field; an attribute other than a
	// namespace declaration; and text in an element that holds elements. A
	// namespace declaration, xmlns or xmlns:prefix, must still name one of
	// Namespaces, and an element the table places where it stands may still
	// appear only as often as the table allows.
	Lenient bool
}

// Child is an element a container holds: its name, whether it may appear
// more than once, and, for a field, the most bytes of decoded text it may
// hold, 0 for no bound but the body's.
type Child struct {
	Name    string
	Repeats bool
	MaxText int
}

// Refusal names the rule a body broke where Scan refused it.
type Refusal int

// The rules Scan refuses a body for.
const (
	// RefusedSyntax is a token Go's decoder cannot read.
	RefusedSyntax Refusal = iota + 1
	// RefusedDeclaration is a <! declaration, a document type declaration
	// among them, whose internal subset can declare entities that expat
	// expands, as RGWXMLParser sets no handler that would stop it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L213-L223 at v19.2.7 and v20.2.4).
	RefusedDeclaration
	// RefusedRoot is a root other than the table's, a prefixed one among
	// them.
	RefusedRoot
	// RefusedSecondRoot is an element after the root.
	RefusedSecondRoot
	// RefusedEndTag is an end tag without a start tag, or one closing an
	// element of another name.
	RefusedEndTag
	// RefusedNested is an element inside a field.
	RefusedNested
	// RefusedPrefix is a prefixed element name, which radosgw reads as
	// another name, even when its prefix is bound to S3's namespace.
	RefusedPrefix
	// RefusedUnknown is an element the table does not place where it
	// stands, under a strict table.
	RefusedUnknown
	// RefusedRepeated is a second of an element the table allows once, where
	// RGWXMLDecoder::decode_xml reads the first
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241 at v19.2.7 and v20.2.4).
	RefusedRepeated
	// RefusedNamespace is a namespace declaration naming a namespace the
	// table does not list.
	RefusedNamespace
	// RefusedAttribute is an attribute other than a default namespace
	// declaration, under a strict table.
	RefusedAttribute
	// RefusedText is text other than XML whitespace outside the root, or,
	// under a strict table, in an element that holds elements.
	RefusedText
	// RefusedTextLength is a field's text past its MaxText.
	RefusedTextLength
	// RefusedProcInst is a processing instruction whose target is followed
	// by neither whitespace nor ?>, or an XML declaration that is malformed
	// or does not open the body.
	RefusedProcInst
	// RefusedRepeatedAttribute is an attribute named twice on one element.
	RefusedRepeatedAttribute
	// RefusedIncomplete is a body that ends before its root does, or holds
	// none.
	RefusedIncomplete
	// RefusedEnd is the refusal of Scan's end function.
	RefusedEnd
)

// ScanError is Scan's refusal: the rule broken, the byte offset in the body
// of the token that broke it, a leading byte order mark counted, and what
// was refused, which for RefusedEnd is the end function's error. Client text
// in a refusal Scan makes is cut by Clip, so its Error is at most a few
// hundred bytes.
type ScanError struct {
	Refusal Refusal
	Offset  int64
	Err     error
}

// Error is Err's text and the offset.
func (e *ScanError) Error() string {
	return e.Err.Error() + " at byte " + strconv.FormatInt(e.Offset, 10)
}

// Unwrap returns Err.
func (e *ScanError) Unwrap() error { return e.Err }

// Closed is an element Scan has read to its end tag, as its end function
// sees it.
type Closed struct {
	// Name is the element's name as written.
	Name string
	// Text is a field's text, its character data with references and CDATA
	// decoded and comments and processing instructions dropped, untrimmed;
	// empty for an element that holds elements. It is valid only during the
	// call.
	Text     []byte
	children []Child
	seen     uint64
}

// Has reports whether the element held a child named name.
func (c Closed) Has(name string) bool {
	i := slices.IndexFunc(c.children, func(ch Child) bool { return ch.Name == name })
	return i >= 0 && c.seen&(1<<i) != 0
}

// frame is an open element: its name, the children it may hold, which of
// them have started, by their index there, and, for a field, its bound and
// the bytes of text read so far.
type frame struct {
	name     xml.Name
	field    bool
	children []Child
	seen     uint64
	maxText  int
	size     int
}

// Scan reads body's tokens against s, building no tree, and refuses the
// first that breaks the table, so a refused body costs a read of its tokens
// up to the refusal. A field holds no element and an element the table does
// not place is refused or read as a field, so nothing is read deeper than
// the table. When end is not nil it is called as each element ends, after
// its children, and its error refuses the body at that end tag. Every
// refusal is a *ScanError.
//
// Besides the table's rules, Scan refuses a declaration and every body Parse
// refuses: a token Go's decoder cannot read; a second root; text outside the
// root, judged as written, so whitespace CDATA or a reference to a space
// there too; an end tag without a start tag or closing an element of another
// name; an attribute named twice on one element; a processing instruction
// Parse refuses, a misplaced or malformed XML declaration among them; and a
// body that ends before its root does, or holds none. So no body Scan
// accepts is one Parse refuses. A refusal of Parse's met before the root's
// start tag is made at that tag, after the root's own name is judged, so a
// root other than the table's is refused as such whatever the decoder passed
// before it. A leading UTF-8 byte order mark is skipped, as Parse and expat
// skip it. Scan reads with Go's decoder, as Parse does, so a field's text is
// the text Parse gives that element.
func Scan(body []byte, s *Schema, end func(Closed) error) error {
	doc := bytes.TrimPrefix(body, utf8BOM)
	base := int64(len(body) - len(doc))
	d := xml.NewDecoder(bytes.NewReader(doc))
	var (
		open    []frame
		closed  bool
		text    []byte
		pending *ScanError
	)
	// parseOnly is a refusal Parse makes and the table does not: made at once
	// within or after the root, and held before it until the root's start
	// tag.
	parseOnly := func(e *ScanError) *ScanError {
		if len(open) > 0 || closed {
			return e
		}
		if pending == nil {
			pending = e
		}
		return nil
	}
	for {
		start := d.InputOffset()
		off := base + start
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			switch {
			case pending != nil:
				return pending
			case len(open) > 0:
				return refuse(RefusedIncomplete, off, "the body ends inside "+Quote(qname(open[len(open)-1].name)))
			case !closed:
				return refuse(RefusedIncomplete, off, "the body holds no root")
			}
			return nil
		}
		if err != nil {
			return refuse(RefusedSyntax, off, Clip(err.Error(), 128))
		}
		raw := doc[start:d.InputOffset()]
		switch t := tok.(type) {
		case xml.StartElement:
			f, r, why := s.place(open, closed, t.Name)
			if r == 0 && pending != nil {
				return pending
			}
			if r == 0 {
				r, why = s.checkAttrs(t)
			}
			if r != 0 {
				return refuse(r, off, why)
			}
			open = append(open, f)
			text = text[:0]
		case xml.EndElement:
			if len(open) == 0 {
				return refuse(RefusedEndTag, off, "an end tag without a start tag")
			}
			f := open[len(open)-1]
			if t.Name != f.name {
				return refuse(RefusedEndTag, off, "an end tag "+Quote(qname(t.Name))+" closing "+Quote(qname(f.name)))
			}
			open = open[:len(open)-1]
			if len(open) == 0 {
				closed = true
			}
			if end == nil {
				continue
			}
			c := Closed{Name: qname(f.name), children: f.children, seen: f.seen}
			if f.field {
				c.Text = text
			}
			if err := end(c); err != nil {
				return &ScanError{Refusal: RefusedEnd, Offset: off, Err: err}
			}
		case xml.CharData:
			if len(open) == 0 {
				switch {
				case !isSpace(t):
					return refuse(RefusedText, off, "text outside the root")
				case !isSpace(raw):
					if e := parseOnly(refuse(RefusedText, off, "text outside the root")); e != nil {
						return e
					}
				}
				continue
			}
			f := &open[len(open)-1]
			if !f.field {
				if !s.Lenient && !isSpace(t) {
					return refuse(RefusedText, off, "text in "+qname(f.name))
				}
				continue
			}
			f.size += len(t)
			if f.maxText > 0 && f.size > f.maxText {
				return refuse(RefusedTextLength, off, qname(f.name)+" holds more than "+strconv.Itoa(f.maxText)+" bytes")
			}
			if end != nil {
				text = append(text, t...)
			}
		case xml.ProcInst:
			if checkProcInst(t, raw, start == 0) == nil {
				continue
			}
			why := "a misplaced or malformed XML declaration"
			if !equalFoldASCII(t.Target, "xml") {
				why = "a processing-instruction target " + Quote(t.Target) + " followed by neither whitespace nor ?>"
			}
			if e := parseOnly(refuse(RefusedProcInst, off, why)); e != nil {
				return e
			}
		case xml.Directive:
			return refuse(RefusedDeclaration, off, "a declaration "+Quote(string(t)))
		}
	}
}

// place is the frame a start tag named n opens inside open, or the rule it
// breaks there and why. closed reports whether the root has ended. It marks
// the child started in its parent.
func (s *Schema) place(open []frame, closed bool, n xml.Name) (frame, Refusal, string) {
	if len(open) == 0 {
		switch {
		case closed:
			return frame{}, RefusedSecondRoot, "a second root " + Quote(qname(n))
		case n != (xml.Name{Local: s.Root}):
			return frame{}, RefusedRoot, "the root is " + Quote(qname(n)) + ", not " + s.Root
		}
		return s.open(n, 0), 0, ""
	}
	p := &open[len(open)-1]
	if p.field {
		return frame{}, RefusedNested, "an element " + Quote(qname(n)) + " nested in " + Quote(qname(p.name))
	}
	if n.Space != "" {
		return frame{}, RefusedPrefix, "the prefixed element name " + Quote(qname(n))
	}
	i := slices.IndexFunc(p.children, func(c Child) bool { return c.Name == n.Local })
	switch {
	case i < 0 && s.Lenient:
		return frame{name: n, field: true}, 0, ""
	case i < 0:
		return frame{}, RefusedUnknown, "an unknown element " + Quote(n.Local) + " in " + qname(p.name)
	case p.seen&(1<<i) != 0 && !p.children[i].Repeats:
		return frame{}, RefusedRepeated, "a repeated " + n.Local + " in " + qname(p.name)
	}
	p.seen |= 1 << i
	return s.open(n, p.children[i].MaxText), 0, ""
}

// open is the frame of a table element named n with the bound maxText.
func (s *Schema) open(n xml.Name, maxText int) frame {
	children, holds := s.Children[n.Local]
	if len(children) > 64 {
		panic("xmltext: the schema's " + n.Local + " holds more than 64 children")
	}
	return frame{name: n, field: !holds, children: children, maxText: maxText}
}

// fewAttrs is how many of a tag's attributes checkAttrs judges for repeats
// by comparing each with those before it, which allocates nothing.
const fewAttrs = 8

// sameAttrName is how checkAttrs compares two of a tag's first few attribute
// names, a variable so that specs can count the comparisons.
var sameAttrName = func(a, b xml.Name) bool { return a == b }

// checkAttrs is the first rule t's attributes break, and why: an attribute
// named twice, as Parse refuses it; a namespace declaration naming a
// namespace s does not list; and, under a strict table, any attribute but a
// default namespace declaration. A lenient table admits any number of
// attributes, so past the first few a repeat is judged with a set, keeping a
// tag's cost linear in its attributes.
func (s *Schema) checkAttrs(t xml.StartElement) (r Refusal, why string) {
	var named map[xml.Name]bool
	for i, a := range t.Attr {
		if i == fewAttrs {
			named = make(map[xml.Name]bool, len(t.Attr))
			for _, b := range t.Attr[:i] {
				named[b.Name] = true
			}
		}
		var repeated bool
		if named == nil {
			repeated = slices.ContainsFunc(t.Attr[:i], func(b xml.Attr) bool { return sameAttrName(b.Name, a.Name) })
		} else {
			repeated = named[a.Name]
			named[a.Name] = true
		}
		switch {
		case repeated:
			return RefusedRepeatedAttribute, "the attribute " + Quote(qname(a.Name)) + " twice on " + Quote(qname(t.Name))
		case a.Name == (xml.Name{Local: "xmlns"}) || s.Lenient && a.Name.Space == "xmlns":
			if !slices.Contains(s.Namespaces, a.Value) {
				return RefusedNamespace, "the namespace " + Quote(a.Value) + " on " + Quote(qname(t.Name))
			}
		case !s.Lenient:
			return RefusedAttribute, "an attribute " + Quote(qname(a.Name)) + "=" + Quote(a.Value) + " on " + qname(t.Name)
		}
	}
	return 0, ""
}

func refuse(r Refusal, off int64, why string) *ScanError {
	return &ScanError{Refusal: r, Offset: off, Err: errors.New(why)}
}

// isSpace reports whether text is XML whitespace alone.
func isSpace(text []byte) bool { return len(bytes.Trim(text, xmlSpace)) == 0 }

// Quote is s cut by Clip to 32 bytes, then quoted. strconv.Quote writes a
// byte as up to four, so the result is at most 133 bytes.
func Quote(s string) string { return strconv.Quote(Clip(s, 32)) }

// Clip is s cut at a rune boundary to at most n bytes, marked when cut, so a
// refusal that repeats a client's text stays small. Cutting by runes would
// not bound it: a non-printable rune quotes as up to ten bytes.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
