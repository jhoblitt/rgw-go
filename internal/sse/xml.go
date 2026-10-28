package sse

import (
	"bytes"
	"errors"
	"fmt"

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

// The document's elements.
const (
	elemRoot      = "ServerSideEncryptionConfiguration"
	elemRule      = "Rule"
	elemApply     = "ApplyServerSideEncryptionByDefault"
	elemBucketKey = "BucketKeyEnabled"
	elemAlgorithm = "SSEAlgorithm"
	elemKeyID     = "KMSMasterKeyID"
)

// schema is the document's table: the root holding Rule; Rule holding the
// default, ApplyServerSideEncryptionByDefault, and BucketKeyEnabled, a
// value; the default holding the values SSEAlgorithm and KMSMasterKeyID.
// Each appears at most once, as its container does, in S3's namespace or
// none.
var schema = &xmltext.Schema{
	Root: elemRoot,
	Children: map[string][]xmltext.Child{
		elemRoot:  {{Name: elemRule}},
		elemRule:  {{Name: elemApply}, {Name: elemBucketKey, MaxText: maxBoolText}},
		elemApply: {{Name: elemAlgorithm}, {Name: elemKeyID}},
	},
	Namespaces: []string{xmlnsS3, ""},
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
// The body is read with xmltext.Scan against schema, and no tree is built:
// the scan refuses it at the first token that breaks one of those rules, or
// that xmltext.Parse, the reader every S3 XML request body shares, would
// refuse as expat does, so a hostile document costs no more than its
// offending prefix. It reads each value as its element ends, as Parse reads
// it. Last, the configuration must pass Validate, which radosgw does not
// check. Every refusal is ErrMalformedXML; one the scan makes names the byte
// offset of the token it refused. Client-chosen text in a refusal is cut by
// bytes before it is quoted, so a refusal's message stays small however its
// runes quote.
func ParseXML(body []byte) (BucketEncryptionConfig, error) {
	if len(body) > MaxConfigXML {
		return BucketEncryptionConfig{}, fmt.Errorf("%w: a %d-byte body exceeds %d bytes", ErrMalformedXML, len(body), MaxConfigXML)
	}
	var c BucketEncryptionConfig
	if err := xmltext.Scan(body, schema, func(e xmltext.Closed) error { return setValue(&c, e) }); err != nil {
		return BucketEncryptionConfig{}, fmt.Errorf("%w: %w", ErrMalformedXML, err)
	}
	if err := c.Validate(); err != nil {
		return BucketEncryptionConfig{}, err
	}
	return c, nil
}

// setValue stores in c the element e that has ended: a value's text, and
// for Rule a rule, opened by the first of its parts to end or else by Rule's
// own end, so an empty Rule stores a rule naming nothing.
func setValue(c *BucketEncryptionConfig, e xmltext.Closed) error {
	rule := func() *ServerSideEncryptionConfiguration {
		if c.Rule == nil {
			c.Rule = &ServerSideEncryptionConfiguration{}
		}
		return c.Rule
	}
	switch e.Name {
	case elemRule:
		rule()
	case elemAlgorithm:
		rule().Apply.SSEAlgorithm = string(e.Text)
	case elemKeyID:
		rule().Apply.KMSMasterKeyID = string(e.Text)
	case elemBucketKey:
		v, ok := xmlBool(e.Text)
		if !ok {
			return fmt.Errorf("%s is %s, not a boolean", e.Name, xmltext.Quote(string(e.Text)))
		}
		rule().BucketKeyEnabled = v
	}
	return nil
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
		return fmt.Errorf("%w: SSEAlgorithm %s is not AES256, aws:kms, aws:kms:dsse, aws:fsx or aws:backup", ErrMalformedXML, xmltext.Quote(alg))
	}
	if keyID != "" && !takesKey {
		return fmt.Errorf("%w: a KMSMasterKeyID with SSEAlgorithm %s, not aws:kms or aws:kms:dsse", ErrMalformedXML, xmltext.Quote(alg))
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
// (docs/differences.md, "GetBucketEncryption writes BucketKeyEnabled as
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
