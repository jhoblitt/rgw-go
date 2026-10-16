package cors

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// ErrMalformedXML is radosgw's -ERR_MALFORMED_XML: a CORSConfiguration
// document PutBucketCors refuses.
var ErrMalformedXML = errors.New("cors: malformed xml")

// maxIDLen is the longest ID xml_end keeps.
const maxIDLen = 255

// ParseXML is RGWPutCORS_ObjStore_S3::get_params' parse of a PutBucketCors
// body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3772-L3832 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4223-L4283 at v20.2.4),
// up to the rule-count limit, which is the caller's. Every refusal is
// ErrMalformedXML.
//
// The body is read with xmltext.ParseFunc, as RGWXMLParser reads it: names
// as written, prefix included, and an element's text its own character data,
// untrimmed. RGWCORSXMLParser_S3 makes every element named CORSConfiguration
// or CORSRule, wherever it sits, the object whose xml_end checks it, and one
// xml_end that fails fails the parse
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L226-L245 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L226-L245 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L197-L205 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L197-L205 at v20.2.4),
// so each is checked where it ends. A CORSConfiguration needs a CORSRule
// child
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L177-L188 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L177-L188 at v20.2.4),
// the root must be a CORSConfiguration, and its CORSRule children are the
// rules, in document order.
func ParseXML(body []byte) (*Configuration, error) {
	rules := make(map[*xmltext.Element]Rule)
	root, err := xmltext.ParseFunc(body, func(e *xmltext.Element) error {
		switch e.Name {
		case "CORSRule":
			r, err := parseRule(e)
			if err != nil {
				return err
			}
			rules[e] = r
		case "CORSConfiguration":
			if e.FindFirst("CORSRule") == nil {
				return fmt.Errorf("%w: a CORSConfiguration has no CORSRule", ErrMalformedXML)
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, ErrMalformedXML):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrMalformedXML, err)
	case root.Name != "CORSConfiguration":
		return nil, fmt.Errorf("%w: the root element is %q, not CORSConfiguration", ErrMalformedXML, root.Name)
	}
	c := &Configuration{}
	for e := range root.Find("CORSRule") {
		c.Rules = append(c.Rules, rules[e])
	}
	return c, nil
}

// parseRule is RGWCORSRule_S3::xml_end
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L76-L164 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L76-L164 at v20.2.4),
// its checks in its order:
//   - each AllowedMethod is GET, POST, DELETE, HEAD, PUT or COPY under
//     strcasecmp, which folds ASCII letters only; a rule may name none;
//   - the first ID is at most 255 bytes;
//   - there is an AllowedOrigin, and each is a valid name;
//   - the first MaxAgeSeconds is a number as strtoull reads it, with nothing
//     after it; one of 2^32 or more is stored as MaxAgeInvalid;
//   - each ExposeHeader is kept as it is;
//   - each AllowedHeader is a valid name.
//
// A valid name, validate_name_string's, is not empty and holds at most one *
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L129-L135 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L129-L135 at v20.2.4).
func parseRule(e *xmltext.Element) (Rule, error) {
	r := Rule{MaxAge: MaxAgeInvalid}
	for m := range e.Find("AllowedMethod") {
		f, ok := methodNamed(m.Text)
		if !ok {
			return Rule{}, fmt.Errorf("%w: CORSRule: AllowedMethod %q", ErrMalformedXML, m.Text)
		}
		r.Methods |= f
	}
	if id := e.FindFirst("ID"); id != nil {
		if len(id.Text) > maxIDLen {
			return Rule{}, fmt.Errorf("%w: CORSRule: an ID of %d bytes exceeds %d", ErrMalformedXML, len(id.Text), maxIDLen)
		}
		r.ID = id.Text
	}
	var err error
	if r.AllowedOrigins, err = validNames(e, "AllowedOrigin"); err != nil {
		return Rule{}, err
	}
	if len(r.AllowedOrigins) == 0 {
		return Rule{}, fmt.Errorf("%w: CORSRule: no AllowedOrigin", ErrMalformedXML)
	}
	if m := e.FindFirst("MaxAgeSeconds"); m != nil {
		v, ok := parseMaxAge(m.Text)
		if !ok {
			return Rule{}, fmt.Errorf("%w: CORSRule: MaxAgeSeconds %q", ErrMalformedXML, m.Text)
		}
		r.MaxAge = v
	}
	for x := range e.Find("ExposeHeader") {
		r.ExposeHeaders = append(r.ExposeHeaders, x.Text)
	}
	if r.AllowedHeaders, err = validNames(e, "AllowedHeader"); err != nil {
		return Rule{}, err
	}
	r.AllowedOrigins, r.AllowedHeaders = set(r.AllowedOrigins), set(r.AllowedHeaders)
	return r, nil
}

// validNames is the text of each child of e named name, each a valid name.
func validNames(e *xmltext.Element, name string) ([]string, error) {
	var out []string
	for c := range e.Find(name) {
		if c.Text == "" || strings.Count(c.Text, "*") > 1 {
			return nil, fmt.Errorf("%w: CORSRule: %s %q", ErrMalformedXML, name, c.Text)
		}
		out = append(out, c.Text)
	}
	return out, nil
}

// methodNames are the AllowedMethod names xml_end reads, each with its flag.
var methodNames = []struct {
	name string
	flag Methods
}{
	{"GET", MethodGet},
	{"POST", MethodPost},
	{"DELETE", MethodDelete},
	{"HEAD", MethodHead},
	{"PUT", MethodPut},
	{"COPY", MethodCopy},
}

func methodNamed(s string) (Methods, bool) {
	for _, m := range methodNames {
		if equalFoldASCII(s, m.name) {
			return m.flag, true
		}
	}
	return 0, false
}

// parseMaxAge reads s as xml_end does: strtoull in base 10, which skips
// leading C whitespace, takes a sign, saturates at 2^64-1 and negates a
// negative value modulo 2^64, then end must be at the string's end. With no
// digit at all strtoull ends at the very start, which is the end only of an
// empty string, read as 0.
func parseMaxAge(s string) (uint32, bool) {
	i := 0
	for i < len(s) && isCSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	digits := i
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	if i == digits {
		return 0, s == ""
	}
	if i != len(s) {
		return 0, false
	}
	v, err := strconv.ParseUint(s[digits:i], 10, 64)
	switch {
	case err != nil:
		return MaxAgeInvalid, true
	case neg:
		v = -v
	}
	if v > math.MaxUint32 {
		return MaxAgeInvalid, true
	}
	return uint32(v), true
}

// isCSpace is isspace in the C locale.
func isCSpace(c byte) bool {
	return c == ' ' || ('\t' <= c && c <= '\r')
}

// equalFoldASCII is strcasecmp's equality in the C locale: ASCII letters
// fold, every other byte compares as it is.
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

const xmlnsS3 = "http://s3.amazonaws.com/doc/2006-03-01/"

// renderOrder is the order to_xml writes the AllowedMethods in.
var renderOrder = []struct {
	name string
	flag Methods
}{
	{"GET", MethodGet},
	{"PUT", MethodPut},
	{"DELETE", MethodDelete},
	{"HEAD", MethodHead},
	{"POST", MethodPost},
	{"COPY", MethodCopy},
}

// MarshalS3XML is the body GetBucketCors writes, RGWCORSConfiguration_S3::to_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L166-L175 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L166-L175 at v20.2.4),
// without the XML declaration the op writes before it: the CORSConfiguration
// section in the S3 namespace and each rule as RGWCORSRule_S3::to_xml writes
// it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L32-L74 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L32-L74 at v20.2.4):
// the ID when it is not empty; an AllowedMethod for each flag set, GET, PUT,
// DELETE, HEAD, POST then COPY; the origins and the headers in set order;
// MaxAgeSeconds unless it is MaxAgeInvalid; the expose headers in stored
// order. Text goes through xmltext.Escape, XMLFormatter's dump_string escape,
// and an empty section is closed with an end tag, as XMLFormatter closes one.
func (c *Configuration) MarshalS3XML() []byte {
	var b bytes.Buffer
	b.WriteString(`<CORSConfiguration xmlns="` + xmlnsS3 + `">`)
	for i := range c.Rules {
		c.Rules[i].marshal(&b)
	}
	b.WriteString("</CORSConfiguration>")
	return b.Bytes()
}

func (r *Rule) marshal(b *bytes.Buffer) {
	element := func(name, text string) {
		b.WriteString("<" + name + ">" + xmltext.Escape(text) + "</" + name + ">")
	}
	b.WriteString("<CORSRule>")
	if r.ID != "" {
		element("ID", r.ID)
	}
	for _, m := range renderOrder {
		if r.Methods&m.flag != 0 {
			element("AllowedMethod", m.name)
		}
	}
	for _, o := range set(r.AllowedOrigins) {
		element("AllowedOrigin", o)
	}
	for _, h := range set(r.AllowedHeaders) {
		element("AllowedHeader", h)
	}
	if r.MaxAge != MaxAgeInvalid {
		element("MaxAgeSeconds", strconv.FormatUint(uint64(r.MaxAge), 10))
	}
	for _, x := range r.ExposeHeaders {
		element("ExposeHeader", x)
	}
	b.WriteString("</CORSRule>")
}
