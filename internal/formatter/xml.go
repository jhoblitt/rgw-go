package formatter

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// xmlDeclaration is XMLFormatter::XML_1_DTD
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L377-L378 at v19.2.6).
const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>`

type xmlFormatter struct {
	failure
	pretty, lowercased bool
	buf                bytes.Buffer
	sections           []string
	headerDone         bool
	// pendingName and pending are m_pending_string_name and
	// m_pending_string: a dump_stream's value, written with its closing tag
	// by the next call that finishes it.
	pendingName string
	pending     strings.Builder
}

// NewXML is XMLFormatter(pretty, lowercased, underscored=true)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L377-L654 at v19.2.6);
// radosgw never builds one that keeps spaces in element names
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1796).
func NewXML(pretty, lowercased bool) Formatter {
	return &xmlFormatter{pretty: pretty, lowercased: lowercased}
}

// elementName is get_xml_name
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L461-L467,
// https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L646-L654):
// a space becomes '_', and with lowercased every A-Z byte is lowered, as
// tolower does in the C locale.
func (x *xmlFormatter) elementName(name string) string {
	b := []byte(name)
	for i, c := range b {
		switch {
		case c == ' ':
			b[i] = '_'
		case x.lowercased && 'A' <= c && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// printSpaces is print_spaces
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L637-L644),
// which first finishes a pending dump_stream.
func (x *xmlFormatter) printSpaces() {
	x.finishPending()
	if x.pretty {
		x.buf.WriteString(strings.Repeat(" ", len(x.sections)))
	}
}

func (x *xmlFormatter) newline() {
	if x.pretty {
		x.buf.WriteByte('\n')
	}
}

// open is open_section_in_ns
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L603-L622);
// arrays and objects are alike.
func (x *xmlFormatter) open(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ">")
	x.newline()
	x.sections = append(x.sections, name)
}

func (x *xmlFormatter) OpenObjectSection(name string) { x.open(name) }
func (x *xmlFormatter) OpenArraySection(name string)  { x.open(name) }

// CloseSection is close_section
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L469-L480).
func (x *xmlFormatter) CloseSection() {
	x.finishPending()
	name := x.elementName(x.sections[len(x.sections)-1])
	x.sections = x.sections[:len(x.sections)-1]
	x.printSpaces()
	x.buf.WriteString("</" + name + ">")
	x.newline()
}

// element is the shape of every XMLFormatter scalar
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L482-L523,
// https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L544-L571).
func (x *xmlFormatter) element(name, text string) {
	e := x.elementName(name)
	x.printSpaces()
	x.buf.WriteString("<" + e + ">" + text + "</" + e + ">")
	x.newline()
}

func (x *xmlFormatter) DumpString(name, s string) { x.element(name, xmltext.Escape(s)) }
func (x *xmlFormatter) DumpUnquoted(name, s string) {
	x.element(name, xmltext.Escape(rgwtext.CString(s)))
}
func (x *xmlFormatter) DumpInt(name string, v int64) { x.element(name, strconv.FormatInt(v, 10)) }
func (x *xmlFormatter) DumpUnsigned(name string, v uint64) {
	x.element(name, strconv.FormatUint(v, 10))
}
func (x *xmlFormatter) DumpBool(name string, v bool) { x.element(name, strconv.FormatBool(v)) }

// DumpStream is dump_stream
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L536-L542):
// the opening tag, its name written as given, now, and the value pending
// until finishPending.
func (x *xmlFormatter) DumpStream(name, s string) {
	x.printSpaces()
	x.buf.WriteString("<" + name + ">")
	x.pendingName = name
	x.pending.WriteString(s)
}

// finishPending is finish_pending_string
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L624-L635):
// the pending value, escaped, and the closing tag. With an empty name it
// writes nothing and keeps the value, which the next dump_stream's value
// is appended to.
func (x *xmlFormatter) finishPending() {
	if x.pendingName == "" {
		return
	}
	x.buf.WriteString(xmltext.Escape(x.pending.String()) + "</" + x.pendingName + ">")
	x.pendingName = ""
	x.pending.Reset()
	x.newline()
}

// DumpNull is dump_null
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L493-L499).
func (x *xmlFormatter) DumpNull(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ` xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`)
	x.newline()
}

func (x *xmlFormatter) SetStatus(int, string) {}

// OutputHeader is output_header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L414-L422):
// the declaration, once.
func (x *xmlFormatter) OutputHeader() {
	if x.headerDone {
		return
	}
	x.headerDone = true
	x.buf.WriteString(xmlDeclaration)
	x.newline()
}

// OutputFooter is output_footer
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L424-L429):
// every open section closed.
func (x *xmlFormatter) OutputFooter() {
	for len(x.sections) > 0 {
		x.CloseSection()
	}
}

// Bytes finishes a pending dump_stream first, as flush does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L388-L401).
func (x *xmlFormatter) Bytes() []byte {
	x.finishPending()
	return x.buf.Bytes()
}

// Reset is reset
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L403-L412),
// which also clears the header flag.
func (x *xmlFormatter) Reset() {
	x.buf.Reset()
	x.sections = x.sections[:0]
	x.headerDone = false
	x.pendingName = ""
	x.pending.Reset()
	x.err = nil
}
