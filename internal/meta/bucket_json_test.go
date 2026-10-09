package meta_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// normalBucket is a Normal 11-shard bucket with every field the dump
// carries set, its times at the microsecond precision the dump keeps.
func normalBucket() meta.BucketInfo {
	b := meta.NewBucketInfo()
	b.Bucket = meta.BucketID{Tenant: "t", Name: "photos", Marker: "z.4161.1", ID: "z.4161.1"}
	b.CreationTime = meta.Time{Time: time.Date(2026, 9, 29, 6, 45, 34, 50305000, time.UTC)}
	b.Owner = meta.UserOwner(meta.UserID{Tenant: "t", ID: "alice"})
	b.Flags = meta.BucketVersioned
	b.Zonegroup = "zg1"
	b.PlacementRule = meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
	b.HasInstanceObj = true
	b.Quota = meta.Quota{MaxSize: 4096, MaxObjects: 3, Enabled: true}
	b.Layout.Current.Layout.Normal.NumShards = 11
	b.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, b.Layout.Current)}
	b.RequesterPays = true
	b.SwiftVersioning = true
	b.SwiftVerLocation = "archive"
	b.MDSearchConfig = map[string]uint32{"color": 1}
	b.ReshardStatus = meta.ReshardStatusNotResharding
	return b
}

var _ = Describe("BucketEntryPoint.UnmarshalJSON", func() {
	It("reads back what MarshalJSON writes", func() {
		ep := meta.NewBucketEntryPoint()
		ep.Bucket = meta.BucketID{Tenant: "t", Name: "photos", Marker: "z.1", ID: "z.1"}
		ep.Owner = meta.AccountOwner("RGW00000000000000001")
		ep.CreationTime = meta.Time{Time: time.Date(2026, 9, 29, 6, 0, 0, 1000, time.UTC)}
		ep.Linked = true
		data, err := json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		var got meta.BucketEntryPoint
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(ep))
	})

	It("refuses an owner that is no string and a creation time that is no time", func() {
		var got meta.BucketEntryPoint
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"owner":{}}`), &got)).To(MatchError(meta.ErrBadJSONValue))
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"creation_time":"never"}`), &got)).To(MatchError(meta.ErrBadJSONValue))
	})
})

var _ = Describe("BucketInfo.UnmarshalJSON", func() {
	It("reads back what MarshalJSON writes, the layout from num_shards, bi_shard_hash_type and index_type", func() {
		b := normalBucket()
		data, err := json.Marshal(b)
		Expect(err).NotTo(HaveOccurred())
		var got meta.BucketInfo
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(b))
		Expect(got.Layout.Current.Layout.Normal.NumShards).To(Equal(uint32(11)))
	})

	It("never reads new_bucket_instance_id, which decode_json does not read", func() {
		var got meta.BucketInfo
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"reshard_status":1,"new_bucket_instance_id":"z.2"}`), &got)).To(Succeed())
		Expect(got.NewBucketInstanceID).To(BeEmpty())
		Expect(got.ReshardStatus).To(Equal(meta.ReshardStatusInProgress))
	})

	It("takes region for an absent zonegroup, as older documents name it", func() {
		var got meta.BucketInfo
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"region":"old-region"}`), &got)).To(Succeed())
		Expect(got.Zonegroup).To(Equal("old-region"))
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"zonegroup":"zg","region":"old-region"}`), &got)).To(Succeed())
		Expect(got.Zonegroup).To(Equal("zg"))
	})

	It("refuses a website configuration and a sync policy, which it carries opaque", func() {
		var got meta.BucketInfo
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"has_website":true,"website_conf":{}}`), &got)).To(MatchError(meta.ErrOpaqueJSON))
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"sync_policy":{"groups":[{"id":"g"}]}}`), &got)).To(MatchError(meta.ErrOpaqueJSON))
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"sync_policy":{"groups":[]}}`), &got)).To(Succeed(),
			"a policy without groups is empty, which radosgw drops")
	})

	DescribeTable("refuses a layout or field radosgw would store but cannot mean",
		func(doc string) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(doc), &got)).To(MatchError(meta.ErrBadJSONValue), doc)
		},
		Entry("a negative shard count, which strtoul wraps", `{"num_shards":-1}`),
		Entry("a shard count past unsigned's range", `{"num_shards":4294967296}`),
		Entry("a hash type other than Mod", `{"bi_shard_hash_type":1}`),
		Entry("an index type it does not know", `{"index_type":7}`),
		Entry("a reshard status it does not know", `{"reshard_status":9}`),
		Entry("flags that are no number", `{"flags":"versioned"}`),
		Entry("an mdsearch value that is no number", `{"mdsearch_config":[{"key":"k","val":"x"}]}`),
		Entry("a quota whose size overflows", `{"quota":{"max_size_kb":9007199254740992}}`),
	)
})

var _ = Describe("AttrsJSON", func() {
	It("writes entries in key order with base64 values and reads them back", func() {
		a := meta.AttrsJSON{"user.rgw.b": {0xff, 0x00}, "user.rgw.a": []byte("v")}
		data, err := json.Marshal(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(`[{"key":"user.rgw.a","val":"dg=="},{"key":"user.rgw.b","val":"/wA="}]`))
		var got meta.AttrsJSON
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(a))
		data, err = json.Marshal(meta.AttrsJSON(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(`[]`))
	})
})

var _ = Describe("BucketCompleteInfo", func() {
	It("writes bucket_info and attrs and reads them back", func() {
		bc := meta.BucketCompleteInfo{Info: normalBucket(), Attrs: meta.AttrsJSON{meta.AttrACL: {2, 2, 0}}}
		data, err := json.Marshal(bc)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(HavePrefix(`{"bucket_info":{`))
		var got meta.BucketCompleteInfo
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(bc))
	})
})
