package sse

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// MaxConfigXML is the longest body ParseXML reads: 1 MiB,
// rgw_max_put_param_size's default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151 at v20.2.4),
// the limit RGWPutBucketEncryption::get_params reads the body under
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8757-L8762 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9725-L9730 at v20.2.4).
// It holds when an operator raises the option, so a parse costs at most that.
const MaxConfigXML = 1 << 20

// ErrMalformedXML is radosgw's -ERR_MALFORMED_XML, 400 MalformedXML: a
// PutBucketEncryption body ParseXML refuses, or a configuration Validate
// refuses.
var ErrMalformedXML = errors.New("sse: malformed xml")

const xmlnsS3 = "http://s3.amazonaws.com/doc/2006-03-01/"

// xmlSpace is XML's S production.
const xmlSpace = " \t\r\n"

// utf8BOM is the byte order mark xmltext.Parse skips, as expat does.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// node is an element of the document's schema.
type node int

const (
	nodeRoot node = iota
	nodeRule
	nodeApply
	nodeBucketKey
	nodeAlgorithm
	nodeKeyID
	nodeCount
)

// schema is each element's name, the element it sits in, and whether it
// holds a value rather than elements. Each appears at most once, as its
// container does.
var schema = [nodeCount]struct {
	name   string
	parent node
	value  bool
}{
	nodeRoot:      {"ServerSideEncryptionConfiguration", nodeRoot, false},
	nodeRule:      {"Rule", nodeRoot, false},
	nodeApply:     {"ApplyServerSideEncryptionByDefault", nodeRule, false},
	nodeBucketKey: {"BucketKeyEnabled", nodeRule, true},
	nodeAlgorithm: {"SSEAlgorithm", nodeApply, true},
	nodeKeyID:     {"KMSMasterKeyID", nodeApply, true},
}

// maxBoolText is the most text BucketKeyEnabled may hold: room for 1 or 0
// with whitespace around it, and a bound on what the scan keeps.
const maxBoolText = 64

// ParseXML reads a PutBucketEncryption body. radosgw parses it with expat and
// refuses a document expat refuses, or one without a
// ServerSideEncryptionConfiguration root, as MalformedXML
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8774-L8796 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9742-L9764 at v20.2.4).
// Under the root, Rule is optional, and so are, in Rule,
// ApplyServerSideEncryptionByDefault and BucketKeyEnabled, and, in that
// default, SSEAlgorithm and KMSMasterKeyID; each absent one takes its default,
// an empty string or false
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L8-L34 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.cc#L8-L34 at v20.2.4).
// A value is its element's character data, references and CDATA decoded,
// comments and processing instructions dropped, nothing trimmed.
//
// Past that, radosgw reads the body leniently where Amazon S3 holds a client
// to its schema. It ignores an element it does not know, so a misspelled
// SSEAlgorithm or Rule, or AWS's BlockedEncryptionTypes, leaves the
// configuration without it while the request answers 200, and of a repeated
// element it reads the first
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L218-L241 at v20.2.4);
// it ignores attributes, namespaces among them, and text between the
// elements, and expands the entities a DTD declares. It reads a boolean as
// true or false in any ASCII case, else as a decimal integer with
// whitespace around it, non-zero for true
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L392-L406 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L392-L406 at v20.2.4).
// rgw-go answers MalformedXML instead for each of these: a document type
// declaration or any other declaration; a root other than
// ServerSideEncryptionConfiguration, or a second one; an element the schema
// does not place where it stands, nested in a value, or repeated; a
// prefixed name, even one bound to S3's namespace; an attribute other than
// xmlns naming S3's namespace or none; text other than XML whitespace
// outside a value; a BucketKeyEnabled xmlBool does not read, or whose text,
// once decoded, is longer than maxBoolText bytes; and a body longer than
// MaxConfigXML. Names are read as written, as radosgw reads them.
//
// The body is refused at the first token that breaks one of those rules, so
// a hostile document costs no more than its offending prefix, and only then
// is it read with xmltext.Parse, the reader every S3 XML request body
// shares, which refuses what expat refuses. Both read the same tokens, so a
// value is the same to each. Last, the configuration must pass Validate,
// which radosgw does not check. Every refusal is ErrMalformedXML; one the
// scan makes names the byte offset of the token it refused. Client-chosen
// text in a refusal is cut short, so a refusal's message stays small.
func ParseXML(body []byte) (BucketEncryptionConfig, error) {
	if len(body) > MaxConfigXML {
		return BucketEncryptionConfig{}, fmt.Errorf("%w: a %d-byte body exceeds %d bytes", ErrMalformedXML, len(body), MaxConfigXML)
	}
	c, err := scan(body)
	if err != nil {
		return BucketEncryptionConfig{}, err
	}
	if _, err := xmltext.Parse(body); err != nil {
		return BucketEncryptionConfig{}, fmt.Errorf("%w: %s", ErrMalformedXML, clip(err.Error(), 128))
	}
	if err := c.Validate(); err != nil {
		return BucketEncryptionConfig{}, err
	}
	return c, nil
}

// scan reads body's tokens and refuses the first that breaks ParseXML's
// rules. Every element the schema allows is at most four deep, and a value
// holds only text, so nothing deeper is read.
func scan(body []byte) (BucketEncryptionConfig, error) {
	doc := bytes.TrimPrefix(body, utf8BOM)
	base := int64(len(body) - len(doc))
	var (
		c    BucketEncryptionConfig
		seen [nodeCount]bool
		open []node
		text []byte
	)
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		off := base + d.InputOffset()
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return c, nil
		}
		if err != nil {
			return BucketEncryptionConfig{}, malformedAt(off, "%s", clip(err.Error(), 128))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n, err := place(open, &seen, t.Name)
			if err != nil {
				return BucketEncryptionConfig{}, malformedAt(off, "%w", err)
			}
			if a, ok := foreignAttr(t.Attr); ok {
				return BucketEncryptionConfig{}, malformedAt(off, "an attribute %s on %s", quote(writtenName(a.Name)), quote(writtenName(t.Name)))
			}
			seen[n] = true
			open = append(open, n)
			text = text[:0]
			if n == nodeRule {
				c.Rule = &ServerSideEncryptionConfiguration{}
			}
		case xml.EndElement:
			if len(open) == 0 {
				return BucketEncryptionConfig{}, malformedAt(off, "an end tag with no start tag")
			}
			n := open[len(open)-1]
			open = open[:len(open)-1]
			if t.Name != (xml.Name{Local: schema[n].name}) {
				return BucketEncryptionConfig{}, malformedAt(off, "an end tag %s closing %s", quote(writtenName(t.Name)), schema[n].name)
			}
			if schema[n].value {
				if err := setValue(&c, n, text); err != nil {
					return BucketEncryptionConfig{}, malformedAt(off, "%w", err)
				}
			}
		case xml.CharData:
			switch {
			case len(open) > 0 && schema[open[len(open)-1]].value:
				if n := open[len(open)-1]; n == nodeBucketKey && len(text)+len(t) > maxBoolText {
					return BucketEncryptionConfig{}, malformedAt(off, "%s holds more than %d bytes", schema[n].name, maxBoolText)
				}
				text = append(text, t...)
			case len(bytes.Trim(t, xmlSpace)) > 0:
				return BucketEncryptionConfig{}, malformedAt(off, "text outside a value")
			}
		case xml.Directive:
			return BucketEncryptionConfig{}, malformedAt(off, "a declaration %s", quote(string(t)))
		}
	}
}

// place is the schema element a start tag named n opens inside open, or why
// it may not stand there.
func place(open []node, seen *[nodeCount]bool, n xml.Name) (node, error) {
	if len(open) == 0 {
		switch {
		case seen[nodeRoot]:
			return 0, fmt.Errorf("a second root %s", quote(writtenName(n)))
		case n != (xml.Name{Local: schema[nodeRoot].name}):
			return 0, fmt.Errorf("the root is %s, not %s", quote(writtenName(n)), schema[nodeRoot].name)
		}
		return nodeRoot, nil
	}
	parent := open[len(open)-1]
	if schema[parent].value {
		return 0, fmt.Errorf("an element %s inside %s", quote(writtenName(n)), schema[parent].name)
	}
	for c := nodeRule; c < nodeCount; c++ {
		if schema[c].parent != parent || n != (xml.Name{Local: schema[c].name}) {
			continue
		}
		if seen[c] {
			return 0, fmt.Errorf("a repeated %s", schema[c].name)
		}
		return c, nil
	}
	return 0, fmt.Errorf("an unknown element %s in %s", quote(writtenName(n)), schema[parent].name)
}

// setValue stores a value element's text in c, whose Rule the schema has
// opened before any value.
func setValue(c *BucketEncryptionConfig, n node, text []byte) error {
	switch n {
	case nodeAlgorithm:
		c.Rule.Apply.SSEAlgorithm = string(text)
	case nodeKeyID:
		c.Rule.Apply.KMSMasterKeyID = string(text)
	case nodeBucketKey:
		v, ok := xmlBool(text)
		if !ok {
			return fmt.Errorf("%s is %s, not a boolean", schema[n].name, quote(string(text)))
		}
		c.Rule.BucketKeyEnabled = v
	}
	return nil
}

func malformedAt(off int64, format string, args ...any) error {
	return fmt.Errorf("%w: %w at byte %d", ErrMalformedXML, fmt.Errorf(format, args...), off)
}

// foreignAttr is the first attribute that is not a default namespace
// declaration of S3's namespace or of none.
func foreignAttr(attrs []xml.Attr) (xml.Attr, bool) {
	for _, a := range attrs {
		if a.Name != (xml.Name{Local: "xmlns"}) || (a.Value != "" && a.Value != xmlnsS3) {
			return a, true
		}
	}
	return xml.Attr{}, false
}

// writtenName is a name as the document writes it, prefix included.
func writtenName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// quote is s, cut by clip to 32 bytes, quoted.
func quote(s string) string { return strconv.Quote(clip(s, 32)) }

// clip is s cut at a rune boundary to at most n bytes, marked when cut, so a
// refusal that repeats a client's text stays small.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// xmlBool reads the boolean forms radosgw reads that every reference gives
// one meaning: true or false in any ASCII case, as strncasecmp compares them;
// and 1 or 0 with XML whitespace around it, as xs:boolean collapses it and
// strtol skips it. radosgw refuses whitespace around true or false too. The
// other integers its strtol fallback reads, such as 2, -7, +0 or 01, have no
// boolean meaning in any schema.
func xmlBool(text []byte) (v, ok bool) {
	switch {
	case equalFoldASCII(text, "true"):
		return true, true
	case equalFoldASCII(text, "false"):
		return false, true
	}
	switch string(bytes.Trim(text, xmlSpace)) {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

// equalFoldASCII reports whether b is s with ASCII letters folded.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if lowerASCII(b[i]) != lowerASCII(s[i]) {
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

// algorithms are the SSEAlgorithm values Amazon S3 takes, each marked when
// it is one of the two that take a KMSMasterKeyID
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionByDefault.html).
var algorithms = map[string]bool{
	"AES256":       false,
	"aws:kms":      true,
	"aws:kms:dsse": true,
	"aws:fsx":      false,
	"aws:backup":   false,
}

// Validate refuses, as MalformedXML, a configuration Amazon S3 does not
// take: an SSEAlgorithm outside AWS's list, and a KMSMasterKeyID with an
// algorithm other than aws:kms or aws:kms:dsse. Values are compared as
// written. A rule naming no algorithm, which radosgw stores for an empty
// Rule, passes, and so does a configuration without a rule. Neither floor
// checks either value: PutBucketEncryption stores what it decoded
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8790-L8811 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9758-L9779 at v20.2.4).
func (c BucketEncryptionConfig) Validate() error {
	if c.Rule == nil {
		return nil
	}
	alg, keyID := c.Rule.Apply.SSEAlgorithm, c.Rule.Apply.KMSMasterKeyID
	takesKey, known := algorithms[alg]
	if alg != "" && !known {
		return fmt.Errorf("%w: SSEAlgorithm %s is not AES256, aws:kms, aws:kms:dsse, aws:fsx or aws:backup", ErrMalformedXML, quote(alg))
	}
	if keyID != "" && !takesKey {
		return fmt.Errorf("%w: a KMSMasterKeyID with SSEAlgorithm %s, not aws:kms or aws:kms:dsse", ErrMalformedXML, quote(alg))
	}
	return nil
}

// MarshalS3XML is the body GetBucketEncryption writes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3886-L3904 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4337-L4355 at v20.2.4)
// without the XML declaration the op writes before it: the
// ServerSideEncryptionConfiguration section in the S3 namespace, then, when
// there is a rule, dump_xml's Rule: its default, whose SSEAlgorithm is written
// even when empty and whose KMSMasterKeyID only when set, then
// BucketKeyEnabled only when true
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L13-L40 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.cc#L13-L40 at v20.2.4).
// Values go through xmltext.Escape, as encode_xml writes a string with the
// formatter's dump_string, and XMLFormatter closes every section with an end
// tag. radosgw writes the bucket key's true as True, encode_xml's form for a
// bool
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L463-L472 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L463-L472 at v20.2.4),
// which botocore reads as false; rgw-go writes true, as AWS does
// (docs/exclusions.md, "GetBucketEncryption writes BucketKeyEnabled as
// true").
func (c BucketEncryptionConfig) MarshalS3XML() []byte {
	var b bytes.Buffer
	b.WriteString(`<ServerSideEncryptionConfiguration xmlns="` + xmlnsS3 + `">`)
	if r := c.Rule; r != nil {
		b.WriteString("<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>")
		b.WriteString(xmltext.Escape(r.Apply.SSEAlgorithm))
		b.WriteString("</SSEAlgorithm>")
		if r.Apply.KMSMasterKeyID != "" {
			b.WriteString("<KMSMasterKeyID>")
			b.WriteString(xmltext.Escape(r.Apply.KMSMasterKeyID))
			b.WriteString("</KMSMasterKeyID>")
		}
		b.WriteString("</ApplyServerSideEncryptionByDefault>")
		if r.BucketKeyEnabled {
			b.WriteString("<BucketKeyEnabled>true</BucketKeyEnabled>")
		}
		b.WriteString("</Rule>")
	}
	b.WriteString("</ServerSideEncryptionConfiguration>")
	return b.Bytes()
}
