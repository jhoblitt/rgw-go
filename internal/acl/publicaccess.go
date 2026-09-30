package acl

import "github.com/jhoblitt/rgw-go/internal/denc"

// PublicAccessBlock is PublicAccessBlockConfiguration
// (rgw_public_access.h:21-66), which radosgw stores in a bucket's
// user.rgw.public-access attr. rgw-go does not manage it yet, but honors a
// stored one.
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
