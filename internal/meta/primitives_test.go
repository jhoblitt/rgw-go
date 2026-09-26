package meta_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("ObjKey names", func() {
	DescribeTable("index key, oid and locator",
		func(k meta.ObjKey, indexKey, oid string) {
			Expect(k.IndexKeyName()).To(Equal(indexKey), "index key of %+v", k)
			Expect(k.OID()).To(Equal(oid), "oid of %+v", k)
		},
		Entry("plain", meta.ObjKey{Name: "foo"}, "foo", "foo"),
		Entry("leading underscore is escaped", meta.ObjKey{Name: "_foo"}, "__foo", "__foo"),
		Entry("namespaced", meta.ObjKey{Name: "foo", NS: "multipart"}, "_multipart_foo", "_multipart_foo"),
		Entry("versioned", meta.ObjKey{Name: "foo", Instance: "v1"}, "foo", "_:v1_foo"),
		Entry("versioned with a leading underscore", meta.ObjKey{Name: "_foo", Instance: "v1"}, "__foo", "_:v1__foo"),
		Entry("null instance is not encoded", meta.ObjKey{Name: "foo", Instance: "null"}, "foo", "foo"),
		Entry("namespaced and versioned", meta.ObjKey{Name: "foo", NS: "shadow", Instance: "v1"}, "_shadow_foo", "_shadow:v1_foo"),
	)
	It("sets a locator only for an escaped name outside a namespace", func() {
		Expect(meta.ObjKey{Name: "_foo"}.Locator()).To(Equal("_foo"))
		Expect(meta.ObjKey{Name: "_foo", Instance: "v1"}.Locator()).To(Equal("_foo"))
		Expect(meta.ObjKey{Name: "foo"}.Locator()).To(BeEmpty())
		Expect(meta.ObjKey{Name: "_foo", NS: "shadow"}.Locator()).To(BeEmpty())
		Expect(meta.ObjKey{}.Locator()).To(BeEmpty())
	})
})

// rgw_obj_key has no corpus objects, so its framing is checked against bytes
// transcribed from rgw_obj_key::encode and ::decode.
var objKeyV2 = []byte{
	2, 1, 20, 0, 0, 0,
	3, 0, 0, 0, 'f', 'o', 'o',
	2, 0, 0, 0, 'v', '1',
	3, 0, 0, 0, 'm', 'p', 'u',
}

var _ = Describe("ObjKey encoding", func() {
	It("encodes version 2 with the namespace", func() {
		e := denc.NewEncoder()
		meta.ObjKey{Name: "foo", Instance: "v1", NS: "mpu"}.Encode(e, denc.Squid)
		Expect(e.Bytes()).To(Equal(objKeyV2))
	})
	It("decodes version 2", func() {
		d := denc.NewDecoder(objKeyV2)
		Expect(meta.DecodeObjKey(d)).To(Equal(meta.ObjKey{Name: "foo", Instance: "v1", NS: "mpu"}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero())
	})
	It("decodes version 1, which has no namespace", func() {
		d := denc.NewDecoder([]byte{1, 1, 13, 0, 0, 0, 3, 0, 0, 0, 'f', 'o', 'o', 2, 0, 0, 0, 'v', '1'})
		Expect(meta.DecodeObjKey(d)).To(Equal(meta.ObjKey{Name: "foo", Instance: "v1"}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero())
	})
})

var _ = DescribeTable("UserID string forms round-trip",
	func(s string, want meta.UserID) {
		got := meta.ParseUserID(s)
		Expect(got).To(Equal(want), "parse %q", s)
		Expect(got.String()).To(Equal(s), "format %q", s)
	},
	Entry("bare id", "alice", meta.UserID{ID: "alice"}),
	Entry("tenant and id", "t1$alice", meta.UserID{Tenant: "t1", ID: "alice"}),
	Entry("tenant, namespace and id", "t1$oidc$alice", meta.UserID{Tenant: "t1", NS: "oidc", ID: "alice"}),
	Entry("namespace without tenant", "$oidc$alice", meta.UserID{NS: "oidc", ID: "alice"}),
)

var _ = It("UserID marshals as its string form, as RGW's encode_json does", func() {
	b, err := json.Marshal(meta.UserID{Tenant: "t1", ID: "alice"})
	Expect(err).NotTo(HaveOccurred())
	Expect(string(b)).To(Equal(`"t1$alice"`))
	var u meta.UserID
	Expect(json.Unmarshal(b, &u)).To(Succeed())
	Expect(u).To(Equal(meta.UserID{Tenant: "t1", ID: "alice"}))
})

var _ = DescribeTable("PlacementRule string forms",
	func(s string, want meta.PlacementRule) {
		Expect(meta.ParsePlacementRule(s)).To(Equal(want))
		Expect(want.String()).To(Equal(s))
	},
	Entry("name only", "default-placement", meta.PlacementRule{Name: "default-placement"}),
	Entry("name and class", "default-placement/COLD", meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}),
)

var _ = It("PlacementRule omits the STANDARD class from its string form", func() {
	Expect(meta.PlacementRule{Name: "p", StorageClass: "STANDARD"}.String()).To(Equal("p"))
	Expect(meta.PlacementRule{Name: "p"}.CanonicalStorageClass()).To(Equal("STANDARD"))
})

var _ = It("Pool splits name and namespace at the colon", func() {
	Expect(meta.ParsePool("default.rgw.meta:root")).To(Equal(meta.Pool{Name: "default.rgw.meta", NS: "root"}))
	Expect(meta.Pool{Name: "default.rgw.control"}.String()).To(Equal("default.rgw.control"))
})

var _ = DescribeTable("Pool escapes colons and backslashes as rgw_escape_str does",
	func(p meta.Pool, s string) {
		Expect(p.String()).To(Equal(s))
		Expect(meta.ParsePool(s)).To(Equal(p))
	},
	Entry("colon in name", meta.Pool{Name: "a:b", NS: "n"}, `a\:b:n`),
	Entry("backslash in namespace", meta.Pool{Name: "a", NS: `n\s`}, `a:n\\s`),
)

var _ = Describe("BucketID object names", func() {
	It("names the entrypoint and instance objects without a tenant", func() {
		b := meta.BucketID{Name: "photos", ID: "zone.4137.1"}
		Expect(b.EntryPointOID()).To(Equal("photos"))
		Expect(b.InstanceOID()).To(Equal(".bucket.meta.photos:zone.4137.1"))
	})
	It("names the entrypoint and instance objects with a tenant", func() {
		b := meta.BucketID{Tenant: "t1", Name: "photos", ID: "zone.4137.1"}
		Expect(b.EntryPointOID()).To(Equal("t1/photos"))
		Expect(b.InstanceOID()).To(Equal(".bucket.meta.t1:photos:zone.4137.1"))
	})
})

var _ = Describe("Time", func() {
	It("marshals in RGW's dump format", func() {
		t := meta.Time{Time: time.Date(2026, 9, 25, 20, 1, 2, 345678000, time.UTC)}
		b, err := json.Marshal(t)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`"2026-09-25T20:01:02.345678Z"`))
		var back meta.Time
		Expect(json.Unmarshal(b, &back)).To(Succeed())
		Expect(back.Equal(t.Time)).To(BeTrue(), "%v != %v", back, t)
	})
	It("marshals a time within ten years of the epoch as seconds, as utime_t::gmtime does", func() {
		b, err := json.Marshal(meta.Time{})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`"0.000000"`))
		var back meta.Time
		Expect(json.Unmarshal(b, &back)).To(Succeed())
		Expect(back.IsZero()).To(BeTrue())
	})
	It("round-trips through the wire encoding", func() {
		t := meta.Time{Time: time.Date(2026, 9, 25, 20, 1, 2, 345678901, time.UTC)}
		e := denc.NewEncoder()
		t.Encode(e)
		d := denc.NewDecoder(e.Bytes())
		Expect(meta.DecodeTime(d).Equal(t.Time)).To(BeTrue())
		Expect(d.Err()).NotTo(HaveOccurred())
	})
})

var _ = Describe("Quota", func() {
	It("encodes max_size_kb rounded away from zero", func() {
		e := denc.NewEncoder()
		meta.Quota{MaxSize: -1025, MaxObjects: -1}.Encode(e, denc.Squid)
		d := denc.NewDecoder(e.Bytes())
		h := d.BeginStruct(3)
		Expect(d.I64()).To(Equal(int64(-2)))
		d.EndStruct(h)
		Expect(d.Err()).NotTo(HaveOccurred())
	})
	It("derives max_size from max_size_kb at version 1", func() {
		d := denc.NewDecoder([]byte{
			1, 1, 17, 0, 0, 0,
			2, 0, 0, 0, 0, 0, 0, 0,
			5, 0, 0, 0, 0, 0, 0, 0,
			1,
		})
		Expect(meta.DecodeQuota(d)).To(Equal(meta.Quota{MaxSize: 2048, MaxObjects: 5, Enabled: true}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero())
	})
})

// decodeWhole decodes b and asserts the decode consumed it without error.
func decodeWhole[T any](b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

// The corpus holds no encodings at these versions, so they are built here
// field by field from the C++ decode bodies.
var _ = Describe("legacy decoding", func() {
	It("reads rgw_bucket v3's numeric bucket id, truncated as snprintf into char[16] does", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 3)
		e.String("b")
		e.String("data")
		e.String("m")
		e.U64(12345678901234567890)
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeBucketID)).To(Equal(meta.BucketID{
			Name: "b", Marker: "m", ID: "123456789012345",
			ExplicitPlacement: meta.DataPlacement{DataPool: meta.Pool{Name: "data"}, IndexPool: meta.Pool{Name: "data"}},
		}))
	})
	It("reads rgw_pool below v10 as the first field of an old rgw_bucket", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(9, 3)
		e.String("pool")
		e.String("rest of an old rgw_bucket")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodePool)).To(Equal(meta.Pool{Name: "pool"}))
	})
	It("unmangles a namespaced rgw_obj v4 name", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(4, 3)
		e.String("old")
		e.String("")
		e.String("multipart")
		e.String("_multipart_foo.2~x.1")
		meta.BucketID{Name: "b", Marker: "m"}.Encode(e, denc.Squid)
		e.String("")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeObj)).To(Equal(meta.Obj{
			Bucket: meta.BucketID{Name: "b", Marker: "m"},
			Key:    meta.ObjKey{Name: "foo.2~x.1", NS: "multipart"},
		}))
	})
	It("rejects a namespaced rgw_obj v4 name without a separator", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(4, 3)
		e.String("b")
		e.String("")
		e.String("shadow")
		e.String("_x")
		meta.BucketID{Name: "b"}.Encode(e, denc.Squid)
		e.String("")
		e.EndStruct(f)
		d := denc.NewDecoder(e.Bytes())
		meta.DecodeObj(d)
		Expect(d.Err()).To(MatchError(denc.ErrMalformed))
	})
	It("converts an rgw_raw_obj below v6 from the rgw_obj it was written as", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(5, 3)
		e.String("b")
		e.String("")
		e.String("")
		e.String("__foo")
		meta.BucketID{
			Name: "b", Marker: "zone.1",
			ExplicitPlacement: meta.DataPlacement{DataPool: meta.Pool{Name: "data"}, IndexPool: meta.Pool{Name: "index"}},
		}.Encode(e, denc.Squid)
		e.String("")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeRawObj)).To(Equal(meta.RawObj{
			Pool: meta.Pool{Name: "data"}, OID: "zone.1___foo", Loc: "zone.1__foo",
		}))
	})
})
