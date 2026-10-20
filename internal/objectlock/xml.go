package objectlock

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/iso8601"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// The retention modes radosgw takes, in a rule and on an object.
const (
	modeGovernance = "GOVERNANCE"
	modeCompliance = "COMPLIANCE"
)

// The legal-hold statuses radosgw takes; is_enabled is status == "ON"
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L155-L157 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L139-L141 at v20.2.4).
const (
	holdOn  = "ON"
	holdOff = "OFF"
)

// cSpaces is what isspace takes in the C locale.
const cSpaces = " \t\n\v\f\r"

// xmlErr is an RGWXMLDecoder::err, whose text each enclosing decode_xml
// prefixes with its element's name as it rethrows
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241 at v19.2.7 and v20.2.4).
type xmlErr struct{ what string }

func (e *xmlErr) in(name string) *xmlErr { return &xmlErr{name + ": " + e.what} }

func missing(name string) *xmlErr { return &xmlErr{"missing mandatory field " + name} }

func (e *xmlErr) malformed() error { return &Error{Err: ErrMalformedXML, Detail: e.what} }

// field is the text of e's first child named name, the element's own
// character data as decode_xml_obj takes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L179-L182 at v19.2.7 and v20.2.4),
// and whether there is one.
func field(e *xmltext.Element, name string) (string, bool) {
	c := e.FindFirst(name)
	if c == nil {
		return "", false
	}
	return c.Text, true
}

// root parses body and returns its root, which must be name:
// RGWXMLDecoder::decode_xml(name, ..., &parser, true) looks it up among the
// parser's top-level elements, by its name as written, namespaces off.
func root(body []byte, name string) (*xmltext.Element, error) {
	r, err := xmltext.Parse(body)
	if err != nil {
		return nil, &Error{Err: ErrMalformedXML, Detail: fmt.Sprintf("%.128s", err.Error())}
	}
	if r.Name != name {
		return nil, missing(name).malformed()
	}
	return r, nil
}

// decodeInt is decode_xml_obj(int&)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L308 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L366-L377 at v19.2.7 and v20.2.4):
// strtol in base 10 over the text as a C string, which skips leading C white
// space and takes a sign, then nothing but C white space, within long's range
// and then int's.
func decodeInt(s string) (int32, *xmlErr) {
	s = rgwtext.CString(s)
	v, end, erange := rgwtext.Strtoll(s)
	if erange || end == 0 || strings.TrimLeft(s[end:], cSpaces) != "" {
		return 0, &xmlErr{"failed to parse number"}
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, &xmlErr{"integer out of range"}
	}
	return int32(v), nil
}

// intField is decode_xml(name, int&, e): the first child's number, 0 and
// false when there is none.
func intField(e *xmltext.Element, name string) (v int32, present bool, err *xmlErr) {
	s, ok := field(e, name)
	if !ok {
		return 0, false, nil
	}
	if v, err = decodeInt(s); err != nil {
		return 0, true, err.in(name)
	}
	return v, true, nil
}

// ParseConfiguration is PutObjectLockConfiguration's reading of its body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8253-L8274 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9188-L9209 at v20.2.4)
// with RGWObjectLock::decode_xml and its parts
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L14-L91 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L8-L75 at v20.2.4).
// The root is ObjectLockConfiguration, holding ObjectLockEnabled, whose text
// is exactly "Enabled", and an optional Rule. A Rule holds a DefaultRetention,
// with a Mode of GOVERNANCE or COMPLIANCE and exactly one of Days and Years
// present, each a number decode_xml_obj(int) reads; one present as 0 counts,
// which RetentionPeriodValid then refuses. Of repeated elements the first is
// read, and elements, attributes and text it does not look for are ignored.
// Every refusal is an *Error of ErrMalformedXML.
func ParseConfiguration(body []byte) (meta.ObjectLock, error) {
	r, err := root(body, "ObjectLockConfiguration")
	if err != nil {
		return meta.ObjectLock{}, err
	}
	l, xerr := decodeObjectLock(r)
	if xerr != nil {
		return meta.ObjectLock{}, xerr.in(r.Name).malformed()
	}
	return l, nil
}

func decodeObjectLock(e *xmltext.Element) (meta.ObjectLock, *xmlErr) {
	s, ok := field(e, "ObjectLockEnabled")
	if !ok {
		return meta.ObjectLock{}, missing("ObjectLockEnabled")
	}
	if s != "Enabled" {
		return meta.ObjectLock{}, &xmlErr{"invalid ObjectLockEnabled value"}
	}
	l := meta.ObjectLock{Enabled: true}
	if c := e.FindFirst("Rule"); c != nil {
		dr := c.FindFirst("DefaultRetention")
		if dr == nil {
			return meta.ObjectLock{}, missing("DefaultRetention").in("Rule")
		}
		r, err := decodeDefaultRetention(dr)
		if err != nil {
			return meta.ObjectLock{}, err.in("DefaultRetention").in("Rule")
		}
		l.Rule = &meta.ObjectLockRule{DefaultRetention: r}
	}
	return l, nil
}

func decodeDefaultRetention(e *xmltext.Element) (meta.DefaultRetention, *xmlErr) {
	mode, ok := field(e, "Mode")
	if !ok {
		return meta.DefaultRetention{}, missing("Mode")
	}
	if mode != modeGovernance && mode != modeCompliance {
		return meta.DefaultRetention{}, &xmlErr{"bad Mode in lock rule"}
	}
	days, hasDays, err := intField(e, "Days")
	if err != nil {
		return meta.DefaultRetention{}, err
	}
	years, hasYears, err := intField(e, "Years")
	if err != nil {
		return meta.DefaultRetention{}, err
	}
	if hasDays == hasYears {
		return meta.DefaultRetention{}, &xmlErr{"either Days or Years must be specified, but not both"}
	}
	return meta.DefaultRetention{Mode: mode, Days: days, Years: years}, nil
}

// ParseRetention is PutObjectRetention's reading of its body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8366-L8385 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9301-L9320 at v20.2.4)
// with RGWObjectRetention::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L124-L136 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L108-L120 at v20.2.4):
// a Retention root holding a Mode of GOVERNANCE or COMPLIANCE and a
// RetainUntilDate iso8601.Parse reads, which refuses a date real_time cannot
// hold where radosgw wraps it. Every refusal is an *Error of ErrMalformedXML.
func ParseRetention(body []byte) (meta.ObjectRetention, error) {
	r, err := root(body, "Retention")
	if err != nil {
		return meta.ObjectRetention{}, err
	}
	mode, ok := field(r, "Mode")
	if !ok {
		return meta.ObjectRetention{}, missing("Mode").in(r.Name).malformed()
	}
	if mode != modeGovernance && mode != modeCompliance {
		return meta.ObjectRetention{}, (&xmlErr{"bad Mode in retention"}).in(r.Name).malformed()
	}
	date, ok := field(r, "RetainUntilDate")
	if !ok {
		return meta.ObjectRetention{}, missing("RetainUntilDate").in(r.Name).malformed()
	}
	until, ok := iso8601.Parse(date)
	if !ok {
		return meta.ObjectRetention{}, (&xmlErr{"invalid RetainUntilDate value"}).in(r.Name).malformed()
	}
	return meta.ObjectRetention{Mode: mode, RetainUntil: until}, nil
}

// ParseLegalHold is PutObjectLegalHold's reading of its body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8511-L8533 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9459-L9481 at v20.2.4)
// with RGWObjectLegalHold::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L144-L149 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L128-L133 at v20.2.4):
// a LegalHold root holding a Status of ON or OFF. Every refusal is an *Error
// of ErrMalformedXML.
func ParseLegalHold(body []byte) (meta.ObjectLegalHold, error) {
	r, err := root(body, "LegalHold")
	if err != nil {
		return meta.ObjectLegalHold{}, err
	}
	status, ok := field(r, "Status")
	if !ok {
		return meta.ObjectLegalHold{}, missing("Status").in(r.Name).malformed()
	}
	if status != holdOn && status != holdOff {
		return meta.ObjectLegalHold{}, (&xmlErr{"bad status in legal hold"}).in(r.Name).malformed()
	}
	return meta.ObjectLegalHold{Status: status}, nil
}

// element writes name holding text, escaped as XMLFormatter's dump_string
// escapes it.
func element(b *strings.Builder, name, text string) {
	b.WriteString("<" + name + ">" + xmltext.Escape(text) + "</" + name + ">")
}

// MarshalConfiguration is GetObjectLockConfiguration's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4504-L4518 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5015-L5029 at v20.2.4)
// without the XML declaration the op writes before it: RGWObjectLock::dump_xml
// in an ObjectLockConfiguration with no namespace, ObjectLockEnabled when the
// lock is enabled, and the rule when there is one, its Days when they are
// positive and its Years otherwise
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L35-L42 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L84-L91 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L29-L36 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L68-L75 at v20.2.4).
// A section is closed with an end tag, as XMLFormatter closes one.
func MarshalConfiguration(l meta.ObjectLock) []byte {
	var b strings.Builder
	b.WriteString("<ObjectLockConfiguration>")
	if l.Enabled {
		element(&b, "ObjectLockEnabled", "Enabled")
	}
	if l.Rule != nil {
		r := l.Rule.DefaultRetention
		b.WriteString("<Rule><DefaultRetention>")
		element(&b, "Mode", r.Mode)
		if r.Days > 0 {
			b.WriteString("<Days>" + strconv.Itoa(int(r.Days)) + "</Days>")
		} else {
			b.WriteString("<Years>" + strconv.Itoa(int(r.Years)) + "</Years>")
		}
		b.WriteString("</DefaultRetention></Rule>")
	}
	b.WriteString("</ObjectLockConfiguration>")
	return []byte(b.String())
}

// MarshalRetention is GetObjectRetention's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4543-L4557 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5054-L5068 at v20.2.4)
// without the XML declaration: RGWObjectRetention::dump_xml in a Retention
// with no namespace, the date as iso8601.Format writes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L138-L142 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L122-L126 at v20.2.4).
func MarshalRetention(r meta.ObjectRetention) []byte {
	var b strings.Builder
	b.WriteString("<Retention>")
	element(&b, "Mode", r.Mode)
	element(&b, "RetainUntilDate", iso8601.Format(r.RetainUntil))
	b.WriteString("</Retention>")
	return []byte(b.String())
}

// MarshalLegalHold is GetObjectLegalHold's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4568-L4582 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5079-L5093 at v20.2.4)
// without the XML declaration: RGWObjectLegalHold::dump_xml in a LegalHold
// with no namespace
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L151-L153 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L135-L137 at v20.2.4).
func MarshalLegalHold(h meta.ObjectLegalHold) []byte {
	var b strings.Builder
	b.WriteString("<LegalHold>")
	element(&b, "Status", h.Status)
	b.WriteString("</LegalHold>")
	return []byte(b.String())
}
