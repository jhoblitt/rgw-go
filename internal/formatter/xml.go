package formatter

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// xmlDeclaration is XMLFormatter::XML_1_DTD (Formatter.cc:377-378 at v19.2.6).
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
// (Formatter.cc:377-654 at v19.2.6); radosgw never builds one that keeps
// spaces in element names (rgw_rest.cc:1796).
func NewXML(pretty, lowercased bool) Formatter {
	return &xmlFormatter{pretty: pretty, lowercased: lowercased}
}

// elementName is get_xml_name (:461-467, :646-654): a space becomes '_', and
// with lowercased every A-Z byte is lowered, as tolower does in the C locale.
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

// printSpaces is print_spaces (:637-644), which first finishes a pending
// dump_stream.
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

// open is open_section_in_ns (:603-622); arrays and objects are alike.
func (x *xmlFormatter) open(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ">")
	x.newline()
	x.sections = append(x.sections, name)
}

func (x *xmlFormatter) OpenObjectSection(name string) { x.open(name) }
func (x *xmlFormatter) OpenArraySection(name string)  { x.open(name) }

// CloseSection is close_section (:469-480).
func (x *xmlFormatter) CloseSection() {
	x.finishPending()
	name := x.elementName(x.sections[len(x.sections)-1])
	x.sections = x.sections[:len(x.sections)-1]
	x.printSpaces()
	x.buf.WriteString("</" + name + ">")
	x.newline()
}

// element is the shape of every XMLFormatter scalar (:482-523, :544-571).
func (x *xmlFormatter) element(name, text string) {
	e := x.elementName(name)
	x.printSpaces()
	x.buf.WriteString("<" + e + ">" + text + "</" + e + ">")
	x.newline()
}

func (x *xmlFormatter) DumpString(name, s string)    { x.element(name, xmltext.Escape(s)) }
func (x *xmlFormatter) DumpUnquoted(name, s string)  { x.element(name, xmltext.Escape(cString(s))) }
func (x *xmlFormatter) DumpInt(name string, v int64) { x.element(name, strconv.FormatInt(v, 10)) }
func (x *xmlFormatter) DumpUnsigned(name string, v uint64) {
	x.element(name, strconv.FormatUint(v, 10))
}
func (x *xmlFormatter) DumpBool(name string, v bool) { x.element(name, strconv.FormatBool(v)) }

// DumpStream is dump_stream (:536-542): the opening tag, its name written
// as given, now, and the value pending until finishPending.
func (x *xmlFormatter) DumpStream(name, s string) {
	x.printSpaces()
	x.buf.WriteString("<" + name + ">")
	x.pendingName = name
	x.pending.WriteString(s)
}

// finishPending is finish_pending_string (:624-635): the pending value,
// escaped, and the closing tag. With an empty name it writes nothing and
// keeps the value, which the next dump_stream's value is appended to.
func (x *xmlFormatter) finishPending() {
	if x.pendingName == "" {
		return
	}
	x.buf.WriteString(xmltext.Escape(x.pending.String()) + "</" + x.pendingName + ">")
	x.pendingName = ""
	x.pending.Reset()
	x.newline()
}

// DumpNull is dump_null (:493-499).
func (x *xmlFormatter) DumpNull(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ` xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`)
	x.newline()
}

func (x *xmlFormatter) SetStatus(int, string) {}

// OutputHeader is output_header (:414-422): the declaration, once.
func (x *xmlFormatter) OutputHeader() {
	if x.headerDone {
		return
	}
	x.headerDone = true
	x.buf.WriteString(xmlDeclaration)
	x.newline()
}

// OutputFooter is output_footer (:424-429): every open section closed.
func (x *xmlFormatter) OutputFooter() {
	for len(x.sections) > 0 {
		x.CloseSection()
	}
}

// Bytes finishes a pending dump_stream first, as flush does (:388-401).
func (x *xmlFormatter) Bytes() []byte {
	x.finishPending()
	return x.buf.Bytes()
}

// Reset is reset (:403-412), which also clears the header flag.
func (x *xmlFormatter) Reset() {
	x.buf.Reset()
	x.sections = x.sections[:0]
	x.headerDone = false
	x.pendingName = ""
	x.pending.Reset()
	x.err = nil
}
