package s3

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// checksumField is the field lookup a write op reads its checksum request
// through (op.ChecksumInput.Field): r's headers as radosgw's environment
// holds them (op.HeaderValue), looked up when the op asks, so that what auth
// folds into them counts. radosgw reads the checksum fields from the
// environment alone
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L65-L136 at v20.2.4),
// and an aws-chunked payload's trailers reach the op as op.Request.Trailers.
func checksumField(r *op.Request) func(name string) (string, bool) {
	return func(name string) (string, bool) { return op.HeaderValue(r.Header, name) }
}

// setChecksumHeaders is the checksum a PutObject or an UploadPart answers
// with, as RGWPutObj_ObjStore_S3::send_response sends it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2914-L2917 at v20.2.4):
// for a type AWS defines, x-amz-checksum-type FULL_OBJECT, whatever a part's
// upload flags say, and the armored value under the type's header; nothing
// for no checksum. rgw-go names the types as AWS does, so a SHA512 checksum
// is answered as x-amz-checksum-sha512, where v20.2.4, whose table counts
// sha512 as no AWS type, sends neither header (docs/differences.md, "Writes
// answer a SHA512 checksum under AWS's name"). Squid's radosgw sends none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2738-L2791 at v19.2.7),
// and rgw-go answers on Squid the checksum it stores there.
func setChecksumHeaders(h http.Header, c *cksum.Cksum) {
	if c == nil || !c.Type.Info().AWS {
		return
	}
	SetHeader(h, "x-amz-checksum-type", cksum.FullObjectType)
	SetHeader(h, c.Type.Info().Header, c.Armor())
}

// copyPartChecksum is the checksum of a copied part as CopyPartResult carries
// it, the Checksum<ALG> element of a type AWS defines, its armor bare, as
// Amazon S3 answers an UploadPartCopy, which carries no checksum header;
// nil for no checksum. radosgw sends x-amz-checksum-type and the value header
// for it instead, and no element
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2918-L2940 at v20.2.4;
// docs/differences.md, "UploadPartCopy and CreateMultipartUpload answer their
// checksums as Amazon S3 does").
func copyPartChecksum(c *cksum.Cksum) *checksumElement {
	if c == nil || !c.Type.Info().AWS {
		return nil
	}
	return &checksumElement{name: c.Type.Info().Element, value: xmltext.Text(c.Armor())}
}

// setUploadChecksum is a created upload's checksum, for an upload that has a
// type: x-amz-checksum-algorithm, the type upper-cased, as
// RGWInitMultipart_ObjStore_S3::send_response sends it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4540-L4543 at v20.2.4),
// and x-amz-checksum-type, which Amazon S3 sends beside it and radosgw does
// not (docs/differences.md, "UploadPartCopy and CreateMultipartUpload answer
// their checksums as Amazon S3 does"): COMPOSITE for an upload flagged
// COMPOSITE, else FULL_OBJECT. An upload holds only a pair
// permitted_cksum_algo_and_type permits (op.ChecksumForCreate), so one
// without the flag is a CRC's, which its completion combines into a
// full-object CRC (cksum.NewCombiner). A Squid upload has no type, and
// Squid's radosgw sends neither header
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4059-L4085 at v19.2.7).
func setUploadChecksum(h http.Header, uc op.UploadChecksum) {
	if uc.Type == cksum.None {
		return
	}
	SetHeader(h, "x-amz-checksum-algorithm", strings.ToUpper(uc.Type.Info().Name))
	typ := cksum.FullObjectType
	if uc.Flags&cksum.FlagComposite != 0 {
		typ = cksum.CompositeType
	}
	SetHeader(h, "x-amz-checksum-type", typ)
}

// checksumElement is a checksum's element in an XML document, named for its
// type, Checksum<ALG>, as element_name names it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L189-L192 at v20.2.4).
type checksumElement struct {
	name  string
	value xmltext.Text
}

// MarshalXML writes the element under its own name, whatever the field's.
func (e checksumElement) MarshalXML(enc *xml.Encoder, _ xml.StartElement) error {
	return e.value.MarshalXML(enc, xml.StartElement{Name: xml.Name{Local: e.name}})
}

// completeChecksum is the checksum CompleteMultipartUploadResult carries
// after its ETag on Tentacle, for c, the completion's combined checksum or
// the one a retried completion's head stores, of an object of parts parts
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4635-L4640 at v20.2.4):
// the armored value, with "-<parts>" when the record is composite, as
// try_sum_part_cksums and check_previously_completed armor it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7145-L7151 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7494-L7497 at v20.2.4),
// and ChecksumType, get_checksum_type's for a multipart object. A record of
// type none, which holds no digest, gets neither, as rgw-go's reads show none
// (docs/differences.md, "A stored checksum rgw-go cannot read whole is not
// shown"), and nor does any completion on Squid, whose radosgw renders no
// checksum
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4106-L4138 at v19.2.7).
func completeChecksum(r *op.Request, c *cksum.Cksum, parts int) (*checksumElement, xmltext.Text) {
	if c == nil || c.Type == cksum.None || r.Env.Zone.Release() < denc.Tentacle {
		return nil, ""
	}
	value := c.Armor()
	if c.Composite() {
		value += "-" + strconv.Itoa(parts)
	}
	return &checksumElement{name: c.Type.Info().Element, value: xmltext.Text(value)}, xmltext.Text(c.ObjectType(true))
}
