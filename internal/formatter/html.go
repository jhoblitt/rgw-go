package formatter

import (
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// htmlFormatter is HTMLFormatter: XMLFormatter's sections and dump_null, with
// every other scalar a list item and a status page as its header.
type htmlFormatter struct {
	xmlFormatter
	status     int
	statusName string
}

// NewHTML is HTMLFormatter(pretty)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L34-L168 at v19.2.6),
// an XMLFormatter that neither lowercases nor keeps spaces in section
// names.
func NewHTML(pretty bool) Formatter {
	return &htmlFormatter{pretty: pretty}
}

// item is dump_template and dump_format_va
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L93-L100,
// https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L141-L168):
// the name as given, the value escaped when it is text.
func (h *htmlFormatter) item(name, text string) {
	h.printSpaces()
	h.buf.WriteString("<li>" + name + ": " + text + "</li>")
	h.newline()
}

func (h *htmlFormatter) DumpString(name, s string) { h.item(name, xmltext.Escape(s)) }
func (h *htmlFormatter) DumpUnquoted(name, s string) {
	h.item(name, xmltext.Escape(rgwtext.CString(s)))
}
func (h *htmlFormatter) DumpInt(name string, v int64) { h.item(name, strconv.FormatInt(v, 10)) }

func (h *htmlFormatter) DumpUnsigned(name string, v uint64) { h.item(name, strconv.FormatUint(v, 10)) }
func (h *htmlFormatter) DumpBool(name string, v bool)       { h.item(name, strconv.FormatBool(v)) }

// DumpStream is dump_stream
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L133-L139):
// the list item opened now, its value pending under the name "li" until
// finishPending.
func (h *htmlFormatter) DumpStream(name, s string) {
	h.printSpaces()
	h.buf.WriteString("<li>" + name + ": ")
	h.pendingName = "li"
	h.pending.WriteString(s)
}

// SetStatus is set_status
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L58-L67):
// a missing name keeps the previous one.
func (h *htmlFormatter) SetStatus(code int, name string) {
	h.status = code
	if name != "" {
		h.statusName = name
	}
}

// OutputHeader is output_header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L69-L91):
// html, head, body, h1 and an open ul, once until a reset.
func (h *htmlFormatter) OutputHeader() {
	if h.headerDone {
		return
	}
	h.headerDone = true
	line := strconv.Itoa(h.status)
	if h.statusName != "" {
		line += " " + h.statusName
	}
	h.OpenObjectSection("html")
	h.printSpaces()
	h.buf.WriteString("<head><title>" + line + "</title></head>")
	h.newline()
	h.OpenObjectSection("body")
	h.printSpaces()
	h.buf.WriteString("<h1>" + line + "</h1>")
	h.newline()
	h.OpenObjectSection("ul")
}

// Reset is reset
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L47-L56).
func (h *htmlFormatter) Reset() {
	h.xmlFormatter.Reset()
	h.status = 0
	h.statusName = ""
}
