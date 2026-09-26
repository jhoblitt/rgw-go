package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// BucketEntryPoint is RGWBucketEntryPoint, the object naming a bucket's
// current instance. Its JSON form is RGWBucketEntryPoint::dump.
type BucketEntryPoint struct {
	Bucket       BucketID `json:"bucket"`
	Owner        Owner    `json:"owner"`
	CreationTime Time     `json:"creation_time"`
	Linked       bool     `json:"linked"`
	// HasBucketInfo marks an entry point from before version 8, which was
	// itself an RGWBucketInfo; OldBucketInfo holds it. Encode writes neither.
	HasBucketInfo bool        `json:"has_bucket_info"`
	OldBucketInfo *BucketInfo `json:"old_bucket_info,omitempty"`
}

// NewBucketEntryPoint returns the value RGWBucketEntryPoint's constructor
// gives: the empty user as owner, as rgw_owner default-constructs its first
// alternative, not linked, and no embedded bucket info.
func NewBucketEntryPoint() BucketEntryPoint {
	return BucketEntryPoint{Owner: UserOwner(UserID{})}
}

// Encode mirrors RGWBucketEntryPoint::encode, ENCODE_START(10, 8). The
// owner's id is duplicated into its pre-v9 position, empty for an account.
func (b BucketEntryPoint) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(10, 8)
	b.Bucket.Encode(e, r)
	e.String(b.Owner.ownerUser().ID)
	e.Bool(b.Linked)
	e.U64(unixSeconds(b.CreationTime))
	b.Owner.EncodeConverted(e, r)
	b.CreationTime.Encode(e)
	e.EndStruct(f)
}

// DecodeBucketEntryPoint mirrors RGWBucketEntryPoint::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(10, 4, 4), into NewBucketEntryPoint,
// whose fields the embedded form leaves unset. Below version 8 the encoding
// is an RGWBucketInfo, whose decoder shares these header rules; C++ rewinds
// and decodes it whole, leaving its own iterator just past the header, while
// this reads its body under the header already consumed and ends after it.
// Below version 10 the creation time is whole seconds, and below version 9
// the owner is the bare user id.
func DecodeBucketEntryPoint(d *denc.Decoder) BucketEntryPoint {
	h := d.BeginStructLegacy(10, 4, 4, 3)
	b := NewBucketEntryPoint()
	if h.Version < 8 {
		info := decodeBucketInfoBody(d, h)
		d.EndStruct(h)
		if d.Err() == nil {
			b.HasBucketInfo = true
			b.OldBucketInfo = &info
		}
		return b
	}
	b.Bucket = DecodeBucketID(d)
	userID := d.String()
	b.Linked = d.Bool()
	ctime := d.U64()
	if h.Version < 10 {
		b.CreationTime = fromUnixSeconds(ctime)
	}
	if h.Version >= 9 {
		b.Owner = DecodeOwnerConverted(d)
	} else {
		b.Owner = UserOwner(UserID{ID: userID})
	}
	if h.Version >= 10 {
		b.CreationTime = DecodeTime(d)
	}
	d.EndStruct(h)
	return b
}
