package website

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var (
	// ErrMalformedXML is 400 MalformedXML: a PutBucketWebsite body that does
	// not parse, has no WebsiteConfiguration root, or does not fit S3's
	// schema. radosgw answers InvalidArgument for the first two and reads the
	// third; docs/exclusions.md, "PutBucketWebsite answers MalformedXML for a
	// document that does not parse or fit S3's schema", lists each case.
	ErrMalformedXML = errors.New("website: malformed xml")
	// ErrInvalidArgument is radosgw's -EINVAL, 400 InvalidArgument.
	ErrInvalidArgument = errors.New("website: invalid argument")
	// ErrTooManyRules is radosgw's -ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR,
	// 400 InvalidRequest
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L83 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L84 at v20.2.4).
	ErrTooManyRules = errors.New("website: too many routing rules")
)

// ParseError is a refusal of a PutBucketWebsite body. Err is
// ErrMalformedXML, ErrInvalidArgument or ErrTooManyRules; Message is the text
// radosgw sets in s->err.message, empty where it sets none, and never holds
// the body's text; Detail says what was refused, any text of the body in it
// cut short.
type ParseError struct {
	Err     error
	Message string
	Detail  string
}

// Error returns Err's text, then Message and Detail where they are set.
func (e *ParseError) Error() string {
	s := e.Err.Error()
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Unwrap returns Err.
func (e *ParseError) Unwrap() error { return e.Err }

func malformed(format string, args ...any) *ParseError {
	return &ParseError{Err: ErrMalformedXML, Detail: fmt.Sprintf(format, args...)}
}

// MaxXML is the longest body ParseXML reads: 1 MiB, rgw_max_put_param_size's
// default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151 at v20.2.4),
// the limit get_params reads the body under. It holds when an operator raises
// the option, so a parse costs at most that.
const MaxXML = 1 << 20

// The messages get_params sets in s->err.message
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2312-L2342 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2407-L2437 at v20.2.4).
const (
	msgNoHost    = `A host name must be provided to redirect all requests (e.g. "example.com").`
	msgNoIndex   = "A value for IndexDocument Suffix must be provided if RedirectAllRequestsTo is empty"
	msgBadSuffix = "The IndexDocument Suffix is not well formed"
	msgTooMany   = "%d routing rules provided, the number of routing rules in a website configuration is limited to %d."
)

// defaultMaxRules is WEBSITE_ROUTING_RULES_MAX_NUM, the limit get_params
// takes for a negative rgw_website_routing_rules_max_num.
const defaultMaxRules = 50

// The bounds RGWBWRedirectInfo::decode_xml and
// RGWBWRoutingRuleCondition::decode_xml hold the codes to, inclusive
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L233-L275 at v19.2.7 and v20.2.4).
const (
	minRedirectCode = 301
	maxRedirectCode = 399
	minErrorCode    = 400
	maxErrorCode    = 599
)

const (
	rootName = "WebsiteConfiguration"
	xmlnsS3  = "http://s3.amazonaws.com/doc/2006-03-01/"
)

// ParseXML is RGWSetBucketWebsite_ObjStore_S3::get_params' reading of a
// PutBucketWebsite body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2279-L2345 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2374-L2440 at v20.2.4)
// and RGWBucketWebsiteConf::decode_xml with the decode_xml of its parts
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L233-L341 at v19.2.7 and v20.2.4),
// holding the document to S3's schema. maxRules is
// rgw_website_routing_rules_max_num, 50 when negative. Every refusal is a
// *ParseError.
//
// The configuration is RedirectAllRequestsTo, its HostName and optional
// Protocol; or else an IndexDocument whose Suffix is not empty, an optional
// ErrorDocument's Key, and the RoutingRule children of RoutingRules in
// document order, each with an optional Condition and a Redirect. Text is an
// element's own character data, untrimmed, as decode_xml_obj takes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L179-L182 at v19.2.7 and v20.2.4).
//
// The answers are, in this order:
//   - MalformedXML, Amazon S3's code for configuration XML that does not
//     conform to its schema (aws-sdk-go v1.55.8 service/s3/api.go:20217-20221),
//     for a body longer than MaxXML; then, before any tree is built, for what
//     scan refuses: a declaration, whose internal subset can declare entities
//     that expat, as RGWXMLParser sets it up, expands
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L213-L223 at v19.2.7 and v20.2.4);
//     a root other than WebsiteConfiguration; an element the schema does not
//     hold where it stands, a field holding one among them;
//     a second of an element the schema allows once, where
//     RGWXMLDecoder::decode_xml reads the first
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241 at v19.2.7 and v20.2.4);
//     an element name with a prefix, which radosgw, with namespace processing
//     off (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158 at v19.2.7 and v20.2.4),
//     reads as another name; an attribute other than xmlns naming S3's
//     namespace; text other than XML whitespace between elements; and what
//     S3's model asks of a whole element (frame.end). Then for what
//     xmltext.Parse refuses, as expat refuses it, a second root among them.
//     radosgw answers
//     InvalidArgument for a body that does not parse or has another root, and
//     reads the rest.
//   - InvalidArgument with no message, as radosgw's caught decode throws
//     carry none: a RedirectAllRequestsTo without a HostName; an
//     HttpRedirectCode outside 301-399 or an HttpErrorCodeReturnedEquals
//     outside 400-599, each read as strtol reads a decimal;
//     ReplaceKeyPrefixWith beside ReplaceKeyWith, even empty; and, where
//     radosgw refuses nothing, a HostName holding a byte at or below 0x20,
//     or 0x7F, which Apply would decline.
//   - InvalidArgument with radosgw's message: a redirect-all HostName that is
//     empty, no IndexDocument, and a Suffix that is absent or empty.
//   - ErrTooManyRules with radosgw's message, for more than maxRules rules.
func ParseXML(body []byte, maxRules int) (meta.WebsiteConf, error) {
	if len(body) > MaxXML {
		return meta.WebsiteConf{}, malformed("a %d-byte body exceeds %d bytes", len(body), MaxXML)
	}
	if err := scan(body); err != nil {
		return meta.WebsiteConf{}, err
	}
	root, err := xmltext.Parse(body)
	if err != nil {
		return meta.WebsiteConf{}, malformed("%.128s", err.Error())
	}
	conf, err := decode(root)
	if err != nil {
		return meta.WebsiteConf{}, err
	}
	if maxRules < 0 {
		maxRules = defaultMaxRules
	}
	if n := len(conf.RoutingRules); n > maxRules {
		return meta.WebsiteConf{}, &ParseError{Err: ErrTooManyRules, Message: fmt.Sprintf(msgTooMany, n, maxRules)}
	}
	return conf, nil
}

// decoder reads a WebsiteConfiguration tree that scan admitted, keeping the
// first InvalidArgument refusal it meets.
type decoder struct {
	invalid *ParseError
}

// invalidArgument keeps a decode throw, which get_params answers -EINVAL
// without a message.
func (d *decoder) invalidArgument(format string, args ...any) {
	if d.invalid == nil {
		d.invalid = &ParseError{Err: ErrInvalidArgument, Detail: fmt.Sprintf(format, args...)}
	}
}

// field is the text of e's child named name, and whether there is one.
func field(e *xmltext.Element, name string) (string, bool) {
	c := e.FindFirst(name)
	if c == nil {
		return "", false
	}
	return c.Text, true
}

// decode is RGWBucketWebsiteConf::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L323-L341 at v19.2.7 and v20.2.4),
// then get_params' three messages in their order.
func decode(root *xmltext.Element) (meta.WebsiteConf, error) {
	var d decoder
	var conf meta.WebsiteConf
	if o := root.FindFirst("RedirectAllRequestsTo"); o != nil {
		host, ok := field(o, "HostName")
		if !ok {
			d.invalidArgument("RedirectAllRequestsTo has no HostName")
		}
		d.host(host)
		protocol, _ := field(o, "Protocol")
		conf.RedirectAll = meta.RedirectInfo{Hostname: host, Protocol: protocol}
		switch {
		case d.invalid != nil:
			return meta.WebsiteConf{}, d.invalid
		case host == "":
			return meta.WebsiteConf{}, &ParseError{Err: ErrInvalidArgument, Message: msgNoHost}
		}
		return conf, nil
	}
	index := root.FindFirst("IndexDocument")
	if index != nil {
		conf.IndexDocSuffix, _ = field(index, "Suffix")
	}
	if o := root.FindFirst("ErrorDocument"); o != nil {
		conf.ErrorDoc, _ = field(o, "Key")
	}
	if o := root.FindFirst("RoutingRules"); o != nil {
		for r := range o.Find("RoutingRule") {
			conf.RoutingRules = append(conf.RoutingRules, d.rule(r))
		}
	}
	switch {
	case d.invalid != nil:
		return meta.WebsiteConf{}, d.invalid
	case index == nil:
		return meta.WebsiteConf{}, &ParseError{Err: ErrInvalidArgument, Message: msgNoIndex}
	case conf.IndexDocSuffix == "":
		return meta.WebsiteConf{}, &ParseError{Err: ErrInvalidArgument, Message: msgBadSuffix}
	}
	return conf, nil
}

// rule is RGWBWRoutingRule::decode_xml with RGWBWRoutingRuleCondition's and
// RGWBWRedirectInfo's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L233-L286 at v19.2.7 and v20.2.4).
func (d *decoder) rule(e *xmltext.Element) meta.RoutingRule {
	var r meta.RoutingRule
	if c := e.FindFirst("Condition"); c != nil {
		r.Condition.KeyPrefixEquals, _ = field(c, "KeyPrefixEquals")
		if s, ok := field(c, "HttpErrorCodeReturnedEquals"); ok {
			r.Condition.HTTPErrorCodeReturnedEquals = d.code("HttpErrorCodeReturnedEquals", s, minErrorCode, maxErrorCode)
		}
	}
	if c := e.FindFirst("Redirect"); c != nil {
		ri := &r.RedirectInfo
		ri.Redirect.Protocol, _ = field(c, "Protocol")
		ri.Redirect.Hostname, _ = field(c, "HostName")
		d.host(ri.Redirect.Hostname)
		if s, ok := field(c, "HttpRedirectCode"); ok {
			ri.Redirect.HTTPRedirectCode = d.code("HttpRedirectCode", s, minRedirectCode, maxRedirectCode)
		}
		var hasPrefix, hasKey bool
		ri.ReplaceKeyPrefixWith, hasPrefix = field(c, "ReplaceKeyPrefixWith")
		ri.ReplaceKeyWith, hasKey = field(c, "ReplaceKeyWith")
		if hasPrefix && hasKey {
			d.invalidArgument("ReplaceKeyPrefixWith beside ReplaceKeyWith")
		}
	}
	return r
}

// host refuses a HostName Apply would decline, one holding a byte at or below
// 0x20, or 0x7F, which radosgw stores and writes into the Location raw.
// S3's model types a HostName as a string, so this is a semantic refusal,
// InvalidArgument, not MalformedXML.
func (d *decoder) host(h string) {
	if !hostSafe(h) {
		d.invalidArgument("the HostName %.64q holds a byte at or below 0x20, or 0x7F", h)
	}
}

// code is s read as decode_xml_obj(int&) reads it, which must lie in
// [lo, hi]; outside it, or when it is no number, the throw is kept.
func (d *decoder) code(name, s string, lo, hi int) uint16 {
	n, ok := strtolInt(s)
	if !ok || n < lo || n > hi {
		d.invalidArgument("%s %.64q is not %d to %d", name, s, lo, hi)
		return 0
	}
	return uint16(n) //nolint:gosec // lo and hi lie within u16
}

// strtolInt is decode_xml_obj(int&)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L308 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L366-L377 at v19.2.7 and v20.2.4):
// strtol in base 10, which skips leading C whitespace and takes a sign, then
// nothing but C whitespace, and an int's range. ok is false where it throws.
func strtolInt(s string) (int, bool) {
	i := 0
	for i < len(s) && isCSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var v int64
	for ; i < len(s) && '0' <= s[i] && s[i] <= '9'; i++ {
		if v <= math.MaxInt32+1 {
			v = v*10 + int64(s[i]-'0')
		}
	}
	if i == start {
		return 0, false
	}
	for ; i < len(s); i++ {
		if !isCSpace(s[i]) {
			return 0, false
		}
	}
	if neg {
		v = -v
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

// isCSpace is isspace in the C locale.
func isCSpace(c byte) bool {
	return c == ' ' || ('\t' <= c && c <= '\r')
}

// MarshalS3XML is the body GetBucketWebsite writes
// (RGWGetBucketWebsite_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2365-L2383 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2460-L2478 at v20.2.4)
// without the XML declaration the op writes before it: WebsiteConfiguration
// in S3's namespace holding RGWBucketWebsiteConf::dump_xml's sections, each
// only when set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L214-L316 at v19.2.7 and v20.2.4).
// RedirectAllRequestsTo, when it names a host, holds HostName, then Protocol
// when set; IndexDocument holds Suffix, ErrorDocument Key; and each routing
// rule holds a Condition and a Redirect, even empty, with the fields that are
// set, a Redirect's Protocol before its HostName. A rule with no condition
// therefore renders an empty Condition, which ParseXML reads back as none.
// Text goes through xmltext.Escape, XMLFormatter's dump_string
// escape, codes are decimal, and a section is closed with an end tag, as
// XMLFormatter closes one.
func MarshalS3XML(conf meta.WebsiteConf) []byte {
	var b bytes.Buffer
	text := func(name, s string) {
		b.WriteString("<" + name + ">" + xmltext.Escape(s) + "</" + name + ">")
	}
	code := func(name string, c uint16) {
		b.WriteString("<" + name + ">" + strconv.Itoa(int(c)) + "</" + name + ">")
	}
	b.WriteString(`<WebsiteConfiguration xmlns="` + xmlnsS3 + `">`)
	if ra := conf.RedirectAll; ra.Hostname != "" {
		b.WriteString("<RedirectAllRequestsTo>")
		text("HostName", ra.Hostname)
		if ra.Protocol != "" {
			text("Protocol", ra.Protocol)
		}
		b.WriteString("</RedirectAllRequestsTo>")
	}
	if conf.IndexDocSuffix != "" {
		b.WriteString("<IndexDocument>")
		text("Suffix", conf.IndexDocSuffix)
		b.WriteString("</IndexDocument>")
	}
	if conf.ErrorDoc != "" {
		b.WriteString("<ErrorDocument>")
		text("Key", conf.ErrorDoc)
		b.WriteString("</ErrorDocument>")
	}
	if len(conf.RoutingRules) > 0 {
		b.WriteString("<RoutingRules>")
		for _, r := range conf.RoutingRules {
			b.WriteString("<RoutingRule><Condition>")
			if c := r.Condition; c.KeyPrefixEquals != "" {
				text("KeyPrefixEquals", c.KeyPrefixEquals)
			}
			if c := r.Condition.HTTPErrorCodeReturnedEquals; c > 0 {
				code("HttpErrorCodeReturnedEquals", c)
			}
			b.WriteString("</Condition><Redirect>")
			ri := r.RedirectInfo
			if ri.Redirect.Protocol != "" {
				text("Protocol", ri.Redirect.Protocol)
			}
			if ri.Redirect.Hostname != "" {
				text("HostName", ri.Redirect.Hostname)
			}
			if ri.Redirect.HTTPRedirectCode > 0 {
				code("HttpRedirectCode", ri.Redirect.HTTPRedirectCode)
			}
			if ri.ReplaceKeyPrefixWith != "" {
				text("ReplaceKeyPrefixWith", ri.ReplaceKeyPrefixWith)
			}
			if ri.ReplaceKeyWith != "" {
				text("ReplaceKeyWith", ri.ReplaceKeyWith)
			}
			b.WriteString("</Redirect></RoutingRule>")
		}
		b.WriteString("</RoutingRules>")
	}
	b.WriteString("</WebsiteConfiguration>")
	return b.Bytes()
}
