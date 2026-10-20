package driver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The documents here are written out by hand from RadosObject::dump_obj_layout
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2780-L2826 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3615-L3661 at v20.2.4)
// inside RGWGetObjLayout's "result" section, whose name a JSONFormatter drops
// at the top level. The manifests of the striped and the multipart object are
// the ones radosgw v19.2.6 wrote for the populated cluster, and their dumps are
// the ones its object stat printed, without the whitespace.
var _ = Describe("ObjectLayout", func() {
	const (
		pool = testDataPool
		// plainBucket is rgw_bucket::dump of the bucket {plain, m1, m1}.
		plainBucket = `{"name":"plain","marker":"m1","bucket_id":"m1","tenant":"","explicit_placement":{"data_pool":"","data_extra_pool":"","index_pool":""}}`
		noRaw       = `"raw_obj":{"pool":"","oid":"","loc":""},"is_raw":false`
		// goldMarker is the marker of the bucket the manifest goldens were
		// written in.
		goldMarker = "e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"
	)
	var (
		c     *fakerados.Cluster
		plain *op.BucketRecord
	)
	raw := func(oid, loc string) string {
		return `{"pool":"` + pool + `","oid":"` + oid + `","loc":"` + loc + `"}`
	}
	stripe := func(ofs, loc, size string) string {
		return `{"ofs":` + ofs + `,"loc":` + loc + `,"loc_ofs":0,"loc_size":` + size + `}`
	}
	// inHead is the manifest AtomicObjectProcessor writes for an object of
	// size bytes below the 4 MiB head: the trivial rule from max_head_size
	// on, in 4 MiB stripes.
	inHead := func(key string, size uint64) meta.Manifest {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: key}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = size, size, 4<<20
		m.Prefix = ".Pfx_"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.HeadPlacementRule = plain.Info.PlacementRule
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		return m
	}
	// inHeadDoc is the document of inHead(key, size) stored under oid and
	// loc. obj_begin is in the head. obj_end's seek to size lands on the
	// rule at 4 MiB: part_ofs is the rule's start, (size - 4 MiB) / 4 MiB in
	// u64 truncates to the int -1, so stripe_ofs wraps back to 0, and
	// cur_stripe, bumped for a head that holds data, to 0; its stripe is the
	// size bytes left, below max_head_size, so the head. The one stripe is
	// the head's whole size.
	inHeadDoc := func(key, oid, loc, size string) string {
		location := `"location":{"placement_rule":"default-placement","obj":{"bucket":` + plainBucket +
			`,"key":{"name":"` + key + `","instance":"","ns":""}},` + noRaw + `}`
		return `{"head":` + raw(oid, loc) + `,"manifest":{"objs":[],"obj_size":` + size +
			`,"explicit_objs":false,"head_size":` + size + `,"max_head_size":4194304,"prefix":".Pfx_",` +
			`"rules":[{"key":0,"val":{"start_part_num":0,"start_ofs":4194304,"part_size":0,"stripe_max_size":4194304,"override_prefix":""}}],` +
			`"tail_instance":"","tail_placement":{"bucket":` + plainBucket + `,"placement_rule":"default-placement"},"tier_type":"",` +
			`"begin_iter":{"part_ofs":0,"stripe_ofs":0,"ofs":0,"stripe_size":` + size + `,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` + location + `},` +
			`"end_iter":{"part_ofs":4194304,"stripe_ofs":0,"ofs":` + size + `,"stripe_size":` + size + `,"cur_part_id":0,"cur_stripe":0,"cur_override_prefix":"",` + location + `}},` +
			`"data_location":[` + stripe("0", raw(oid, loc), size) + `]}`
	}
	encodeAt := func(m meta.Manifest, rel denc.Release) []byte {
		e := denc.NewEncoder()
		m.Encode(e, rel)
		return e.Bytes()
	}
	put := func(oid string, data []byte, attrs map[string][]byte) {
		c.Put(pool, "", oid, data)
		maps.Copy(c.Object(pool, "", oid).Xattrs, attrs)
	}
	open := func(ctx context.Context, rel denc.Release) *driver.Store {
		GinkgoHelper()
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		return s
	}
	layout := func(ctx context.Context, s *driver.Store, rec *op.BucketRecord, key string) (string, error) {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred())
		doc, err := s.ObjectLayout(ctx, st)
		if err != nil {
			Expect(fmt.Sprintf("%T", doc)).To(Equal("<nil>"), "a nil interface with the error, not a typed nil")
			return "", err
		}
		var out bytes.Buffer
		n, err := doc.WriteTo(&out)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(doc.Len()), "WriteTo's count")
		Expect(int64(out.Len())).To(Equal(doc.Len()), "the bytes written")
		return out.String(), nil
	}
	// golden reads the manifest radosgw stored for name and the dump its
	// object stat printed, compacted as a JSONFormatter writes it.
	golden := func(name string) (bin, dump []byte) {
		GinkgoHelper()
		dir := filepath.Join("..", "meta", "testdata", "manifests")
		bin, err := os.ReadFile(filepath.Join(dir, name+".bin"))
		Expect(err).NotTo(HaveOccurred())
		pretty, err := os.ReadFile(filepath.Join(dir, name+".json"))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		Expect(json.Compact(&out, pretty)).To(Succeed())
		return bin, out.Bytes()
	}

	BeforeEach(func() {
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		plain = &op.BucketRecord{Info: meta.BucketInfo{
			Bucket:        meta.BucketID{Name: "plain", Marker: "m1", ID: "m1"},
			PlacementRule: meta.ParsePlacementRule("default-placement"),
		}}
	})

	It("lays out an object in its head as its one stripe", func(ctx SpecContext) {
		put("m1_small.bin", make([]byte, 1024), map[string][]byte{meta.AttrManifest: encodeAt(inHead("small.bin", 1024), denc.Squid)})
		Expect(layout(ctx, open(ctx, denc.Squid), plain, "small.bin")).To(Equal(inHeadDoc("small.bin", "m1_small.bin", "", "1024")))
	})

	It("names an object whose key starts with _ by its escaped oid and its locator", func(ctx SpecContext) {
		put("m1___underscore", make([]byte, 5), map[string][]byte{meta.AttrManifest: encodeAt(inHead("_underscore", 5), denc.Squid)})
		Expect(layout(ctx, open(ctx, denc.Squid), plain, "_underscore")).
			To(Equal(inHeadDoc("_underscore", "m1___underscore", "m1__underscore", "5")))
	})

	It("lays out a compressed object in its stored bytes, not its original size", func(ctx SpecContext) {
		ci := meta.CompressionInfo{Type: "zlib", OrigSize: 1000, Blocks: []meta.CompressionBlock{{Len: 300}}}
		put("m1_comp.bin", make([]byte, 300), map[string][]byte{
			meta.AttrManifest: encodeAt(inHead("comp.bin", 300), denc.Squid), meta.AttrCompression: encode(ci),
		})
		Expect(layout(ctx, open(ctx, denc.Squid), plain, "comp.bin")).To(Equal(inHeadDoc("comp.bin", "m1_comp.bin", "", "300")))
	})

	It("lays out an empty object as no stripes, its iterators seeking past a head that holds nothing", func(ctx SpecContext) {
		put("m1_empty", nil, map[string][]byte{meta.AttrManifest: encodeAt(inHead("empty", 0), denc.Squid)})
		// Both iterators seek to 0 on the rule, as head_size is 0: stripe_ofs
		// wraps back to 0, and cur_stripe stays the int -1, as no head holds
		// data to bump it past.
		iter := `{"part_ofs":4194304,"stripe_ofs":0,"ofs":0,"stripe_size":0,"cur_part_id":0,"cur_stripe":-1,"cur_override_prefix":"",` +
			`"location":{"placement_rule":"default-placement","obj":{"bucket":` + plainBucket +
			`,"key":{"name":"empty","instance":"","ns":""}},` + noRaw + `}}`
		Expect(layout(ctx, open(ctx, denc.Squid), plain, "empty")).To(Equal(`{"head":` + raw("m1_empty", "") +
			`,"manifest":{"objs":[],"obj_size":0,"explicit_objs":false,"head_size":0,"max_head_size":4194304,"prefix":".Pfx_",` +
			`"rules":[{"key":0,"val":{"start_part_num":0,"start_ofs":4194304,"part_size":0,"stripe_max_size":4194304,"override_prefix":""}}],` +
			`"tail_instance":"","tail_placement":{"bucket":` + plainBucket + `,"placement_rule":"default-placement"},"tier_type":"",` +
			`"begin_iter":` + iter + `,"end_iter":` + iter + `},"data_location":[]}`))
	})

	It("lays out an object completed from one empty part as no stripes", func(ctx SpecContext) {
		// CompleteMultipartUpload's append copies the lone part's manifest
		// whole: its rule from offset 0 in part 1, part_size still 0, and no
		// head. Both iterators seek to 0 on that rule, in part 1's first
		// stripe, which is the part's head in the multipart namespace.
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "mp"}}
		m.Prefix = "mp.2~x"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, StripeMaxSize: 4 << 20}}
		m.HeadPlacementRule = plain.Info.PlacementRule
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		put("m1_mp", nil, map[string][]byte{meta.AttrManifest: encodeAt(m, denc.Squid)})
		iter := `{"part_ofs":0,"stripe_ofs":0,"ofs":0,"stripe_size":0,"cur_part_id":1,"cur_stripe":0,"cur_override_prefix":"",` +
			`"location":{"placement_rule":"default-placement","obj":{"bucket":` + plainBucket +
			`,"key":{"name":"mp.2~x.1","instance":"","ns":"multipart"}},` + noRaw + `}}`
		Expect(layout(ctx, open(ctx, denc.Squid), plain, "mp")).To(Equal(`{"head":` + raw("m1_mp", "") +
			`,"manifest":{"objs":[],"obj_size":0,"explicit_objs":false,"head_size":0,"max_head_size":0,"prefix":"mp.2~x",` +
			`"rules":[{"key":0,"val":{"start_part_num":1,"start_ofs":0,"part_size":0,"stripe_max_size":4194304,"override_prefix":""}}],` +
			`"tail_instance":"","tail_placement":{"bucket":` + plainBucket + `,"placement_rule":"default-placement"},"tier_type":"",` +
			`"begin_iter":` + iter + `,"end_iter":` + iter + `},"data_location":[]}`))
	})

	It("lays out a striped object as its head, then its shadow stripes, the last cut at the object's end", func(ctx SpecContext) {
		bin, dump := golden("squid-large")
		rec := &op.BucketRecord{Info: meta.BucketInfo{
			Bucket:        meta.BucketID{Name: "plain", Marker: goldMarker, ID: goldMarker},
			PlacementRule: meta.ParsePlacementRule("default-placement"),
		}}
		put(goldMarker+"_large.bin", make([]byte, 4<<20), map[string][]byte{meta.AttrManifest: bin})
		shadow := goldMarker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
		head := raw(goldMarker+"_large.bin", "")
		Expect(layout(ctx, open(ctx, denc.Squid), rec, "large.bin")).To(Equal(`{"head":` + head + `,"manifest":` + string(dump) +
			`,"data_location":[` + stripe("0", head, "4194304") + `,` + stripe("4194304", raw(shadow+"1", ""), "4194304") + `,` +
			stripe("8388608", raw(shadow+"2", ""), "2097152") + `]}`))
	})

	It("lays out a multipart object part by part, with no data in its head", func(ctx SpecContext) {
		bin, dump := golden("squid-multipart")
		rec := &op.BucketRecord{Info: meta.BucketInfo{
			Bucket:        meta.BucketID{Name: "plain", Marker: goldMarker, ID: goldMarker},
			PlacementRule: meta.ParsePlacementRule("default-placement"),
		}}
		put(goldMarker+"_multipart.bin", nil, map[string][]byte{meta.AttrManifest: bin})
		const prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		part := func(n string) string { return raw(goldMarker+"__multipart_"+prefix+"."+n, "") }
		shadow := func(n string) string { return raw(goldMarker+"__shadow_"+prefix+"."+n, "") }
		Expect(layout(ctx, open(ctx, denc.Squid), rec, "multipart.bin")).To(Equal(`{"head":` + raw(goldMarker+"_multipart.bin", "") +
			`,"manifest":` + string(dump) + `,"data_location":[` +
			stripe("0", part("1"), "4194304") + `,` + stripe("4194304", shadow("1_1"), "4194304") + `,` +
			stripe("8388608", part("2"), "4194304") + `,` + stripe("12582912", shadow("2_1"), "4194304") + `,` +
			stripe("16777216", part("3"), "4194304") + `]}`))
	})

	DescribeTable("writes the manifest's tier_config by the store's release",
		func(ctx SpecContext, rel denc.Release, want types.GomegaMatcher) {
			m := inHead("cold.bin", 3)
			m.TierType = meta.TierTypeCloudS3Glacier
			m.TierConfig.Name = "glacier"
			put("m1_cold.bin", make([]byte, 3), map[string][]byte{meta.AttrManifest: encodeAt(m, rel)})
			Expect(layout(ctx, open(ctx, rel), plain, "cold.bin")).To(want)
		},
		Entry("leaving a cloud-s3-glacier tier out on Squid, as v19.2.7 does", denc.Squid,
			ContainSubstring(`"tier_type":"cloud-s3-glacier","begin_iter":`)),
		Entry("writing it on Tentacle", denc.Tentacle,
			ContainSubstring(`"tier_type":"cloud-s3-glacier","tier_config":{"name":"glacier","tier_placement":`)),
	)

	// radosgw writes a cloud tier's S3 key and secret here, to any holder of
	// admin=read (docs/ceph-upstream-bugs.md, "radosgw's ?layout hands a
	// cloud tier's S3 secret to any holder of admin=read"); rgw-go masks the
	// secret with meta.MaskTierSecrets and keeps the access key.
	DescribeTable("masks a cloud tier's S3 secret and keeps its access key",
		func(ctx SpecContext, rel denc.Release, tierType, secret, want string) {
			tier := meta.NewZoneGroupPlacementTier()
			tier.TierType, tier.StorageClass, tier.S3.Endpoint = tierType, "CLOUD", "http://cloud"
			tier.S3.Key.ID, tier.S3.Key.Secret = "AKCLOUD", secret
			m := inHead("tiered", 0)
			m.TierType = tierType
			m.TierConfig = meta.ObjTier{Name: "CLOUD", Tier: tier}
			put("m1_tiered", nil, map[string][]byte{meta.AttrManifest: encodeAt(m, rel)})
			doc, err := layout(ctx, open(ctx, rel), plain, "tiered")
			Expect(err).NotTo(HaveOccurred())
			Expect(doc).To(ContainSubstring(`"s3":{"endpoint":"http://cloud","access_key":"AKCLOUD","secret":"` + want + `","region":""`))
			Expect(doc).NotTo(ContainSubstring("cloud-secret"))
		},
		Entry("a set cloud-s3 secret on Squid", denc.Squid, meta.TierTypeCloudS3, "cloud-secret", "******"),
		Entry("a set cloud-s3 secret on Tentacle", denc.Tentacle, meta.TierTypeCloudS3, "cloud-secret", "******"),
		Entry("a set cloud-s3-glacier secret on Tentacle, which writes that tier's config", denc.Tentacle, meta.TierTypeCloudS3Glacier, "cloud-secret", "******"),
		Entry("an empty secret, which stays empty", denc.Tentacle, meta.TierTypeCloudS3, "", ""),
	)

	It("answers InternalError for an object without a manifest, which radosgw dereferences", func(ctx SpecContext) {
		put("m1_bare", make([]byte, 3), map[string][]byte{meta.AttrETag: []byte("e")})
		doc, err := layout(ctx, open(ctx, denc.Squid), plain, "bare")
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(doc).To(BeEmpty())
	})

	It("answers NoSuchKey for an object that does not exist", func(ctx SpecContext) {
		_, err := layout(ctx, open(ctx, denc.Squid), plain, "missing")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})

	It("fails a walk that stops moving, where radosgw's loop never ends", func(ctx SpecContext) {
		m := inHead("stuck", 4)
		m.ObjSize, m.MaxHeadSize = 8, 4
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4}}
		put("m1_stuck", make([]byte, 4), map[string][]byte{meta.AttrManifest: encodeAt(m, denc.Squid)})
		doc, err := layout(ctx, open(ctx, denc.Squid), plain, "stuck")
		Expect(err).To(MatchError(denc.ErrMalformed))
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(doc).To(BeEmpty())
	})
})
