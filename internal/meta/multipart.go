package meta

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Multipart object names, RGWMPObj (services/svc_tier_rados.h) and
// MP_META_SUFFIX (services/svc_tier_rados.cc:8), identical at v19.2.6 and
// v20.2.4.
const (
	MultipartMetaSuffix = ".meta"
	// MultipartUploadIDPrefix starts every upload id radosgw makes,
	// MULTIPART_UPLOAD_ID_PREFIX (rgw_multi.h:16).
	MultipartUploadIDPrefix = "2~"
	// MultipartUploadIDPrefixLegacy is MULTIPART_UPLOAD_ID_PREFIX_LEGACY
	// (rgw_multi.h:15).
	MultipartUploadIDPrefixLegacy = "2/"
)

// MultipartMetaName is RGWMPObj::get_meta: "<key>.<uploadID>.meta", the name
// of the upload's meta object in the multipart namespace. RGWMPObj::init
// clears every name of an object without a key, so an empty key yields "".
func MultipartMetaName(key, uploadID string) string {
	if key == "" {
		return ""
	}
	return key + "." + uploadID + MultipartMetaSuffix
}

// MultipartPrefix is the manifest prefix MultipartObjectProcessor::prepare
// sets: "<key>.<unique>", where unique is the upload id, or the 32-character
// random string process_first_chunk falls back to when the part head already
// exists, as it does for a part uploaded again
// (driver/rados/rgw_putobj_processor.cc:414-439 and :484-489 at v19.2.6,
// :448-473 and :518-523 at v20.2.4).
func MultipartPrefix(key, unique string) string { return key + "." + unique }

// MultipartPartName is RGWMPObj::get_part: "<prefix>.<n>", the part head's
// name in the multipart namespace, with n printed as the int get_part takes.
func MultipartPartName(prefix string, n uint32) string {
	return prefix + "." + strconv.Itoa(int(int32(n))) //nolint:gosec // get_part's num is an int
}

// MultipartPartKey is the meta object's omap key for part n of an upload
// whose id IsV2UploadID: "part." and n as %08d
// (driver/rados/rgw_putobj_processor.cc:534-543 and
// driver/rados/rgw_sal_rados.cc:3352-3356 at v19.2.6, :572-581 and
// :4198-4202 at v20.2.4). An older upload keys its parts by the part number
// as the request spelled it.
func MultipartPartKey(n uint32) string {
	return fmt.Sprintf("part.%08d", int32(n)) //nolint:gosec // MultipartObjectProcessor's part_num is an int
}

// ParseMultipartMeta is RGWMPObj::from_meta
// (services/svc_tier_rados.h:76-87 at v19.2.6 and v20.2.4): the key and
// upload id of a meta object's name, split at its last two dots, and false
// when the name has no dot, or none before its last one. A name whose only
// dot leads it, as ".meta"'s does, is the exception: from_meta's second
// rfind starts from npos and finds that dot again. A split that leaves the
// key empty succeeds with both parts empty, as RGWMPObj::init clears an
// object without a key.
func ParseMultipartMeta(name string) (key, uploadID string, ok bool) {
	end := strings.LastIndexByte(name, '.')
	if end < 0 {
		return "", "", false
	}
	mid := 0
	if end > 0 {
		if mid = strings.LastIndexByte(name[:end], '.'); mid < 0 {
			return "", "", false
		}
	}
	if mid == 0 {
		return "", "", true
	}
	return name[:mid], name[mid+1 : end], true
}

// IsMultipartMeta is MultipartMetaFilter (services/svc_tier_rados.cc:10-32):
// the name is longer than ".meta", ends in it, and has a dot before it.
func IsMultipartMeta(name string) bool {
	rest, ok := strings.CutSuffix(name, MultipartMetaSuffix)
	return ok && rest != "" && strings.IndexByte(rest, '.') >= 0
}

// IsV2UploadID is is_v2_upload_id (rgw_multi.cc:76-82): an upload id with
// either prefix keys its parts as MultipartPartKey does.
func IsV2UploadID(id string) bool {
	return strings.HasPrefix(id, MultipartUploadIDPrefix) || strings.HasPrefix(id, MultipartUploadIDPrefixLegacy)
}

// UploadPartInfo is RGWUploadPartInfo (rgw_basic_types.h:253 at v19.2.6,
// :270 at v20.2.4): one registered part, stored under MultipartPartKey in
// the meta object's omap. The zero value lacks the C++ member defaults;
// DecodeUploadPartInfo fills them for the fields an older version lacks.
type UploadPartInfo struct {
	Num           uint32
	Size          uint64
	ETag          string
	Modified      time.Time
	Manifest      Manifest
	Compression   CompressionInfo
	AccountedSize uint64
	// PastPrefixes is the std::set of the prefixes the part was uploaded
	// under before, whose objects completion or abort removes; it encodes
	// sorted, each once.
	PastPrefixes []string
	// Cksum is Tentacle's std::optional<rgw::cksum::Cksum>, carried as
	// encoded since rgw-go computes no checksums: nil when absent, else the
	// presence byte 1 and the checksum's own ENCODE_START frame.
	Cksum []byte
}

// Encode mirrors RGWUploadPartInfo::encode, ENCODE_START(5, 2) on Squid and
// ENCODE_START(6, 2) with the checksum on Tentacle
// (rgw_basic_types.h:268 at v19.2.6, :286 at v20.2.4).
func (p UploadPartInfo) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(5)
	if r >= denc.Tentacle {
		v = 6
	}
	f := e.BeginStruct(v, 2)
	e.U32(p.Num)
	e.U64(p.Size)
	e.String(p.ETag)
	e.Time(p.Modified)
	p.Manifest.Encode(e, r)
	p.Compression.Encode(e, r)
	e.U64(p.AccountedSize)
	denc.EncodeSlice(e, stringSet(p.PastPrefixes), (*denc.Encoder).String)
	if v >= 6 {
		if p.Cksum == nil {
			e.Bool(false)
		} else {
			e.Raw(p.Cksum)
		}
	}
	e.EndStruct(f)
}

// DecodeUploadPartInfo mirrors RGWUploadPartInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN(6, 2, 2): the manifest from version 3, the
// compression info and accounted size from 4, with the accounted size the
// size before that, the past prefixes from 5 and the checksum from 6.
func DecodeUploadPartInfo(d *denc.Decoder) UploadPartInfo {
	h := d.BeginStructLegacy(6, 2, 2, 0)
	p := UploadPartInfo{Manifest: NewManifest(), Compression: NewCompressionInfo()}
	p.Num = d.U32()
	p.Size = d.U64()
	p.ETag = d.String()
	p.Modified = d.Time()
	if h.Version >= 3 {
		p.Manifest = DecodeManifest(d)
	}
	if h.Version >= 4 {
		p.Compression = DecodeCompressionInfo(d)
		p.AccountedSize = d.U64()
	} else {
		p.AccountedSize = p.Size
	}
	if h.Version >= 5 {
		p.PastPrefixes = stringSet(denc.DecodeSlice(d, (*denc.Decoder).String))
	}
	if h.Version >= 6 && d.Bool() {
		p.Cksum = decodeCksum(d)
	}
	d.EndStruct(h)
	return p
}

// decodeCksum reads a present rgw::cksum::Cksum as Cksum::decode does,
// DECODE_START(2), and returns it as UploadPartInfo.Cksum carries it.
func decodeCksum(d *denc.Decoder) []byte {
	h := d.BeginStruct(2)
	body := d.RestOfStruct(h)
	d.EndStruct(h)
	if d.Err() != nil {
		return nil
	}
	out := make([]byte, 0, 7+len(body))
	out = append(out, 1, h.Version, h.Compat)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body))) //nolint:gosec // the body came from a u32 length
	return append(out, body...)
}

// MultipartUploadInfo is multipart_upload_info (rgw_common.h:1508 at
// v19.2.6, :1552 at v20.2.4), the meta object's data: the upload's
// destination placement and the object lock settings its initiation gave.
type MultipartUploadInfo struct {
	DestPlacement PlacementRule
	// Retention is nil when obj_retention_exist is false.
	Retention *ObjectRetention
	// LegalHold is nil when obj_legal_hold_exist is false.
	LegalHold *ObjectLegalHold
	// CksumType is Tentacle's rgw::cksum::Type, 0 for none.
	CksumType uint16
	// CksumFlags is Tentacle's cksum_flags, 0 for FLAG_CKSUM_NONE.
	CksumFlags uint16
}

// Encode mirrors multipart_upload_info::encode, ENCODE_START(2, 1) on Squid
// and ENCODE_START(4, 1) with the checksum type and flags on Tentacle. The
// retention and legal hold are written whether they exist or not, a default
// one in their place when they do not.
func (u MultipartUploadInfo) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(2)
	if r >= denc.Tentacle {
		v = 4
	}
	f := e.BeginStruct(v, 1)
	u.DestPlacement.Encode(e, r)
	e.Bool(u.Retention != nil)
	e.Bool(u.LegalHold != nil)
	var ret ObjectRetention
	if u.Retention != nil {
		ret = *u.Retention
	}
	ret.Encode(e, r)
	var hold ObjectLegalHold
	if u.LegalHold != nil {
		hold = *u.LegalHold
	}
	hold.Encode(e, r)
	if v >= 4 {
		e.U16(u.CksumType)
		e.U16(u.CksumFlags)
	}
	e.EndStruct(f)
}

// DecodeMultipartUploadInfo mirrors multipart_upload_info::decode as
// Tentacle has it, DECODE_START_LEGACY_COMPAT_LEN(4, 1, 1): the object lock
// settings from version 2, the checksum type from 3 and its flags from 4.
// Squid's DECODE_START(2) reads the same fields of what either writes.
func DecodeMultipartUploadInfo(d *denc.Decoder) MultipartUploadInfo {
	h := d.BeginStructLegacy(4, 1, 1, 0)
	var u MultipartUploadInfo
	u.DestPlacement = DecodePlacementRule(d)
	if h.Version >= 2 {
		hasRet, hasHold := d.Bool(), d.Bool()
		ret := DecodeObjectRetention(d)
		hold := DecodeObjectLegalHold(d)
		if hasRet {
			u.Retention = &ret
		}
		if hasHold {
			u.LegalHold = &hold
		}
		if h.Version >= 3 {
			u.CksumType = d.U16()
		}
		if h.Version >= 4 {
			u.CksumFlags = d.U16()
		}
	}
	d.EndStruct(h)
	return u
}
