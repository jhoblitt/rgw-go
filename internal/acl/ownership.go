package acl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// Ownership is S3 Object Ownership, v21's rgw::s3::ObjectOwnership
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.h#L29-L33 at v21.1.2):
// whether a bucket's ACLs apply and who owns an object written to it. Neither
// floor, v19.2.7 nor v20.2.4, has the setting, so it is stored as v21 stores
// it, the form a zone's radosgw reads once upgraded to v21.
type Ownership uint8

// The three settings, in v21's order, which their stored byte follows.
const (
	// BucketOwnerEnforced disables ACLs: the bucket owner owns every object.
	BucketOwnerEnforced Ownership = iota
	// BucketOwnerPreferred gives the bucket owner an object written with the
	// bucket-owner-full-control canned ACL.
	BucketOwnerPreferred
	// ObjectWriter leaves an object to its writer, as radosgw did before the
	// setting existed; it is a bucket's setting when it has none stored.
	ObjectWriter
)

// AttrOwnershipControls is RGW_ATTR_OWNERSHIP_CONTROLS
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_common.h#L125 at v21.1.2),
// the bucket attr holding its Ownership.
const AttrOwnershipControls = meta.AttrPrefix + "ownership-controls"

// String is to_string
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L22-L34 at v21.1.2):
// the setting's name, or invalid for a byte outside the three, which
// DecodeOwnership keeps as stored.
func (o Ownership) String() string {
	switch o {
	case BucketOwnerEnforced:
		return "BucketOwnerEnforced"
	case BucketOwnerPreferred:
		return "BucketOwnerPreferred"
	case ObjectWriter:
		return "ObjectWriter"
	}
	return "invalid"
}

// Encode mirrors encode(const OwnershipControls&): the setting's byte inside
// ENCODE_START(1, 1)
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L77-L82 at v21.1.2).
func (o Ownership) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(uint8(o))
	e.EndStruct(f)
}

// DecodeOwnership mirrors decode(OwnershipControls&), DECODE_START(1)
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L84-L89 at v21.1.2).
// The byte is kept as stored, as the enum's denc copies it unchecked
// (https://github.com/ceph/ceph/blob/v21.1.2/src/include/denc.h#L306-L333 at v21.1.2).
// When d records a failure it is ObjectWriter, OwnershipControls' default,
// so a caller that misses the failure reads what a bucket without the
// setting has, never BucketOwnerEnforced.
func DecodeOwnership(d *denc.Decoder) Ownership {
	h := d.BeginStruct(1)
	o := Ownership(d.U8())
	d.EndStruct(h)
	if d.Err() != nil {
		return ObjectWriter
	}
	return o
}

// OwnershipOf is get_object_ownership
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L91-L106 at v21.1.2):
// the Ownership a bucket's attrs hold, and ObjectWriter when they hold none or
// one that does not decode. Bytes after the encoding are not looked at, as
// decode does not look at them. A byte outside the three is ObjectWriter too:
// v21 returns it as stored, and each of its readers tests only for
// BucketOwnerEnforced or BucketOwnerPreferred, so it acts as ObjectWriter
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl_s3.cc#L678-L706,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_common.cc#L1631,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L6868 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_bucket.cc#L148-L149 at v21.1.2).
func OwnershipOf(attrs map[string][]byte) Ownership {
	b, ok := attrs[AttrOwnershipControls]
	if !ok {
		return ObjectWriter
	}
	o := DecodeOwnership(denc.NewDecoder(b))
	if o > ObjectWriter {
		return ObjectWriter
	}
	return o
}

// MaxOwnershipXML is the longest body ParseOwnershipXML reads: 1 MiB,
// rgw_max_put_param_size's default
// (https://github.com/ceph/ceph/blob/v21.1.2/src/common/options/rgw.yaml.in#L164-L171 at v21.1.2),
// the limit PutBucketOwnershipControls reads the body under
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4555-L4557 at v21.1.2).
// It holds when an operator raises the option, so a parse costs at most that.
const MaxOwnershipXML = 1 << 20

// The messages v21 sets in s->err.message for a body it refuses
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4570-L4574 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L36-L69 at v21.1.2).
const (
	msgNoOwnershipControls = "Missing required element OwnershipControls"
	msgNoRule              = "Missing required element Rule"
	msgNoObjectOwnership   = "Missing required element ObjectOwnership"
	msgBadObjectOwnership  = "ObjectOwnership must be one of BucketOwnerEnforced | BucketOwnerPreferred | ObjectWriter"
)

// ownershipPath is the document's one chain of elements, OwnershipControls
// holding Rule holding ObjectOwnership, each at the depth of its index.
var ownershipPath = [...]string{"OwnershipControls", "Rule", "ObjectOwnership"}

// ParseOwnershipXML reads a PutBucketOwnershipControls body as v21 does
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4553-L4579 at v21.1.2):
// it parses the body with expat and refuses one expat refuses; then it looks
// up the OwnershipControls root, its first Rule and that Rule's first
// ObjectOwnership, whose text, untrimmed, ParseOwnership must read
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L56-L69 at v21.1.2).
// Every refusal is a *ParseError whose Err wraps ErrMalformedXML, all of
// which radosgw answers 400 MalformedXML. Message is v21's text for a missing
// root, Rule or ObjectOwnership, or for another name. It is empty where v21
// sets none, and also where the scan below refuses the body first, even when
// v21 would refuse it with a message: an unknown element standing where Rule
// or ObjectOwnership belongs carries none. The Ownership returned with a
// refusal is ObjectWriter.
//
// Past that, v21 reads the body leniently: it ignores an element it does not
// look up, attributes, namespaces among them, and text between the elements;
// it reads the first of a repeated Rule or ObjectOwnership; and it expands
// the entities a document type declaration declares. rgw-go refuses instead,
// with no Message: a document type declaration or any other declaration; under
// the root, an element other than Rule, under Rule one other than
// ObjectOwnership, and any element inside ObjectOwnership; a repeated Rule or
// ObjectOwnership; a prefixed name, even one bound to S3's namespace; an
// attribute other than xmlns naming S3's namespace or none; text other than
// XML whitespace outside ObjectOwnership; and a body longer than
// MaxOwnershipXML. Names are read as written, as radosgw reads them.
// docs/exclusions.md, "A PutBucketOwnershipControls document is held to S3's
// schema", lists each case.
//
// The body is refused at the first token that breaks one of those rules, so
// a hostile document costs no more than its offending prefix, and only then
// is it read with xmltext.Parse, the reader every S3 XML request body shares,
// which refuses what expat refuses. The missing elements and the name are
// judged last, as v21 judges them once expat has read the whole body, so a
// body that is not well-formed carries no message. The one exception is a
// root other than an unprefixed OwnershipControls: the scan refuses it at its
// start tag with v21's message, without reading the rest, so the message is
// sent once Go's decoder has read that tag, even when expat would refuse the
// body. A malformation the decoder meets first, before or on that tag, is
// refused with no message, as v21 refuses it. Client-chosen text in a refusal
// is cut short by bytes, so a refusal's text stays small however its runes
// quote.
func ParseOwnershipXML(body []byte) (Ownership, error) {
	if len(body) > MaxOwnershipXML {
		return ObjectWriter, &ParseError{Err: fmt.Errorf("%w: a %d-byte body exceeds %d bytes", ErrMalformedXML, len(body), MaxOwnershipXML)}
	}
	if err := scanOwnership(body); err != nil {
		return ObjectWriter, err
	}
	root, err := xmltext.Parse(body)
	if err != nil {
		return ObjectWriter, &ParseError{Err: fmt.Errorf("%w: %s", ErrMalformedXML, clip(err.Error(), 128))}
	}
	rule := root.FindFirst(ownershipPath[1])
	if rule == nil {
		return ObjectWriter, &ParseError{Message: msgNoRule, Err: ErrMalformedXML}
	}
	value := rule.FindFirst(ownershipPath[2])
	if value == nil {
		return ObjectWriter, &ParseError{Message: msgNoObjectOwnership, Err: ErrMalformedXML}
	}
	o, err := ParseOwnership(value.Text)
	if err != nil {
		return ObjectWriter, &ParseError{Message: msgBadObjectOwnership, Err: fmt.Errorf("%w: ObjectOwnership is %s", ErrMalformedXML, quote(value.Text))}
	}
	return o, nil
}

// ParseOwnership is parse
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L36-L54 at v21.1.2):
// the setting whose name is s exactly. Any other s is a *ParseError with
// v21's message whose Err wraps ErrInvalid, radosgw's -EINVAL, which
// CreateBucket answers 400 InvalidArgument for its x-amz-object-ownership
// header
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L2809-L2816 at v21.1.2);
// the Ownership returned with it is ObjectWriter. ParseOwnershipXML refuses
// the same name in a body as MalformedXML, with the same message.
func ParseOwnership(s string) (Ownership, error) {
	for o := range ObjectWriter + 1 {
		if s == o.String() {
			return o, nil
		}
	}
	return ObjectWriter, &ParseError{Message: msgBadObjectOwnership, Err: fmt.Errorf("%w: ObjectOwnership is %s", ErrInvalid, quote(s))}
}

// scanOwnership reads body's tokens and refuses the first that breaks
// ParseOwnershipXML's rules. Each element the document may hold stands at
// the depth of its place in ownershipPath, and ObjectOwnership holds only
// text, so nothing is read past depth three.
func scanOwnership(body []byte) error {
	doc := bytes.TrimPrefix(body, utf8BOM)
	base := int64(len(body) - len(doc))
	var (
		seen   [len(ownershipPath)]bool
		depth  int
		closed bool
	)
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		off := base + d.InputOffset()
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return &ParseError{Err: malformedAt(off, "%s", clip(err.Error(), 128))}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := quote(writtenName(t.Name))
			switch {
			case depth == 0 && closed:
				return &ParseError{Err: malformedAt(off, "a second root %s", name)}
			case depth == len(ownershipPath):
				return &ParseError{Err: malformedAt(off, "an element %s inside %s", name, ownershipPath[depth-1])}
			case t.Name != (xml.Name{Local: ownershipPath[depth]}) && depth == 0:
				return &ParseError{
					Message: msgNoOwnershipControls,
					Err:     malformedAt(off, "the root is %s, not %s", name, ownershipPath[0]),
				}
			case t.Name != (xml.Name{Local: ownershipPath[depth]}):
				return &ParseError{Err: malformedAt(off, "an unknown element %s in %s", name, ownershipPath[depth-1])}
			case seen[depth]:
				return &ParseError{Err: malformedAt(off, "a repeated %s", ownershipPath[depth])}
			}
			if a, ok := foreignAttr(t.Attr); ok {
				return &ParseError{Err: malformedAt(off, "an attribute %s=%s on %s", quote(writtenName(a.Name)), quote(a.Value), ownershipPath[depth])}
			}
			seen[depth] = true
			depth++
		case xml.EndElement:
			depth--
			switch depth {
			case -1:
				return &ParseError{Err: malformedAt(off, "an end tag with no start tag")}
			case 0:
				closed = true
			}
		case xml.CharData:
			if depth < len(ownershipPath) && len(bytes.Trim(t, xmlSpace)) > 0 {
				return &ParseError{Err: malformedAt(off, "text outside ObjectOwnership")}
			}
		case xml.Directive:
			return &ParseError{Err: malformedAt(off, "a declaration %s", quote(string(t)))}
		}
	}
}

// quote is s, cut by clip to 32 bytes, quoted. strconv.Quote writes a byte
// as up to four, so a quoted name is at most 133 bytes.
func quote(s string) string { return strconv.Quote(clip(s, 32)) }

// clip is s cut at a rune boundary to at most n bytes, marked when cut, so a
// refusal that repeats a client's text stays small. Cutting by runes would
// not bound it: a non-printable rune quotes as up to ten bytes.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// MarshalS3XML is the body GetBucketOwnershipControls writes
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4590-L4607 at v21.1.2)
// without the XML declaration the op writes before it: the OwnershipControls
// section in the S3 namespace, then dump_xml's Rule holding the setting's
// name, invalid for a byte outside the three
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L71-L75 at v21.1.2).
func (o Ownership) MarshalS3XML() []byte {
	return []byte(`<OwnershipControls xmlns="` + s3XMLNS + `"><Rule><ObjectOwnership>` +
		o.String() + `</ObjectOwnership></Rule></OwnershipControls>`)
}
