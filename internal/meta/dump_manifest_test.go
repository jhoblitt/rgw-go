package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The documents below are written out by hand from RGWObjManifest::dump and
// the dumps it calls
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_obj_manifest.cc#L308-L395
// at v19.2.7 and v20.2.4), as JSONFormatter writes them: no spaces, a name
// dropped inside an array.
const (
	// dumpBucketB is rgw_bucket::dump of {b, m.1, m.1}: name, marker,
	// bucket_id, tenant, then the explicit placement.
	dumpBucketB    = `{"name":"b","marker":"m.1","bucket_id":"m.1","tenant":"","explicit_placement":{"data_pool":"","data_extra_pool":"","index_pool":""}}`
	dumpBucketNone = `{"name":"","marker":"","bucket_id":"","tenant":"","explicit_placement":{"data_pool":"","data_extra_pool":"","index_pool":""}}`
	// dumpNoRaw ends rgw_obj_select::dump of a location the iterator set,
	// which is never raw.
	dumpNoRaw = `"raw_obj":{"pool":"","oid":"","loc":""},"is_raw":false`
)

var _ = Describe("Manifest.Dump", func() {
	bucketB := func() meta.BucketID { return meta.BucketID{Name: "b", Marker: "m.1", ID: "m.1"} }
	render := func(m meta.Manifest, rel denc.Release) (string, error) {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("manifest")
		m.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes()), f.Err()
	}
	mustRender := func(m meta.Manifest, rel denc.Release) string {
		GinkgoHelper()
		s, err := render(m, rel)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	It("writes radosgw's key order and json_stream_escaper's escapes on both releases, as MarshalJSON does", func() {
		head := meta.Obj{Bucket: bucketB(), Key: meta.ObjKey{Name: "a<b>&c\u2028\r\x7f"}}
		m := meta.NewManifest()
		m.Obj, m.ObjSize, m.HeadSize, m.MaxHeadSize = head, 6, 6, 8
		m.HeadPlacementRule = meta.PlacementRule{Name: "default-placement"}
		m.Prefix = ".p_"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 8, StripeMaxSize: 8}}
		m.TailPlacement = meta.BucketPlacement{Bucket: bucketB(), PlacementRule: m.HeadPlacementRule}

		// <, > and & stay as they are, U+2028 stays its three bytes, CR and
		// DEL take \u00xx.
		key := `{"name":"a<b>&c` + "\u2028" + `\u000d\u007f","instance":"","ns":""}`
		location := `"location":{"placement_rule":"default-placement","obj":{"bucket":` + dumpBucketB + `,"key":` + key + `},` + dumpNoRaw + `}`
		// obj_begin is in the head. obj_end seeks to 6, past the head's 6
		// bytes, onto the rule at 8: part_ofs is the rule's start, and
		// (6 - 8) / 8 in u64 truncates to the int -1, so stripe_ofs wraps
		// back to 0 and cur_stripe, bumped for the head, to 0; its stripe
		// is the 6 bytes left, still below max_head_size, so the head.
		want := `{"objs":[],"obj_size":6,"explicit_objs":false,"head_size":6,"max_head_size":8,"prefix":".p_",` +
			`"rules":[{"key":0,"val":{"start_part_num":0,"start_ofs":8,"part_size":0,"stripe_max_size":8,"override_prefix":""}}],` +
			`"tail_instance":"","tail_placement":{"bucket":` + dumpBucketB + `,"placement_rule":"default-placement"},"tier_type":"",` +
			`"begin_iter":{"part_ofs":0,"stripe_ofs":0,"ofs":0,"stripe_size":6,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` + location + `},` +
			`"end_iter":{"part_ofs":8,"stripe_ofs":0,"ofs":6,"stripe_size":6,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` + location + `}}`
		Expect(mustRender(m, denc.Squid)).To(Equal(want))
		Expect(mustRender(m, denc.Tentacle)).To(Equal(want))
		got, err := m.MarshalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(want))
	})

	It("writes an explicit manifest's objs as offset and part in turn, and obj_end's members as its seek leaves them", func() {
		head := meta.Obj{Bucket: bucketB(), Key: meta.ObjKey{Name: "k"}}
		tail := meta.Obj{Bucket: bucketB(), Key: meta.ObjKey{Name: ".t_1", NS: meta.NSShadow}}
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: head, Size: 4}, 4: {Loc: tail, LocOfs: 512, Size: 6}}
		m.Obj, m.ObjSize, m.HeadSize, m.MaxHeadSize = head, 10, 4, 4

		headLoc := `{"bucket":` + dumpBucketB + `,"key":{"name":"k","instance":"","ns":""}}`
		tailLoc := `{"bucket":` + dumpBucketB + `,"key":{"name":".t_1","instance":"","ns":"shadow"}}`
		// obj_end's seek to obj_size rests on the last piece without
		// update_explicit_pos, so its stripe_ofs and stripe_size keep their
		// initial 0; an explicit piece's location has no placement rule.
		want := `{"objs":[0,{"loc":` + headLoc + `,"loc_ofs":0,"size":4},4,{"loc":` + tailLoc + `,"loc_ofs":512,"size":6}],` +
			`"obj_size":10,"explicit_objs":true,"head_size":4,"max_head_size":4,"prefix":"","rules":[],"tail_instance":"",` +
			`"tail_placement":{"bucket":` + dumpBucketNone + `,"placement_rule":""},"tier_type":"",` +
			`"begin_iter":{"part_ofs":0,"stripe_ofs":0,"ofs":0,"stripe_size":4,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` +
			`"location":{"placement_rule":"","obj":` + headLoc + `,` + dumpNoRaw + `}},` +
			`"end_iter":{"part_ofs":0,"stripe_ofs":0,"ofs":10,"stripe_size":0,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` +
			`"location":{"placement_rule":"","obj":` + tailLoc + `,` + dumpNoRaw + `}}}`
		Expect(mustRender(m, denc.Tentacle)).To(Equal(want))
	})

	// RGWZoneGroupPlacementTier::dump inside the tier config is the release's
	// too: Squid's stops after s3
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_zone.cc#L895-L904),
	// Tentacle's adds the read-through fields
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L929-L944).
	const (
		tierS3 = `"s3":{"endpoint":"http://cloud","access_key":"","secret":"","region":"","host_style":"path",` +
			`"target_storage_class":"","target_path":"","acl_mappings":[],"multipart_sync_threshold":33554432,"multipart_min_part_size":33554432}`
		squidTierConfig = `,"tier_config":{"name":"tier","tier_placement":{"tier_type":"cloud-s3","storage_class":"CLOUD","retain_head_object":false,` +
			tierS3 + `},"is_multipart_upload":true}`
		tentacleTierConfig = `,"tier_config":{"name":"tier","tier_placement":{"tier_type":"cloud-s3","storage_class":"CLOUD","retain_head_object":false,` +
			tierS3 + `,"allow_read_through":false,"read_through_restore_days":1,"restore_storage_class":"STANDARD"},"is_multipart_upload":true}`
	)
	DescribeTable("writes tier_config by the release's rule: cloud-s3 alone on Squid, either cloud type on Tentacle",
		func(tierType string, rel denc.Release, tierConfig string) {
			m := orderManifest()
			m.TierType = tierType
			Expect(mustRender(m, rel)).To(ContainSubstring(`"tier_type":"` + tierType + `"` + tierConfig + `,"begin_iter":`))
		},
		Entry("cloud-s3 on Squid, with Squid's tier placement", meta.TierTypeCloudS3, denc.Squid, squidTierConfig),
		Entry("cloud-s3 on Tentacle, with Tentacle's tier placement", meta.TierTypeCloudS3, denc.Tentacle, tentacleTierConfig),
		Entry("cloud-s3-glacier on Squid, which v19.2.7 leaves out", meta.TierTypeCloudS3Glacier, denc.Squid, ""),
		Entry("cloud-s3-glacier on Tentacle", meta.TierTypeCloudS3Glacier, denc.Tentacle, tentacleTierConfig),
		Entry("another tier type on Tentacle", "archive", denc.Tentacle, ""),
	)

	// MaskTierSecrets masks as ceph/ceph#68690's dump_mask_keys does
	// (https://github.com/ceph/ceph/pull/68690); Dump itself stays faithful.
	DescribeTable("MaskTierSecrets masks a copy's tier secret and keeps the access key, leaving the caller's manifest and Dump unmasked",
		func(secret, want string) {
			m := orderManifest()
			m.TierType = meta.TierTypeCloudS3
			m.TierConfig.Tier.S3.Key.ID, m.TierConfig.Tier.S3.Key.Secret = "AKCLOUD", secret
			masked := meta.MaskTierSecrets(m)
			Expect(masked.TierConfig.Tier.S3.Key.ID).To(Equal("AKCLOUD"))
			Expect(masked.TierConfig.Tier.S3.Key.Secret).To(Equal(want))
			Expect(m.TierConfig.Tier.S3.Key.Secret).To(Equal(secret))
			Expect(mustRender(masked, denc.Tentacle)).To(ContainSubstring(`"access_key":"AKCLOUD","secret":"` + want + `",`))
			Expect(mustRender(m, denc.Tentacle)).To(ContainSubstring(`"access_key":"AKCLOUD","secret":"` + secret + `",`))
		},
		Entry("a set secret becomes MaskedTierSecret", "cloud-secret", meta.MaskedTierSecret),
		Entry("an empty secret stays empty", "", ""),
	)

	It("pins MaskedTierSecret to dump_mask_keys' six asterisks", func() {
		Expect(meta.MaskedTierSecret).To(Equal("******"))
	})

	It("fails the formatter, and MarshalJSON, where the C++ iterator would read past the pieces", func() {
		m := meta.NewManifest()
		m.ExplicitObjs, m.ObjSize = true, 5
		_, err := render(m, denc.Tentacle)
		Expect(err).To(MatchError(denc.ErrMalformed))
		_, err = m.MarshalJSON()
		Expect(err).To(MatchError(denc.ErrMalformed))
	})
})
