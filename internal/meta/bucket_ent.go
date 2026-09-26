package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// BucketEnt is RGWBucketEnt, a bucket's entry in its owner's bucket list with
// its usage totals. modification_time is not encoded and is left out. Its
// JSON form is RGWBucketEnt::dump, which names the creation time mtime.
type BucketEnt struct {
	Bucket        BucketID      `json:"bucket"`
	Size          uint64        `json:"size"`
	SizeRounded   uint64        `json:"size_rounded"`
	CreationTime  Time          `json:"mtime"`
	Count         uint64        `json:"count"`
	PlacementRule PlacementRule `json:"placement_rule"`
}

// Encode mirrors RGWBucketEnt::encode, ENCODE_START(7, 5). The empty string
// stands where the bucket name was, and the creation time is also written as
// u32 seconds where the mtime was.
func (b BucketEnt) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(7, 5)
	e.String("")
	e.U64(b.Size)
	e.U32(uint32(unixSeconds(b.CreationTime))) //nolint:gosec // C++ truncates time_t to __u32
	e.U64(b.Count)
	b.Bucket.Encode(e, r)
	e.U64(b.SizeRounded)
	b.CreationTime.Encode(e)
	b.PlacementRule.Encode(e, r)
	e.EndStruct(f)
}

// DecodeBucketEnt mirrors RGWBucketEnt::decode,
// DECODE_START_LEGACY_COMPAT_LEN(7, 5, 5). Below version 6 the creation time
// is the u32 seconds; below version 4 the rounded size is the size.
func DecodeBucketEnt(d *denc.Decoder) BucketEnt {
	h := d.BeginStructLegacy(7, 5, 5, 0)
	var b BucketEnt
	_ = d.String() // the old bucket name
	b.Size = d.U64()
	mt := d.U32()
	if h.Version < 6 {
		b.CreationTime = fromUnixSeconds(uint64(mt))
	}
	if h.Version >= 2 {
		b.Count = d.U64()
	}
	if h.Version >= 3 {
		b.Bucket = DecodeBucketID(d)
	}
	b.SizeRounded = b.Size
	if h.Version >= 4 {
		b.SizeRounded = d.U64()
	}
	if h.Version >= 6 {
		b.CreationTime = DecodeTime(d)
	}
	if h.Version >= 7 {
		b.PlacementRule = DecodePlacementRule(d)
	}
	d.EndStruct(h)
	return b
}
