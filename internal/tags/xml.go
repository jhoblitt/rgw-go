package tags

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// ErrMalformedXML is radosgw's -ERR_MALFORMED_XML: a Tagging document
// xmltext.Parse refuses, or one that lacks an element decode_xml requires.
var ErrMalformedXML = errors.New("tags: malformed xml")

// ParseXML is RGWObjTagging_S3::decode_xml then rebuild (rgw_tag_s3.cc:14-17,
// :32-57), as the tagging ops' get_params run them (rgw_rest_s3.cc:776-815
// and :868-913 at v19.2.6, :858-897 and :950-995 at v20.2.4):
// <Tagging><TagSet><Tag><Key/><Value/></Tag>...</TagSet></Tagging>.
//
// The document is read with xmltext.Parse, the reader every S3 XML request
// body shares. It reads as radosgw's expat-based RGWXMLParser does where Go's
// decoder allows, and docs/exclusions.md records the bodies the two read
// differently. Element names are matched as written, prefix included, because
// expat runs without namespace processing, so the xmlns attribute plays no
// part. Of repeated elements only the first TagSet, Key and Value count. An
// element's text is its own character data, untrimmed, without its
// children's.
//
// get_params looks the root up as an optional field, so a root other than
// Tagging is read as the empty set. Under it, TagSet is mandatory
// (rgw_tag_s3.cc:56) and may be empty, and every Tag needs both Key and
// Value. A document xmltext.Parse refuses, or a missing element, is
// ErrMalformedXML. Every Tag is read before any is checked, so a missing
// element anywhere wins over a limit; the tags are then added in multimap
// order through Add with limit, ErrInvalidTag on the first it refuses.
func ParseXML(doc []byte, limit int) (Set, error) {
	root, err := xmltext.Parse(doc)
	if err != nil {
		return Set{}, fmt.Errorf("%w: %w", ErrMalformedXML, err)
	}
	if root.Name != "Tagging" {
		return Set{}, nil
	}
	tagSet := root.FindFirst("TagSet")
	if tagSet == nil {
		return Set{}, fmt.Errorf("%w: missing mandatory field TagSet", ErrMalformedXML)
	}
	var parsed []Tag
	for tag := range tagSet.Find("Tag") {
		key, value := tag.FindFirst("Key"), tag.FindFirst("Value")
		switch {
		case key == nil:
			return Set{}, fmt.Errorf("%w: TagSet: Tag: missing mandatory field Key", ErrMalformedXML)
		case value == nil:
			return Set{}, fmt.Errorf("%w: TagSet: Tag: missing mandatory field Value", ErrMalformedXML)
		}
		parsed = append(parsed, Tag{Key: key.Text, Value: value.Text})
	}
	multimapOrder(parsed)
	var s Set
	for _, t := range parsed {
		if err := s.Add(t.Key, t.Value, limit); err != nil {
			return Set{}, err
		}
	}
	return s, nil
}

const xmlnsS3 = "http://s3.amazonaws.com/doc/2006-03-01/"

// MarshalS3XML is the body RGWGetObjTags_ObjStore_S3::send_response_data and
// RGWGetBucketTags_ObjStore_S3::send_response_data write (rgw_rest_s3.cc:
// 754-772 and :847-865 at v19.2.6, :837-855 and :929-947 at v20.2.4) without
// the XML declaration the op writes before it: the Tagging section in the S3
// namespace, the TagSet section, and one Tag per tag in multimap order
// (RGWObjTagSet_S3::dump_xml, rgw_tag_s3.cc:59-65). Key and Value go through
// xmltext.Escape, because encode_xml writes a string with the formatter's
// dump_string (rgw_xml.cc:448-451). XMLFormatter closes a section with an end
// tag, never as an empty element, so the empty set is <TagSet></TagSet>.
func (s Set) MarshalS3XML() []byte {
	var b bytes.Buffer
	b.WriteString(`<Tagging xmlns="` + xmlnsS3 + `"><TagSet>`)
	for _, t := range s.ordered() {
		b.WriteString("<Tag><Key>")
		b.WriteString(xmltext.Escape(t.Key))
		b.WriteString("</Key><Value>")
		b.WriteString(xmltext.Escape(t.Value))
		b.WriteString("</Value></Tag>")
	}
	b.WriteString("</TagSet></Tagging>")
	return b.Bytes()
}
