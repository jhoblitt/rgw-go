package meta_test

import (
	"encoding/json"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
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

// No corpus instance enables object lock, so the obj_lock forms below are
// built by hand from RGWBucketInfo::dump and decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2542-L2544
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590 at v19.2.7),
// RGWObjectLock's, ObjectLockRule's and DefaultRetention's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L93-L101,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L56-L58,
// :26-33 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L65-L71,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L44-L46,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L8-L12 at v19.2.7),
// and JSONDecoder::decode_json, which gives an absent member T()
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346 at v19.2.7).
var _ = Describe("BucketInfo obj_lock", func() {
	dump := func(b meta.BucketInfo, rel denc.Release) (string, error) {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		b.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes()), f.Err()
	}

	It("is not written without the flag, whatever ObjLock holds", func() {
		b := normalBucket()
		b.ObjLock = meta.RawStruct(lockBytes(true, new(governanceDays())))
		Expect(mustMarshal(b)).To(HaveSuffix(`"new_bucket_instance_id":""}`))
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			out, err := dump(b, r)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(HaveSuffix(`"new_bucket_instance_id":""}`), "%s", r)
		}
	})

	DescribeTable("is written after new_bucket_instance_id with the flag, by Dump as by MarshalJSON on both releases, and read back",
		func(raw []byte, obj string, back []byte) {
			b := lockedBucket(raw)
			want := `"new_bucket_instance_id":"","obj_lock":` + obj + `}`
			data := mustMarshal(b)
			Expect(data).To(HaveSuffix(want))
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				out, err := dump(b, r)
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(HaveSuffix(want), "%s", r)
				Expect(out).To(MatchJSON(data), "%s", r)
			}
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(data), &got)).To(Succeed())
			Expect(got.ObjLock).To(Equal(meta.RawStruct(back)))
		},
		Entry("RGWObjectLock() when ObjLock is nil", nil,
			`{"enabled":true,"rule_exist":false}`, lockBytes(true, nil)),
		Entry("a GOVERNANCE rule in days", lockBytes(true, new(governanceDays())),
			`{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}`,
			lockBytes(true, new(governanceDays()))),
		Entry("a COMPLIANCE rule in years", lockBytes(true, new(complianceYears())),
			`{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"COMPLIANCE","years":7}}}`,
			lockBytes(true, new(complianceYears()))),
		Entry("a configuration not enabled, which leaves enabled out", lockBytes(false, new(governanceDays())),
			`{"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}`,
			lockBytes(false, new(governanceDays()))),
		Entry("a rule whose days are not positive, which writes its years alone",
			lockBytes(true, &meta.DefaultRetention{Mode: "GOVERNANCE", Days: -1, Years: 2}),
			`{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","years":2}}}`,
			lockBytes(true, &meta.DefaultRetention{Mode: "GOVERNANCE", Years: 2})),
		Entry("a rule with both days and years positive, which writes its days alone",
			lockBytes(true, &meta.DefaultRetention{Mode: "GOVERNANCE", Days: 30, Years: 7}),
			`{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}`,
			lockBytes(true, new(governanceDays()))),
	)

	It("fails MarshalJSON and Dump with the error of an ObjLock that does not decode", func() {
		b := lockedBucket(meta.RawStruct{1, 1, 1, 0, 0, 0, 1})
		_, err := b.MarshalJSON()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		_, err = dump(b, denc.Tentacle)
		Expect(err).To(MatchError(denc.ErrShortBuffer))
	})

	DescribeTable("is read under the document's flag, ObjLock nil exactly when it is absent",
		func(doc string, want []byte) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(doc), &got)).To(Succeed())
			if want == nil {
				Expect(got.ObjLock).To(BeNil())
			} else {
				Expect(got.ObjLock).To(Equal(meta.RawStruct(want)))
			}
		},
		Entry("absent", `{"flags":32}`, nil),
		Entry("present as RGWObjectLock()", `{"flags":32,"obj_lock":{"enabled":true,"rule_exist":false}}`,
			lockBytes(true, nil)),
		Entry("present with a rule",
			`{"flags":32,"obj_lock":{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}}`,
			lockBytes(true, new(governanceDays()))),
		Entry("present without the flag, which ignores it",
			`{"flags":2,"obj_lock":{"enabled":true,"rule_exist":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}}`,
			nil),
		Entry("without the flag, not read even to refuse it", `{"flags":2,"obj_lock":{"enabled":"maybe"}}`, nil),
		Entry("empty, whose absent enabled is false", `{"flags":32,"obj_lock":{}}`, lockBytes(false, nil)),
		Entry("rule_exist without a rule, the rule's defaults", `{"flags":32,"obj_lock":{"enabled":true,"rule_exist":true}}`,
			lockBytes(true, &meta.DefaultRetention{})),
		Entry("a rule without rule_exist, which is not read",
			`{"flags":32,"obj_lock":{"enabled":true,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":30}}}}`,
			lockBytes(true, nil)),
		Entry("the boolean and number texts decode_json_obj reads",
			`{"flags":32,"obj_lock":{"enabled":"TRUE","rule_exist":1,"rule":{"defaultRetention":{"mode":"GOVERNANCE","days":"30"}}}}`,
			lockBytes(true, new(governanceDays()))),
	)

	DescribeTable("refuses under the flag an obj_lock member decode_json_obj cannot read",
		func(lock string) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(`{"flags":32,"obj_lock":`+lock+`}`), &got)).To(MatchError(meta.ErrBadJSONValue), lock)
		},
		Entry("an enabled that is no boolean", `{"enabled":"maybe"}`),
		Entry("days past int's range", `{"rule_exist":true,"rule":{"defaultRetention":{"days":2147483648}}}`),
		Entry("a mode that is no string", `{"rule_exist":true,"rule":{"defaultRetention":{"mode":{}}}}`),
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

	It("decodes and re-marshals identically the data radosgw-admin metadata get printed for a bucket instance on Squid", func() {
		doc, err := os.ReadFile("testdata/captured/bucket_instance.json")
		Expect(err).NotTo(HaveOccurred())
		var entry struct {
			Data json.RawMessage `json:"data"`
		}
		Expect(json.Unmarshal(doc, &entry)).To(Succeed())
		var bc meta.BucketCompleteInfo
		Expect(json.Unmarshal(entry.Data, &bc)).To(Succeed())
		Expect(bc.Info.Bucket.Name).To(Equal("plain"))
		Expect(bc.Info.Layout.Current.Layout.Normal.NumShards).To(Equal(uint32(11)))
		Expect(bc.Attrs).To(HaveKey(meta.AttrACL))
		again, err := json.Marshal(bc)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(MatchJSON(entry.Data))
	})
})
