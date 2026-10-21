package s3

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"math"
	"math/bits"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// formFieldsAllowance is what the parts before a POST form's file part may
// take of the body beyond maxLine: room for one field of radosgw's largest
// beside the rest of a form (docs/exclusions.md, "A POST form's fields before
// its file are bounded").
const formFieldsAllowance = 1 << 20

// formBufSize is the form reader's buffer, the most it reads from the body at
// once.
const formBufSize = 64 << 10

// formMaxPadding is the most white space a delimiter may carry after its
// boundary, RFC 2046's transport padding, so that a delimiter is always
// decided within the buffer.
const formMaxPadding = 256

// cSpace is what rgw_trim_whitespace trims: isspace in the C locale.
const cSpace = " \t\n\v\f\r"

// The refusals of RGWPostObj_ObjStore::get_params, whose messages radosgw
// sends in its 400 InvalidArgument
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1431-L1473 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1431-L1473 at v20.2.4),
// and of RGWPostObj_ObjStore_S3::get_params' part loop
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2946-L2967 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3072-L3093 at v20.2.4).
// A part header line radosgw cannot parse is -EINVAL with no message
// (parse_part_field,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1147-L1162 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1147-L1162 at v20.2.4).
var (
	errFormNotMultipart    = op.ErrInvalidArgument.WithMessage("Request Content-Type is not multipart/form-data")
	errFormNoBoundaryParam = op.ErrInvalidArgument.WithMessage("Missing multipart boundary specification")
	errFormMalformed       = op.ErrInvalidArgument.WithMessage("Malformed request")
	errFormNoBoundary      = op.ErrInvalidArgument.WithMessage("Couldn't find boundary")
	errFormHeader          = op.ErrInvalidArgument
)

// errFormFieldName refuses a part name radosgw would keep whole, a control
// byte included (docs/exclusions.md, "A POST form field's name holds no
// control byte").
var errFormFieldName = op.ErrInvalidArgument.WithMessage("A form field name holds a control character")

// errFormTooLarge carries the description Amazon S3's error list gives its
// MaxPostPreDataLengthExceededError
// (https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html), under
// the InvalidArgument of radosgw's other form refusals (docs/exclusions.md,
// "A POST form's fields before its file are bounded").
var errFormTooLarge = op.ErrInvalidArgument.WithMessage("Your POST request fields preceding the upload file were too large.")

// formReader reads a POST object request's multipart/form-data body as
// radosgw's RGWPostObj_ObjStore does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1112-L1476 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1112-L1476 at v20.2.4):
// the fields before the part named file, then the file's data as a stream,
// then the parts after it, read and dropped. It differs from radosgw where
// docs/exclusions.md says, under "A POST form part ends only at a delimiter
// line", "A POST form that ends before its file part's delimiter is
// refused", "A POST form field's name holds no control byte", "A POST form's
// fields before its file are bounded" and "A quoted POST form parameter keeps
// its semicolons".
type formReader struct {
	body      io.Reader
	maxLine   int
	lineLimit int
	dash      []byte // "--" and the boundary: a delimiter at a line's start
	nlDash    []byte // CRLF, "--" and the boundary: a delimiter after a line

	buf  []byte // buf[r:w] is read from the body and not yet consumed
	r, w int
	eof  bool
	err  error // the body's read error, returned from then on
	line []byte

	read  int64 // the body's bytes consumed
	limit int64 // what read may reach before the file's data; 0 for no limit

	lineStart bool // buf[r:] starts a line, where a delimiter needs no CRLF
	inPart    bool // the bytes until the next delimiter are a part's data
	closed    bool // the close delimiter is consumed
	started   bool
	atFile    bool
}

// newFormReader checks the request's Content-Type as
// RGWPostObj_ObjStore::get_params does: its type exactly
// multipart/form-data, and a boundary parameter, named in lower case as
// radosgw's parameter map holds it. maxLine is rgw_max_chunk_size, the longest
// field radosgw reads; it has no minimum, so below 1 it is taken as 1.
func newFormReader(body io.Reader, contentType string, maxLine int) (*formReader, error) {
	mediaType, params := parseFormParams(contentType, "boundary")
	if mediaType != "multipart/form-data" {
		return nil, errFormNotMultipart
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, errFormNoBoundaryParam
	}
	maxLine = max(maxLine, 1)
	dash := "--" + boundary
	return &formReader{
		body:      body,
		maxLine:   maxLine,
		lineLimit: satAdd(maxLine, len(dash)+2),
		dash:      []byte(dash),
		nlDash:    []byte("\r\n" + dash),
		buf:       make([]byte, max(formBufSize, 2*(len(dash)+4+formMaxPadding))),
		lineStart: true,
		inPart:    true,
	}, nil
}

// Fields reads the parts before the one named file, in any ASCII case, as
// RGWPostObj_ObjStore_S3::get_params' loop reads them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2923-L2971 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3049-L3097 at v20.2.4),
// and returns them with the file part, whose Read gives its data. Fields not
// before the file are never read into the result. A preamble before the
// first delimiter is skipped, where radosgw reads it as header lines
// (docs/ceph-upstream-bugs.md, "radosgw parses a POST form's preamble as its
// first part's header lines"). A form that closes before
// its file part is 400 "Malformed request" wherever it closes, where radosgw
// stores what follows the close as the object when it ends a field's data
// (docs/exclusions.md, "A POST form that ends before its file part's
// delimiter is refused"). Each field's data is at most maxLine bytes, as
// read_data reads it, else 400 "Couldn't find boundary", and the parts before
// the file data take at most maxLine and formFieldsAllowance bytes of the
// body.
func (fr *formReader) Fields() (formFields, *formFile, error) {
	fields := formFields{byName: map[string]*formField{}}
	if fr.started {
		return fields, nil, errors.New("reading a POST form's fields twice")
	}
	fr.started = true
	fr.limit = int64(fr.maxLine) + formFieldsAllowance
	if err := fr.skipData(-1); err != nil {
		return fields, nil, err
	}
	for !fr.closed {
		h, closed, err := fr.readPartHeader()
		if err != nil {
			return fields, nil, err
		}
		if closed {
			break
		}
		if hasControlByte(h.name) {
			return fields, nil, errFormFieldName
		}
		if lowerASCIIString(h.name) == "file" {
			fr.limit = 0
			fr.atFile = true
			return fields, &formFile{Filename: h.filename, fr: fr}, nil
		}
		value, err := fr.readValue()
		if err != nil {
			return fields, nil, err
		}
		fields.add(h.name, value)
	}
	return fields, nil, errFormMalformed
}

// Drain reads the rest of the file part and every part after it, storing
// nothing, as complete_get_params does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3256-L3278 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3392-L3414 at v20.2.4):
// each part's data at most maxLine bytes, and every refusal 400
// InvalidArgument with no message, since complete_get_params sets none. It
// stops at the close delimiter and reads no epilogue.
func (fr *formReader) Drain() error {
	if !fr.atFile {
		return errors.New("draining a POST form before its file part")
	}
	if err := fr.drain(); err != nil {
		if fr.err != nil && errors.Is(err, fr.err) {
			return err
		}
		return op.ErrInvalidArgument
	}
	return nil
}

func (fr *formReader) drain() error {
	if err := fr.skipData(-1); err != nil {
		return err
	}
	for !fr.closed {
		_, closed, err := fr.readPartHeader()
		if err != nil || closed {
			return err
		}
		if err := fr.skipData(fr.maxLine); err != nil {
			return err
		}
	}
	return nil
}

// formFile is a POST form's file part: its Content-Disposition filename, ""
// when it names none, as get_params leaves filename then
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2951-L2959 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3077-L3085 at v20.2.4),
// and its data, which Read streams through the reader's buffer alone and ends
// with io.EOF at the part's delimiter. A body that ends before that delimiter
// is 400 "Couldn't find boundary", where radosgw stores the data up to the
// body's end (docs/exclusions.md, "A POST form that ends before its file
// part's delimiter is refused").
type formFile struct {
	Filename string
	fr       *formReader
}

func (f *formFile) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b, err := f.fr.nextData(len(p))
	if err != nil {
		return 0, err
	}
	return copy(p, b), nil
}

// formFields are a POST form's fields before its file part, one per name. A
// name matches in any ASCII case over all its bytes, as postpolicy.Env
// matches it, where radosgw's ltstr_nocase part map
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L246-L247 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L246-L247 at v20.2.4)
// stops at a NUL, which a name here never holds. A later field of a name
// replaces the earlier's value and keeps its spelling, as get_params'
// parts[part.name] = part and RGWPolicyEnv::add_var do, so the policy check
// and every parameter read the same, last, value.
type formFields struct {
	byName map[string]*formField
}

type formField struct{ name, value string }

func (f formFields) add(name, value string) {
	k := lowerASCIIString(name)
	if e, ok := f.byName[k]; ok {
		e.value = value
		return
	}
	f.byName[k] = &formField{name: name, value: value}
}

// Raw is the field's data as sent, as part_bl reads it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1400-L1411 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1400-L1411 at v20.2.4):
// what the policy's environment holds, and the policy field the signature
// covers.
func (f formFields) Raw(name string) (string, bool) {
	e, ok := f.byName[lowerASCIIString(name)]
	if !ok {
		return "", false
	}
	return e.value, true
}

// Trimmed is the field's data trimmed of C white space, as part_str reads a
// parameter
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1372-L1385 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1372-L1385 at v20.2.4).
func (f formFields) Trimmed(name string) (string, bool) {
	v, ok := f.Raw(name)
	return strings.Trim(v, cSpace), ok
}

// All yields each field's first spelling and raw value in strcasecmp order of
// the names, the order radosgw's part map iterates in.
func (f formFields) All() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for _, k := range slices.Sorted(maps.Keys(f.byName)) {
			if e := f.byName[k]; !yield(e.name, e.value) {
				return
			}
		}
	}
}

// partHeader is what a part's headers give: the name and filename parameters
// of its last Content-Disposition, which read_form_part_header files under
// part.fields and part.name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1335-L1367 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1335-L1367 at v20.2.4).
type partHeader struct{ name, filename string }

// readPartHeader reads a part's header lines, each trimmed of C white space,
// up to the empty one, and leaves the reader at the part's data. A line
// needs a colon, and the name before it is compared over all its bytes. A
// close delimiter in their place ends the form, as radosgw's done flag does,
// and reports closed; a part delimiter there, or a line that starts like a
// delimiter and is not one, is 400 "Malformed request".
func (fr *formReader) readPartHeader() (h partHeader, closed bool, err error) {
	for {
		kind, err := fr.readLine()
		if err != nil {
			return h, false, err
		}
		switch kind {
		case lineClose:
			fr.closed = true
			return h, true, nil
		case lineDelimiter, lineMalformed:
			return h, false, errFormMalformed
		case lineText:
		}
		line := strings.Trim(string(fr.line), cSpace)
		if line == "" {
			fr.inPart, fr.lineStart = true, true
			return h, false, nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return h, false, errFormHeader
		}
		if lowerASCIIString(name) == "content-disposition" {
			_, params := parseFormParams(value, "name", "filename")
			h = partHeader{name: strings.Clone(params["name"]), filename: strings.Clone(params["filename"])}
		}
	}
}

type lineKind int

const (
	lineText lineKind = iota
	lineDelimiter
	lineClose
	lineMalformed
)

// readLine reads a part header line into fr.line, without its CRLF. A line
// and its CRLF are at most lineLimit bytes, the widest window read_line finds
// a line end in, rgw_max_chunk_size, 2 and the boundary's length
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1195-L1200 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1195-L1200 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1222-L1254 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1222-L1254 at v20.2.4);
// radosgw cuts a longer one there and reads the rest as the next line, which
// rgw-go refuses with radosgw's header refusal instead
// (docs/ceph-upstream-bugs.md, "radosgw cuts a POST form header line longer
// than its read window and reads the rest as another header line"). The
// line is read whole to its CRLF, where radosgw's read_line also ends it at
// the boundary's text (docs/ceph-upstream-bugs.md, "radosgw cuts a POST
// upload's file at its boundary's text mid-line"). The body's end may end
// only a close delimiter's line.
func (fr *formReader) readLine() (lineKind, error) {
	fr.line = fr.line[:0]
	for {
		data := fr.buf[fr.r:fr.w]
		end := bytes.Index(data, []byte("\r\n"))
		take := end
		if end < 0 {
			take = len(data)
			if take > 0 && data[take-1] == '\r' && !fr.eof {
				take--
			}
		}
		if need := len(fr.line) + take; need > cap(fr.line) {
			grown := make([]byte, len(fr.line), max(2*cap(fr.line), 1<<bits.Len(uint(need-1))))
			copy(grown, fr.line)
			fr.line = grown
		}
		fr.line = append(fr.line, data[:take]...)
		fr.consume(take)
		if len(fr.line)+2 > fr.lineLimit {
			return lineText, errFormHeader
		}
		if end >= 0 {
			fr.consume(2)
		}
		if err := fr.overLimit(); err != nil {
			return lineText, err
		}
		if end >= 0 {
			return fr.kindOf(fr.line, true), nil
		}
		if fr.eof {
			if k := fr.kindOf(fr.line, false); k == lineClose || k == lineMalformed {
				return k, nil
			}
			return lineText, errFormNoBoundary
		}
		if err := fr.more(); err != nil {
			return lineText, err
		}
	}
}

// kindOf tells a delimiter's line from a header line by the rule
// delimiterEnd applies to what follows the boundary; ended is whether a CRLF
// ended the line, rather than the body's end.
func (fr *formReader) kindOf(line []byte, ended bool) lineKind {
	rest, ok := bytes.CutPrefix(line, fr.dash)
	if !ok {
		return lineText
	}
	rest, final := bytes.CutPrefix(rest, []byte("--"))
	after := bytes.TrimLeft(rest, " \t")
	pad := len(rest) - len(after)
	cutCR := !ended && len(after) == 1 && after[0] == '\r'
	switch {
	case !final && pad == 0:
		if len(rest) == 0 && ended {
			return lineDelimiter
		}
		return lineText
	case pad > formMaxPadding, len(after) > 0 && !cutCR:
		return lineMalformed
	case final:
		return lineClose
	case ended:
		return lineDelimiter
	}
	return lineText
}

// readValue reads a field's data, at most maxLine bytes.
func (fr *formReader) readValue() (string, error) {
	var v []byte
	for {
		b, err := fr.nextData(len(fr.buf))
		if errors.Is(err, io.EOF) {
			return string(v), nil
		}
		if err != nil {
			return "", err
		}
		v = append(v, b...)
		if len(v) > fr.maxLine {
			return "", errFormNoBoundary
		}
		if err := fr.overLimit(); err != nil {
			return "", err
		}
	}
}

// skipData reads and drops the current part's data, refusing more than
// maxLen bytes of it unless maxLen is negative.
func (fr *formReader) skipData(maxLen int) error {
	n := 0
	for {
		b, err := fr.nextData(len(fr.buf))
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		n += len(b)
		if maxLen >= 0 && n > maxLen {
			return errFormNoBoundary
		}
		if err := fr.overLimit(); err != nil {
			return err
		}
	}
}

// nextData returns the current part's next run of data, at most n bytes, as
// a view of the buffer that the next read reuses, or io.EOF once the part's
// delimiter is consumed.
func (fr *formReader) nextData(n int) ([]byte, error) {
	if !fr.inPart {
		return nil, io.EOF
	}
	for {
		data, delim, final, kind := fr.scan()
		if data > 0 {
			b := fr.buf[fr.r : fr.r+min(data, n)]
			fr.consume(len(b))
			fr.lineStart = false
			return b, nil
		}
		switch kind {
		case delimFound:
			fr.consume(delim)
			fr.inPart, fr.closed, fr.lineStart = false, final, true
			return nil, io.EOF
		case delimMalformed:
			return nil, errFormMalformed
		case delimNone, delimUndecided:
		}
		if err := fr.more(); err != nil {
			return nil, err
		}
	}
}

// scan finds the current part's end in the buffered bytes: data, how many of
// them are the part's whatever the body holds next; and what follows them,
// kind, with delim the delimiter's length and final when it closes the form.
// A delimiter is "--" and the boundary at a line's start,
// then what delimiterEnd takes, as RFC 2046 section 5.1.1 has it: radosgw's index_of
// takes "--" and the boundary anywhere in the data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1173-L1220 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1173-L1220 at v20.2.4),
// so a file holding that text mid-line is cut there (docs/ceph-upstream-bugs.md,
// "radosgw cuts a POST upload's file at its boundary's text mid-line").
func (fr *formReader) scan() (data, delim int, final bool, kind delimKind) {
	b := fr.buf[fr.r:fr.w]
	if fr.lineStart {
		if n, f, k := fr.delimiterAt(b, fr.dash); k != delimNone {
			return 0, n, f, k
		}
	}
	for i := 0; ; {
		j := bytes.Index(b[i:], fr.nlDash)
		if j < 0 {
			if fr.eof {
				return len(b), 0, false, delimNone
			}
			return max(i, len(b)-(len(fr.nlDash)-1)), 0, false, delimNone
		}
		p := i + j
		if n, f, k := fr.delimiterAt(b[p:], fr.nlDash); k != delimNone {
			return p, n, f, k
		}
		i = p + 1
	}
}

// delimKind is what follows a run of a part's data.
type delimKind int

const (
	delimNone      delimKind = iota // more data
	delimFound                      // a delimiter
	delimMalformed                  // a line that starts like a delimiter and is not one
	delimUndecided                  // too few bytes to tell, and the body has more
)

// delimiterAt reports what b holds when it starts with prefix: what
// delimiterEnd finds after it, with the delimiter's length and whether it
// closes the form.
func (fr *formReader) delimiterAt(b, prefix []byte) (n int, final bool, kind delimKind) {
	k := len(prefix)
	m := min(len(b), k)
	if !bytes.Equal(b[:m], prefix[:m]) {
		return 0, false, delimNone
	}
	if len(b) < k {
		if fr.eof {
			return 0, false, delimNone
		}
		return 0, false, delimUndecided
	}
	n, final, kind = delimiterEnd(b[k:], fr.eof)
	if kind == delimFound {
		n += k
	}
	return n, final, kind
}

// delimiterEnd reads what follows a delimiter's boundary, RFC 2046 section
// 5.1.1's grammar: "--" for the close delimiter, then transport padding,
// spaces and tabs, which "receivers MUST be able to handle", at most
// formMaxPadding of them, then CRLF, or for the close the body's end, a CR
// the body's end cuts off included. n is how many bytes of rest a delimiter
// takes. A boundary followed by neither "--" nor padding is data, as is a part
// delimiter the body's end cuts short, which leaves the body a delimiter
// short. A line that goes on to "--" or padding and does not complete a
// delimiter is malformed: RFC 2046 keeps the boundary delimiter out of a
// part, so no conforming composer sends one, and Go's mime/multipart refuses
// "--" and the boundary, "--" and other text. radosgw closes the form at
// "--" whatever follows it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1279-L1283 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1279-L1283 at v20.2.4)
// (docs/exclusions.md, "A POST form part ends only at a delimiter line"). radosgw
// ends the part at the boundary whatever follows it (index_of,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1201-L1216 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1201-L1216 at v20.2.4),
// and reads a padded line's rest as the next part's empty header block
// (docs/ceph-upstream-bugs.md, "radosgw reads the part after a padded POST
// form delimiter without its headers").
func delimiterEnd(rest []byte, eof bool) (n int, final bool, kind delimKind) {
	i := 0
	if len(rest) > 0 && rest[0] == '-' {
		if len(rest) < 2 {
			if eof {
				return 0, false, delimNone
			}
			return 0, false, delimUndecided
		}
		if rest[1] != '-' {
			return 0, false, delimNone
		}
		final, i = true, 2
	}
	pad := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		if pad == formMaxPadding {
			return 0, final, delimMalformed
		}
		i, pad = i+1, pad+1
	}
	switch {
	case i == len(rest) && !eof:
		return 0, final, delimUndecided
	case i == len(rest), rest[i] == '\r' && i+1 == len(rest) && eof:
		if final {
			return len(rest), true, delimFound
		}
		return 0, false, delimNone
	case rest[i] == '\r' && i+1 == len(rest):
		return 0, final, delimUndecided
	case rest[i] == '\r' && rest[i+1] == '\n':
		return i + 2, final, delimFound
	case final || pad > 0:
		return 0, final, delimMalformed
	}
	return 0, false, delimNone
}

func (fr *formReader) consume(n int) {
	fr.r += n
	fr.read += int64(n)
}

func (fr *formReader) overLimit() error {
	if fr.limit > 0 && fr.read > fr.limit {
		return errFormTooLarge
	}
	return nil
}

// more reads more of the body into the buffer, after moving its unconsumed
// bytes to the front. A body that has ended is 400 "Couldn't find boundary",
// since a part's delimiter is still to come.
func (fr *formReader) more() error {
	if fr.err != nil {
		return fr.err
	}
	if fr.eof {
		return errFormNoBoundary
	}
	if fr.r > 0 {
		fr.w = copy(fr.buf, fr.buf[fr.r:fr.w])
		fr.r = 0
	}
	if fr.w == len(fr.buf) {
		fr.err = errors.New("reading the POST form: the buffer is full")
		return fr.err
	}
	for range 100 {
		n, err := fr.body.Read(fr.buf[fr.w:])
		fr.w += n
		if errors.Is(err, io.EOF) {
			fr.eof = true
			return nil
		}
		if err != nil {
			fr.err = fmt.Errorf("reading the POST form: %w", err)
			return fr.err
		}
		if n > 0 {
			return nil
		}
	}
	fr.err = fmt.Errorf("reading the POST form: %w", io.ErrNoProgress)
	return fr.err
}

// parseFormParams is parse_boundary_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1112-L1145 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1112-L1145 at v20.2.4):
// the trimmed text before the first ";", then each ";"-separated parameter,
// its name trimmed and its value unquoted, a later one replacing an earlier
// of the same name, and one with no "=" taken with the empty value. radosgw
// ends a parameter at every ";", where a value that opens with a double quote
// here runs to its closing quote, a ";" inside it included (docs/exclusions.md,
// "A quoted POST form parameter keeps its semicolons"). Only the parameters
// keep names are kept, so a line of many parameters costs no more than the
// line: radosgw keeps every one, a map node each, for the whole request
// (docs/ceph-upstream-bugs.md, "radosgw holds every POST form field in memory
// before it authenticates the form").
func parseFormParams(s string, keep ...string) (first string, params map[string]string) {
	params = make(map[string]string, len(keep))
	put := func(name, value string) {
		if slices.Contains(keep, name) {
			params[name] = value
		}
	}
	end := paramEnd(s, 0)
	if end < 0 {
		return strings.Trim(s, cSpace), params
	}
	first = strings.Trim(s[:end], cSpace)
	for pos := end + 1; pos < len(s); {
		end := paramEnd(s, pos)
		if end < 0 {
			end = len(s)
		}
		if name, value, ok := strings.Cut(s[pos:end], "="); ok {
			put(strings.Trim(name, cSpace), trimQuotes(value))
		} else {
			put(strings.Trim(s[pos:end], cSpace), "")
		}
		pos = end + 1
	}
	return first, params
}

// paramEnd is the index of the ";" that ends the parameter starting at from,
// or -1 when none does. It looks for the "=" within the parameter alone, as
// radosgw's find('=') on the parameter's substring does, so a line's parse is
// linear: a closing quote's search runs past a ";" only once a value opens
// with a quote, and finding none it never runs again, since no quote follows.
func paramEnd(s string, from int) int {
	end := len(s)
	if k := strings.IndexByte(s[from:], ';'); k >= 0 {
		end = from + k
	}
	if eq := strings.IndexByte(s[from:end], '='); eq >= 0 {
		v := from + eq + 1
		for v < end && strings.IndexByte(cSpace, s[v]) >= 0 {
			v++
		}
		if v < end && s[v] == '"' {
			if q := strings.IndexByte(s[v+1:], '"'); q >= 0 {
				after := v + 1 + q + 1
				if k := strings.IndexByte(s[after:], ';'); k >= 0 {
					return after + k
				}
				return -1
			}
		}
	}
	if end == len(s) {
		return -1
	}
	return end
}

// trimQuotes is rgw_trim_quotes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1854-L1876 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1917-L1939 at v20.2.4):
// the trimmed value, without its double quotes when it both opens and closes
// with one.
func trimQuotes(s string) string {
	s = strings.Trim(s, cSpace)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// hasControlByte reports whether name holds a byte below 0x20 or DEL.
func hasControlByte(name string) bool {
	return strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// satAdd is a+b, or the largest int when that overflows.
func satAdd(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
