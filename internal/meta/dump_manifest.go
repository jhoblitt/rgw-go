package meta

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWObjManifest::dump as release rel writes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L329-L359 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_obj_manifest.cc#L329-L359 at v20.2.4):
// objs alternates each offset with its part, as a formatter drops names
// inside an array; tier_config appears for cloud-s3 alone on Squid and for
// either cloud tier type on Tentacle; begin_iter and end_iter are the states
// of the iterators obj_begin and obj_end return. Where the C++ iterator would
// read past the pieces of an explicit manifest, Dump writes nothing and fails
// f with denc.ErrMalformed.
func (m Manifest) Dump(f formatter.Formatter, rel denc.Release) {
	begin, err := newManifestIter(&m, 0)
	if err != nil {
		f.Fail(err)
		return
	}
	end, err := newManifestIter(&m, m.ObjSize)
	if err != nil {
		f.Fail(err)
		return
	}
	f.OpenArraySection("objs")
	for _, k := range sortedKeys(m.Objs) {
		f.DumpUnsigned("ofs", k)
		dumpSection(f, "part", m.Objs[k], rel)
	}
	f.CloseSection()
	f.DumpUnsigned("obj_size", m.ObjSize)
	f.DumpBool("explicit_objs", m.ExplicitObjs)
	f.DumpUnsigned("head_size", m.HeadSize)
	f.DumpUnsigned("max_head_size", m.MaxHeadSize)
	f.DumpString("prefix", m.Prefix)
	dumpMap(f, "rules", m.Rules,
		func(f formatter.Formatter, k uint64) { f.DumpUnsigned("key", k) },
		func(f formatter.Formatter, r ManifestRule) { dumpSection(f, "val", r, rel) })
	f.DumpString("tail_instance", m.TailInstance)
	dumpSection(f, "tail_placement", m.TailPlacement, rel)
	f.DumpString("tier_type", m.TierType)
	if (rel < denc.Tentacle && m.TierType == TierTypeCloudS3) || (rel >= denc.Tentacle && isSupportedTierType(m.TierType)) {
		dumpSection(f, "tier_config", m.TierConfig, rel)
	}
	f.OpenObjectSection("begin_iter")
	begin.dumpTo(f, rel)
	f.CloseSection()
	f.OpenObjectSection("end_iter")
	end.dumpTo(f, rel)
	f.CloseSection()
}

// MaskedTierSecret is the secret ceph/ceph#68690's dump_mask_keys writes for
// a cloud tier with one (https://github.com/ceph/ceph/pull/68690).
const MaskedTierSecret = "******"

// MaskTierSecret is t with its S3 secret masked as dump_mask_keys masks it:
// a set secret becomes MaskedTierSecret, an empty one stays empty, and the
// access key is kept.
func MaskTierSecret(t ZoneGroupPlacementTier) ZoneGroupPlacementTier {
	if t.S3.Key.Secret != "" {
		t.S3.Key.Secret = MaskedTierSecret
	}
	return t
}

// MaskTierSecrets is m with its tier config's placement masked by
// MaskTierSecret. m is a copy, and so is every field on the way to the
// secret, so the caller's manifest keeps it; Dump writes it unmasked, as
// RGWObjManifest::dump does.
func MaskTierSecrets(m Manifest) Manifest {
	m.TierConfig.Tier = MaskTierSecret(m.TierConfig.Tier)
	return m
}

// dumpTo is obj_iterator::dump, its location rgw_obj_select::dump, whose raw
// object the iterator never sets
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L317-L327
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L382-L388
// at v19.2.7 and v20.2.4).
func (it *manifestIter) dumpTo(f formatter.Formatter, rel denc.Release) {
	f.DumpUnsigned("part_ofs", it.partOfs)
	f.DumpUnsigned("stripe_ofs", it.stripeOfs)
	f.DumpUnsigned("ofs", it.ofs)
	f.DumpUnsigned("stripe_size", it.stripeSize)
	f.DumpInt("cur_part_id", int64(it.curPartID))
	f.DumpInt("cur_stripe", int64(it.curStripe))
	f.DumpString("cur_override_prefix", it.curOverridePrefix)
	f.OpenObjectSection("location")
	f.DumpString("placement_rule", it.location.placement.String())
	dumpSection(f, "obj", it.location.obj, rel)
	dumpSection(f, "raw_obj", RawObj{}, rel)
	f.DumpBool("is_raw", false)
	f.CloseSection()
}

// Dump is RGWObjManifestPart::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L308-L315 at v19.2.7
// and v20.2.4).
func (p ManifestPart) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "loc", p.Loc, rel)
	f.DumpUnsigned("loc_ofs", p.LocOfs)
	f.DumpUnsigned("size", p.Size)
}

// Dump is RGWObjManifestRule::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L361-L368 at v19.2.7
// and v20.2.4).
func (x ManifestRule) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpUnsigned("start_part_num", uint64(x.StartPartNum))
	f.DumpUnsigned("start_ofs", x.StartOfs)
	f.DumpUnsigned("part_size", x.PartSize)
	f.DumpUnsigned("stripe_max_size", x.StripeMaxSize)
	f.DumpString("override_prefix", x.OverridePrefix)
}

// Dump is RGWObjTier::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L390-L395 at v19.2.7
// and v20.2.4), the placement tier as rel writes it.
func (t ObjTier) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", t.Name)
	dumpSection(f, "tier_placement", t.Tier, rel)
	f.DumpBool("is_multipart_upload", t.IsMultipartUpload)
}

// Dump is rgw_bucket_placement::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2482-L2486 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2544-L2548 at v20.2.4).
func (p BucketPlacement) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket", p.Bucket, rel)
	f.DumpString("placement_rule", p.PlacementRule.String())
}

// Dump is rgw_obj::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3150-L3154 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3206-L3210 at v20.2.4).
func (o Obj) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket", o.Bucket, rel)
	dumpSection(f, "key", o.Key, rel)
}

// Dump is rgw_obj_key::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3123-L3128 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3179-L3184 at v20.2.4).
func (k ObjKey) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("name", k.Name)
	f.DumpString("instance", k.Instance)
	f.DumpString("ns", k.NS)
}

// Dump is rgw_raw_obj::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3137-L3142 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3193-L3198 at v20.2.4).
func (o RawObj) Dump(f formatter.Formatter, _ denc.Release) {
	dumpPool(f, "pool", o.Pool)
	f.DumpString("oid", o.OID)
	f.DumpString("loc", o.Loc)
}
