package meta

import (
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// BucketID is rgw_bucket.
type BucketID struct {
	Tenant            string        `json:"tenant"`
	Name              string        `json:"name"`
	Marker            string        `json:"marker"`
	ID                string        `json:"bucket_id"`
	ExplicitPlacement DataPlacement `json:"explicit_placement"`
}

// DataPlacement is rgw_data_placement_target: the pools of a bucket placed
// explicitly rather than through a zone placement rule.
type DataPlacement struct {
	DataPool      Pool `json:"data_pool"`
	DataExtraPool Pool `json:"data_extra_pool"`
	IndexPool     Pool `json:"index_pool"`
}

// Encode mirrors rgw_bucket::encode, ENCODE_START(10, 10). The explicit
// placement is written only when its data pool is set.
func (b BucketID) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(10, 10)
	e.String(b.Name)
	e.String(b.Marker)
	e.String(b.ID)
	e.String(b.Tenant)
	explicit := b.ExplicitPlacement.DataPool.Name != ""
	e.Bool(explicit)
	if explicit {
		b.ExplicitPlacement.DataPool.Encode(e, r)
		b.ExplicitPlacement.DataExtraPool.Encode(e, r)
		b.ExplicitPlacement.IndexPool.Encode(e, r)
	}
	e.EndStruct(f)
}

// legacyIDLen is the most digits rgw_bucket::decode keeps of a numeric bucket
// id below version 4: it formats the id with snprintf into char[16].
const legacyIDLen = 15

// DecodeBucketID mirrors rgw_bucket::decode, DECODE_START_LEGACY_COMPAT_LEN(10, 3, 3).
// Below version 10 the pools are bare names at fixed positions; below version 4
// the bucket id is a u64.
func DecodeBucketID(d *denc.Decoder) BucketID {
	h := d.BeginStructLegacy(10, 3, 3, 0)
	v := h.Version
	var b BucketID
	p := &b.ExplicitPlacement
	b.Name = d.String()
	if v < 10 {
		p.DataPool.Name = d.String()
	}
	if v >= 2 {
		b.Marker = d.String()
		if v <= 3 {
			id := strconv.FormatUint(d.U64(), 10)
			b.ID = id[:min(len(id), legacyIDLen)]
		} else {
			b.ID = d.String()
		}
	}
	if v < 10 {
		if v >= 5 {
			p.IndexPool.Name = d.String()
		} else {
			p.IndexPool = p.DataPool
		}
		if v >= 7 {
			p.DataExtraPool.Name = d.String()
		}
	}
	if v >= 8 {
		b.Tenant = d.String()
	}
	if v >= 10 && d.Bool() {
		p.DataPool = DecodePool(d)
		p.DataExtraPool = DecodePool(d)
		p.IndexPool = DecodePool(d)
	}
	d.EndStruct(h)
	return b
}

// key is rgw_bucket::get_key(tenantDelim, idDelim), a zero delimiter
// omitting its field.
func (b BucketID) key(tenantDelim, idDelim byte) string {
	var s strings.Builder
	if b.Tenant != "" && tenantDelim != 0 {
		s.WriteString(b.Tenant)
		s.WriteByte(tenantDelim)
	}
	s.WriteString(b.Name)
	if b.ID != "" && idDelim != 0 {
		s.WriteByte(idDelim)
		s.WriteString(b.ID)
	}
	return s.String()
}

// EntryPointOID is the bucket entrypoint object's name in the metadata root
// pool, RGWSI_Bucket::get_entrypoint_meta_key: "<name>" or "<tenant>/<name>".
func (b BucketID) EntryPointOID() string { return b.key('/', 0) }

// instancePrefix is RGWSI_Bucket_SObj::instance_oid_prefix.
const instancePrefix = ".bucket.meta."

// InstanceOID is the bucket instance object's name in the metadata root pool,
// RGWSI_Bucket_SObj::instance_meta_key_to_oid of get_bi_meta_key:
// ".bucket.meta.<name>:<id>" or ".bucket.meta.<tenant>:<name>:<id>".
func (b BucketID) InstanceOID() string {
	return instancePrefix + strings.Replace(b.key('/', ':'), "/", ":", 1)
}
