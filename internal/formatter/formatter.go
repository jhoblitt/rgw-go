package formatter

import (
	"fmt"
	"strings"
)

// Formatter is the part of ceph::Formatter (src/common/Formatter.h) that
// radosgw's admin handlers call. Names are written exactly as given,
// unescaped, as the C++ writes them; string values are escaped.
type Formatter interface {
	OpenObjectSection(name string)
	OpenArraySection(name string)
	CloseSection()
	DumpString(name, s string)
	// DumpStream is dump_stream(name) << s, which the C++ uses for times and
	// the index type: a string, except that XMLFormatter writes the element
	// name without get_xml_name (Formatter.cc:536-542).
	DumpStream(name, s string)
	// DumpUnquoted is dump_format_unquoted(name, "%s", s): bare in JSON, and
	// cut at its first NUL, where vsnprintf's "%s" stops.
	DumpUnquoted(name, s string)
	DumpInt(name string, v int64)
	DumpUnsigned(name string, v uint64)
	// DumpBool is Formatter::dump_bool, dump_format_unquoted of "true" or
	// "false".
	DumpBool(name string, v bool)
	DumpNull(name string)
	// SetStatus, OutputHeader and OutputFooter are the calls radosgw's REST
	// layer makes around a body (rgw_rest.cc:289-314, :571-577 at v19.2.6).
	// Only HTMLFormatter uses the status.
	SetStatus(code int, name string)
	OutputHeader()
	OutputFooter()
	// Fail records that a value cannot be rendered and Err returns the first
	// such failure; ceph's Formatter has no counterpart.
	Fail(err error)
	Err() error
	// Bytes is what has been written since the last Reset: what flush writes,
	// without the line break a pretty XMLFormatter adds (Formatter.cc:388-401).
	Bytes() []byte
	// Reset is reset: the output, the open sections, XMLFormatter's header
	// flag and HTMLFormatter's status are cleared, and so is the failure.
	Reset()
}

// EscapeJSON is json_stream_escaper (escape.cc:254-286 at v19.2.6): '"',
// '\\', tab and newline take their short escapes; other bytes below 0x20 and
// 0x7f become \u00xx in lower-case hex; every other byte, UTF-8 included, is
// copied. XML and HTML text goes through xmltext.Escape, xml_stream_escaper.
func EscapeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		switch c := s[i]; {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// failure is the Fail and Err half every formatter shares.
type failure struct{ err error }

func (f *failure) Fail(err error) {
	if f.err == nil {
		f.err = err
	}
}

func (f *failure) Err() error { return f.err }

// cString is s as a C string reads it: up to its first NUL.
func cString(s string) string {
	s, _, _ = strings.Cut(s, "\x00")
	return s
}
