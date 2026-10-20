package acl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// PublicAccessBlock is PublicAccessBlockConfiguration
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_public_access.h#L21-L66),
// which radosgw stores in a bucket's user.rgw.public-access attr.
type PublicAccessBlock struct {
	BlockPublicACLs       bool
	IgnorePublicACLs      bool
	BlockPublicPolicy     bool
	RestrictPublicBuckets bool
}

// Encode mirrors PublicAccessBlockConfiguration::encode, ENCODE_START(1, 1).
func (b PublicAccessBlock) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Bool(b.BlockPublicACLs)
	e.Bool(b.IgnorePublicACLs)
	e.Bool(b.BlockPublicPolicy)
	e.Bool(b.RestrictPublicBuckets)
	e.EndStruct(f)
}

// DecodePublicAccessBlock mirrors PublicAccessBlockConfiguration::decode,
// DECODE_START(1).
func DecodePublicAccessBlock(d *denc.Decoder) PublicAccessBlock {
	h := d.BeginStruct(1)
	b := PublicAccessBlock{
		BlockPublicACLs:       d.Bool(),
		IgnorePublicACLs:      d.Bool(),
		BlockPublicPolicy:     d.Bool(),
		RestrictPublicBuckets: d.Bool(),
	}
	d.EndStruct(h)
	return b
}

// MaxPublicAccessBlockXML is the longest body ParsePublicAccessBlockXML
// reads: 1 MiB, rgw_max_put_param_size's default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151 at v20.2.4),
// the limit RGWPutBucketPublicAccessBlock::get_params reads the body under
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8626-L8631 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9594-L9599 at v20.2.4).
// It holds when an operator raises the option, so a parse costs at most that.
const MaxPublicAccessBlockXML = 1 << 20

// ErrMalformedXML is radosgw's -ERR_MALFORMED_XML, 400 MalformedXML: a
// PutPublicAccessBlock body ParsePublicAccessBlockXML refuses.
var ErrMalformedXML = errors.New("acl: malformed xml")

// blockRoot is the element PutPublicAccessBlock looks up as mandatory.
const blockRoot = "PublicAccessBlockConfiguration"

// xmlSpace is XML's S production.
const xmlSpace = " \t\r\n"

// utf8BOM is the byte order mark xmltext.Parse skips, as expat does.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// blockField is one of the block's booleans under its element name.
type blockField struct {
	name string
	v    *bool
}

// blockFields are b's booleans in dump_xml's order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L11-L20 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L11-L20 at v20.2.4).
func blockFields(b *PublicAccessBlock) [4]blockField {
	return [4]blockField{
		{"BlockPublicAcls", &b.BlockPublicACLs},
		{"IgnorePublicAcls", &b.IgnorePublicACLs},
		{"BlockPublicPolicy", &b.BlockPublicPolicy},
		{"RestrictPublicBuckets", &b.RestrictPublicBuckets},
	}
}

// ParsePublicAccessBlockXML reads a PutPublicAccessBlock body. radosgw
// parses it with expat and refuses a document expat refuses, or one without
// a PublicAccessBlockConfiguration root, as MalformedXML
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8646-L8658 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9614-L9626 at v20.2.4).
// Each of the four booleans is optional and false when absent
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L4-L9 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L4-L9 at v20.2.4).
//
// Past that, radosgw reads the body leniently where Amazon S3 holds a
// client to its schema, and a block is a security setting a lenient read can
// drop. radosgw ignores an unknown element, so a misspelled or prefixed
// boolean is stored false, and reads only the first of a repeated one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L218-L241 at v20.2.4);
// it ignores attributes, namespaces among them, and text between the
// elements, and expands the entities a DTD declares. It reads a boolean as
// true or false in any ASCII case, else as a decimal integer with whitespace
// around it, non-zero for true
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L392-L406 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L392-L406 at v20.2.4).
// rgw-go answers MalformedXML instead for each of these: a document type
// declaration or any other declaration; a root other than
// PublicAccessBlockConfiguration, or a second one; an element other than the
// four booleans under it, nested in one, or repeated; a prefixed name, even
// one bound to S3's namespace; an attribute other than xmlns naming S3's
// namespace or none; text other than XML whitespace outside a boolean; a
// boolean xmlBool does not read; a boolean whose text, once decoded, is
// longer than maxBoolText bytes, which radosgw reads when it is 1 or 0
// inside whitespace; and a body longer than MaxPublicAccessBlockXML. Names
// are read as written, as radosgw reads them.
//
// The body is refused at the first token that breaks one of those rules, so
// a hostile document costs no more than its offending prefix, and only then
// is it read with xmltext.Parse, the reader every S3 XML request body
// shares, which refuses what expat refuses. Both read the same tokens, so a
// boolean's text, its character data with references and CDATA decoded, is
// the same to each. Every refusal is ErrMalformedXML; one the scan makes
// names the byte offset of the token it refused. Client-chosen text in a
// refusal is cut short, so a refusal's message stays small.
func ParsePublicAccessBlockXML(body []byte) (PublicAccessBlock, error) {
	if len(body) > MaxPublicAccessBlockXML {
		return PublicAccessBlock{}, fmt.Errorf("%w: a %d-byte body exceeds %d bytes", ErrMalformedXML, len(body), MaxPublicAccessBlockXML)
	}
	b, err := scanPublicAccessBlock(body)
	if err != nil {
		return PublicAccessBlock{}, err
	}
	if _, err := xmltext.Parse(body); err != nil {
		return PublicAccessBlock{}, fmt.Errorf("%w: %.128s", ErrMalformedXML, err.Error())
	}
	return b, nil
}

// scanPublicAccessBlock reads body's tokens and refuses the first that
// breaks ParsePublicAccessBlockXML's rules. A root holds only the four
// booleans, and a boolean only text, so nothing is read past depth two.
func scanPublicAccessBlock(body []byte) (PublicAccessBlock, error) {
	doc := bytes.TrimPrefix(body, utf8BOM)
	base := int64(len(body) - len(doc))
	var b PublicAccessBlock
	fields := blockFields(&b)
	var (
		seen   [len(fields)]bool
		depth  int
		cur    int
		text   []byte
		closed bool
	)
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		off := base + d.InputOffset()
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PublicAccessBlock{}, malformedAt(off, "%.128s", err.Error())
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case depth == 0 && closed:
				return PublicAccessBlock{}, malformedAt(off, "a second root %.64q", writtenName(t.Name))
			case depth == 0 && t.Name != (xml.Name{Local: blockRoot}):
				return PublicAccessBlock{}, malformedAt(off, "the root is %.64q, not %s", writtenName(t.Name), blockRoot)
			case depth == 1:
				cur = fieldNamed(fields, t.Name)
				switch {
				case cur < 0:
					return PublicAccessBlock{}, malformedAt(off, "an unknown element %.64q", writtenName(t.Name))
				case seen[cur]:
					return PublicAccessBlock{}, malformedAt(off, "a repeated %s", fields[cur].name)
				}
				seen[cur] = true
				text = text[:0]
			case depth > 1:
				return PublicAccessBlock{}, malformedAt(off, "an element %.64q inside %s", writtenName(t.Name), fields[cur].name)
			}
			if a, ok := foreignAttr(t.Attr); ok {
				return PublicAccessBlock{}, malformedAt(off, "an attribute %.64q=%.64q on %.64q", writtenName(a.Name), a.Value, writtenName(t.Name))
			}
			depth++
		case xml.EndElement:
			depth--
			switch depth {
			case -1:
				return PublicAccessBlock{}, malformedAt(off, "an end tag with no start tag")
			case 0:
				closed = true
			case 1:
				v, ok := xmlBool(text)
				if !ok {
					return PublicAccessBlock{}, malformedAt(off, "%s is %q, not a boolean", fields[cur].name, text)
				}
				*fields[cur].v = v
			}
		case xml.CharData:
			switch {
			case depth == 2:
				if len(text)+len(t) > maxBoolText {
					return PublicAccessBlock{}, malformedAt(off, "%s holds more than %d bytes", fields[cur].name, maxBoolText)
				}
				text = append(text, t...)
			case len(bytes.Trim(t, xmlSpace)) > 0:
				return PublicAccessBlock{}, malformedAt(off, "text outside a boolean")
			}
		case xml.Directive:
			return PublicAccessBlock{}, malformedAt(off, "a declaration %.32q", t)
		}
	}
	return b, nil
}

func malformedAt(off int64, format string, args ...any) error {
	return fmt.Errorf("%w: %s at byte %d", ErrMalformedXML, fmt.Sprintf(format, args...), off)
}

// fieldNamed is the index of the boolean named n, unprefixed, or -1.
func fieldNamed(fields [4]blockField, n xml.Name) int {
	for i, f := range fields {
		if n == (xml.Name{Local: f.name}) {
			return i
		}
	}
	return -1
}

// foreignAttr is the first attribute that is not a default namespace
// declaration of S3's namespace or of none.
func foreignAttr(attrs []xml.Attr) (xml.Attr, bool) {
	for _, a := range attrs {
		if a.Name != (xml.Name{Local: "xmlns"}) || (a.Value != "" && a.Value != s3XMLNS) {
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

// maxBoolText is the most text a boolean may hold: room for 1 or 0 with
// whitespace around it, and a bound on what the scan keeps.
const maxBoolText = 64

// xmlBool reads the boolean forms radosgw reads that every reference gives
// one meaning: true or false in any ASCII case, as strncasecmp compares them
// and as AWS's reference writes TRUE; and 1 or 0 with XML whitespace around
// it, as xs:boolean collapses it and strtol skips it. radosgw refuses
// whitespace around true or false too. The other integers its strtol
// fallback reads, such as 2, -7, +0 or 01, have no boolean meaning in any
// schema.
func xmlBool(text []byte) (v, ok bool) {
	switch {
	case equalFoldASCII(string(text), "true"):
		return true, true
	case equalFoldASCII(string(text), "false"):
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

// MarshalS3XML is the body GetPublicAccessBlock writes, dump_xml's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L11-L20 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L11-L20 at v20.2.4),
// without the XML declaration the op writes before it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5124-L5135 at v20.2.4):
// the PublicAccessBlockConfiguration section with no namespace, and each
// boolean in dump_bool's lower-case true or false
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/Formatter.h#L97-L100 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/Formatter.h#L213-L216 at v20.2.4).
func (b PublicAccessBlock) MarshalS3XML() []byte {
	var buf bytes.Buffer
	buf.WriteString("<" + blockRoot + ">")
	for _, f := range blockFields(&b) {
		buf.WriteString("<" + f.name + ">" + strconv.FormatBool(*f.v) + "</" + f.name + ">")
	}
	buf.WriteString("</" + blockRoot + ">")
	return buf.Bytes()
}
