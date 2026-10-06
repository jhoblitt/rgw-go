package formatter

import (
	"bytes"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// jsonSection is json_formatter_stack_entry_d.
type jsonSection struct {
	size    int
	isArray bool
}

type jsonFormatter struct {
	failure
	pretty bool
	buf    bytes.Buffer
	stack  []jsonSection
}

// NewJSON is JSONFormatter(pretty) (Formatter.cc:153-375 at v19.2.6).
func NewJSON(pretty bool) Formatter { return &jsonFormatter{pretty: pretty} }

// indent writes four spaces for each open section after the first.
func (j *jsonFormatter) indent() {
	for range max(len(j.stack)-1, 0) {
		j.buf.WriteString("    ")
	}
}

// printComma is print_comma (Formatter.cc:172-190).
func (j *jsonFormatter) printComma(s *jsonSection) {
	switch {
	case s.size > 0 && j.pretty:
		j.buf.WriteString(",\n")
		j.indent()
	case s.size > 0:
		j.buf.WriteByte(',')
	case j.pretty:
		j.buf.WriteByte('\n')
		j.indent()
	}
	if j.pretty && s.isArray {
		j.buf.WriteString("    ")
	}
}

// printName is print_name (:198-217): nothing at the top level, and no name
// inside an array.
func (j *jsonFormatter) printName(name string) {
	if len(j.stack) == 0 {
		return
	}
	s := &j.stack[len(j.stack)-1]
	j.printComma(s)
	if !s.isArray {
		if j.pretty {
			j.buf.WriteString("    ")
		}
		j.buf.WriteByte('"')
		j.buf.WriteString(name)
		j.buf.WriteByte('"')
		if j.pretty {
			j.buf.WriteString(": ")
		} else {
			j.buf.WriteByte(':')
		}
	}
	s.size++
}

// open is open_section (:219-240).
func (j *jsonFormatter) open(name string, isArray bool) {
	j.printName(name)
	if isArray {
		j.buf.WriteByte('[')
	} else {
		j.buf.WriteByte('{')
	}
	j.stack = append(j.stack, jsonSection{isArray: isArray})
}

func (j *jsonFormatter) OpenObjectSection(name string) { j.open(name, false) }
func (j *jsonFormatter) OpenArraySection(name string)  { j.open(name, true) }

// CloseSection is close_section (:262-281).
func (j *jsonFormatter) CloseSection() {
	s := j.stack[len(j.stack)-1]
	if j.pretty && s.size > 0 {
		j.buf.WriteByte('\n')
		j.indent()
	}
	if s.isArray {
		j.buf.WriteByte(']')
	} else {
		j.buf.WriteByte('}')
	}
	j.stack = j.stack[:len(j.stack)-1]
	if j.pretty && len(j.stack) == 0 {
		j.buf.WriteByte('\n')
	}
}

// value is add_value(name, val, quoted=false) (:301-313).
func (j *jsonFormatter) value(name, v string) {
	j.printName(name)
	j.buf.WriteString(v)
}

// quoted is add_value(name, val, quoted=true) with print_quoted_string
// (:192-196).
func (j *jsonFormatter) quoted(name, v string) {
	j.printName(name)
	j.buf.WriteByte('"')
	j.buf.WriteString(EscapeJSON(v))
	j.buf.WriteByte('"')
}

func (j *jsonFormatter) DumpString(name, s string)    { j.quoted(name, s) }
func (j *jsonFormatter) DumpStream(name, s string)    { j.quoted(name, s) }
func (j *jsonFormatter) DumpUnquoted(name, s string)  { j.value(name, rgwtext.CString(s)) }
func (j *jsonFormatter) DumpInt(name string, v int64) { j.value(name, strconv.FormatInt(v, 10)) }

func (j *jsonFormatter) DumpUnsigned(name string, v uint64) { j.value(name, strconv.FormatUint(v, 10)) }
func (j *jsonFormatter) DumpBool(name string, v bool)       { j.value(name, strconv.FormatBool(v)) }
func (j *jsonFormatter) DumpNull(name string)               { j.value(name, "null") }
func (j *jsonFormatter) SetStatus(int, string)              {}
func (j *jsonFormatter) OutputHeader()                      {}
func (j *jsonFormatter) OutputFooter()                      {}
func (j *jsonFormatter) Bytes() []byte                      { return j.buf.Bytes() }

func (j *jsonFormatter) Reset() {
	j.buf.Reset()
	j.stack = j.stack[:0]
	j.err = nil
}
