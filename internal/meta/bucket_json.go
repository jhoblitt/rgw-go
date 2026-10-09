package meta

import (
	"fmt"
)

// object is a struct member's own members: decode_json_obj of a type with a
// decode_json reads the member's children by name, which here must be an
// object's. An absent member reads as an object without members.
func (f *jsonFields) object(name string) *jsonFields {
	v, ok := f.member(name)
	if !ok {
		return &jsonFields{m: map[string]JSONMember{}}
	}
	if jsonKind(v.Raw) != '{' {
		f.fail(name, "not an object")
		return &jsonFields{m: map[string]JSONMember{}}
	}
	o, err := newJSONFields(v.Raw)
	if err != nil {
		f.err = err
		return &jsonFields{m: map[string]JSONMember{}}
	}
	return o
}

// pool is decode_json_obj(rgw_pool&): the string form through rgw_pool's
// constructor (rgw_common.cc:2429-2434 at v19.2.6, :2491-2496 at v20.2.4).
func (f *jsonFields) pool(name string) Pool { return ParsePool(f.str(name)) }

// owner is decode_json_obj(rgw_owner&): parse_owner of the string
// (rgw_basic_types.cc:228-233 at v19.2.6, :231-236 at v20.2.4), the empty
// user when absent.
func (f *jsonFields) owner(name string) Owner {
	if !f.has(name) {
		return UserOwner(UserID{})
	}
	return ParseOwner(f.str(name))
}

// bucketID is rgw_bucket::decode_json (rgw_basic_types.cc:153-165 at
// v19.2.6 and v20.2.4): with no explicit data pool, the old format's
// "pool", "data_extra_pool" and "index_pool" members of the bucket itself.
func (f *jsonFields) bucketID(name string) BucketID {
	o := f.object(name)
	b := BucketID{
		Name:   o.str("name"),
		Marker: o.str("marker"),
		ID:     o.str("bucket_id"),
		Tenant: o.str("tenant"),
	}
	p := o.object("explicit_placement")
	b.ExplicitPlacement = DataPlacement{DataPool: p.pool("data_pool"), DataExtraPool: p.pool("data_extra_pool"), IndexPool: p.pool("index_pool")}
	if o.err == nil {
		o.err = p.err
	}
	if b.ExplicitPlacement.DataPool == (Pool{}) {
		b.ExplicitPlacement = DataPlacement{DataPool: o.pool("pool"), DataExtraPool: o.pool("data_extra_pool"), IndexPool: o.pool("index_pool")}
	}
	if f.err == nil && o.err != nil {
		f.err = fmt.Errorf("%s: %w", name, o.err)
	}
	return b
}

// UnmarshalJSON is RGWBucketEntryPoint::decode_json
// (driver/rados/rgw_bucket.cc:3593-3604 at v19.2.6, :3694-3705 at v20.2.4)
// over RGWBucketEntryPoint's defaults.
func (b *BucketEntryPoint) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := NewBucketEntryPoint()
	out.Bucket = f.bucketID("bucket")
	out.Owner = f.owner("owner")
	out.CreationTime = f.time("creation_time")
	out.Linked = f.boolean("linked")
	out.HasBucketInfo = f.boolean("has_bucket_info")
	if out.HasBucketInfo && f.err == nil {
		info := NewBucketInfo()
		f.decode("old_bucket_info", &info)
		out.OldBucketInfo = &info
	}
	if f.err != nil {
		return f.err
	}
	*b = out
	return nil
}

// UnmarshalJSON is RGWBucketInfo::decode_json (rgw_common.cc:2544-2585 at
// v19.2.6, :2606-2647 at v20.2.4) over RGWBucketInfo's defaults: the layout
// is NewBucketLayout's with the dump's num_shards, bi_shard_hash_type and
// index_type, and a Normal index gets its in-index log, as
// init_default_bucket_layout gives one (driver/rados/rgw_bucket.cc:2781-2801
// at v19.2.6, :2895-2919 at v20.2.4); "region" stands in for an absent or
// empty zonegroup. A website configuration or a sync policy with groups is
// ErrOpaqueJSON, as they are carried opaque, and a hash type, index type or
// reshard status the enums do not name is refused. The object lock
// configuration has no JSON form at either release, so it is the default,
// and decode_json does not read new_bucket_instance_id, so it is empty.
func (b *BucketInfo) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := NewBucketInfo()
	out.Bucket = f.bucketID("bucket")
	out.CreationTime = f.time("creation_time")
	out.Owner = f.owner("owner")
	out.Flags = f.uint32v("flags")
	if out.Zonegroup = f.str("zonegroup"); out.Zonegroup == "" {
		out.Zonegroup = f.str("region")
	}
	out.PlacementRule = ParsePlacementRule(f.str("placement_rule"))
	out.HasInstanceObj = f.boolean("has_instance_obj")
	out.Quota = f.quota("quota")
	current := &out.Layout.Current.Layout
	current.Normal.NumShards = f.uint32v("num_shards")
	if f.uint32v("bi_shard_hash_type") != uint32(HashMod) {
		f.fail("bi_shard_hash_type", "not Mod")
	}
	out.RequesterPays = f.boolean("requester_pays")
	if f.boolean("has_website") {
		return fmt.Errorf("%w: website_conf", ErrOpaqueJSON)
	}
	out.SwiftVersioning = f.boolean("swift_versioning")
	out.SwiftVerLocation = f.str("swift_ver_location")
	switch t := IndexType(f.uint32v("index_type")); t { //nolint:gosec // refused below unless Normal or Indexless
	case IndexNormal, IndexIndexless:
		current.Type = t
	default:
		f.fail("index_type", "unknown")
	}
	f.entries("mdsearch_config", func(e *jsonFields) {
		k, v := e.str("key"), e.uint32v("val")
		if out.MDSearchConfig == nil {
			out.MDSearchConfig = map[string]uint32{}
		}
		out.MDSearchConfig[k] = v
	})
	switch rs := f.int32v("reshard_status"); {
	case rs < int32(ReshardStatusNotResharding) || rs > int32(ReshardStatusInLogrecord):
		f.fail("reshard_status", "unknown")
	default:
		out.ReshardStatus = ReshardStatus(rs)
	}
	if f.has("sync_policy") && len(f.object("sync_policy").elems("groups")) > 0 {
		return fmt.Errorf("%w: sync_policy", ErrOpaqueJSON)
	}
	if f.err != nil {
		return f.err
	}
	if current.Type == IndexNormal {
		out.Layout.Logs = []LogLayoutGen{LogLayoutFromIndex(0, out.Layout.Current)}
	}
	*b = out
	return nil
}

// BucketCompleteInfo is RGWBucketCompleteInfo, the "bucket.instance"
// metadata section's document (driver/rados/rgw_bucket.h:51-57 at v19.2.6,
// :50-56 at v20.2.4).
type BucketCompleteInfo struct {
	Info  BucketInfo `json:"bucket_info"`
	Attrs AttrsJSON  `json:"attrs"`
}

// UnmarshalJSON is RGWBucketCompleteInfo::decode_json
// (driver/rados/rgw_bucket.cc:2117-2120 at v19.2.6, :2283-2286 at v20.2.4).
func (c *BucketCompleteInfo) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	info := NewBucketInfo()
	f.decode("bucket_info", &info)
	var attrs AttrsJSON
	f.decode("attrs", &attrs)
	if f.err != nil {
		return f.err
	}
	c.Info, c.Attrs = info, attrs
	return nil
}
