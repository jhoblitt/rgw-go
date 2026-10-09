package meta_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// These pin what the corpus cannot: the keys RGWZoneParams::dump adds at
// v20.2.4 (rgw_zone.cc:312-340) and the embedded tier config.
var _ = Describe("Dump", func() {
	render := func(v meta.Dumper, rel denc.Release) string {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		v.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes())
	}
	It("writes the zone params keys Tentacle adds only on Tentacle", func() {
		z := meta.ZoneParams{ID: "zid", Name: "z1", DedupPool: meta.ParsePool("z1.rgw.dedup"), RestorePool: meta.ParsePool("z1.rgw.restore")}
		squid := render(z, denc.Squid)
		Expect(squid).NotTo(ContainSubstring("dedup_pool"))
		Expect(squid).NotTo(ContainSubstring("bucket_logging_pool"))
		Expect(squid).NotTo(ContainSubstring("restore_pool"))
		tentacle := render(z, denc.Tentacle)
		Expect(tentacle).To(ContainSubstring(`"control_pool":"","dedup_pool":"z1.rgw.dedup","gc_pool":""`))
		Expect(tentacle).To(ContainSubstring(`"group_pool":"","bucket_logging_pool":"","system_key"`))
		Expect(tentacle).To(HaveSuffix(`"realm_id":"","restore_pool":"z1.rgw.restore"}`))
	})
	It("writes caps as RGWUserCaps::dump(f, name) does: \"cap\" sections in type order, <none> for no bits", func() {
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("u")
		meta.Caps{"zone": meta.CapRead, "users": meta.CapAll, "usage": 0}.DumpAs(f, "caps")
		f.CloseSection()
		Expect(string(f.Bytes())).To(Equal(`<u><caps><cap><type>usage</type><perm>&lt;none&gt;</perm></cap>` +
			`<cap><type>users</type><perm>*</perm></cap><cap><type>zone</type><perm>read</perm></cap></caps></u>`))
		Expect(render(meta.Caps{"users": meta.CapWrite}, denc.Squid)).To(Equal(`{"caps":[{"type":"users","perm":"write"}]}`))
		Expect(render(meta.Caps(nil), denc.Squid)).To(Equal(`{"caps":[]}`))
	})
	DescribeTable("names identity types as dump_user_info does",
		func(t meta.IdentityType, want string) { Expect(t.DumpName()).To(Equal(want)) },
		Entry("rgw", meta.IdentityRGW, "rgw"),
		Entry("keystone", meta.IdentityKeystone, "keystone"),
		Entry("ldap", meta.IdentityLDAP, "ldap"),
		Entry("root", meta.IdentityRoot, "root"),
		Entry("role, which it does not name", meta.IdentityRole, "none"),
		Entry("none", meta.IdentityNone, "none"),
	)
	DescribeTable("validates account ids as rgw::account::validate_id does",
		func(id string, want bool) { Expect(meta.ValidAccountID(id)).To(Equal(want), "%q", id) },
		Entry("RGW and 17 digits", "RGW00000000000000001", true),
		Entry("16 digits", "RGW0000000000000001", false),
		Entry("18 digits", "RGW000000000000000001", false),
		Entry("lowercase prefix", "rgw00000000000000001", false),
		Entry("a letter among the digits", "RGW0000000000000000a", false),
		Entry("empty", "", false),
	)
	It("never writes main's vector pool, which neither release dumps", func() {
		z := meta.ZoneParams{Name: "z1", VectorPool: meta.ParsePool("elsewhere")}
		Expect(render(z, denc.Tentacle)).NotTo(ContainSubstring("vector_pool"))
	})
	It("writes a tier config value unquoted when it was stored unquoted, as radosgw does", func() {
		var tc meta.JSONFormattable
		tc.Type = meta.FormattableObject
		tc.Object = map[string]meta.JSONFormattable{
			"retain": {Type: meta.FormattableValue, Value: "+5"},
			"target": {Type: meta.FormattableValue, Value: "s3", Quoted: true},
			"hosts": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{
				{Type: meta.FormattableValue, Value: "a", Quoted: true},
				{},
			}},
		}
		z := meta.ZoneParams{TierConfig: tc}
		Expect(render(z, denc.Squid)).To(ContainSubstring(`"tier_config":{"hosts":["a"],"retain":+5,"target":"s3"}`))
	})
	It("writes the system key as dump_plain and the placement pools as std::map entries", func() {
		std := meta.ParsePool("z1.rgw.buckets.data")
		z := meta.ZoneParams{
			SystemKey: meta.AccessKey{ID: "AK", Secret: "SK"},
			PlacementPools: map[string]meta.ZonePlacementInfo{
				"p2": meta.NewZonePlacementInfo(),
				"p1": {IndexType: 1, StorageClasses: meta.ZoneStorageClasses{"STANDARD": {DataPool: &std}}},
			},
		}
		Expect(render(z, denc.Squid)).To(ContainSubstring(`"system_key":{"access_key":"AK","secret_key":"SK"},` +
			`"placement_pools":[{"key":"p1","val":{"index_pool":"","storage_classes":{"STANDARD":{"data_pool":"z1.rgw.buckets.data"}},"data_extra_pool":"","index_type":1,"inline_data":false}},` +
			`{"key":"p2","val":{"index_pool":"","storage_classes":{"STANDARD":{}},"data_extra_pool":"","index_type":0,"inline_data":true}}],` +
			`"realm_id":""`))
	})
	It("renders Time.Gmtime as utime_t::gmtime does", func() {
		Expect(meta.Time{}.Gmtime()).To(Equal("0.000000"))
		Expect(meta.Time{Time: time.Unix(1348588800, 123456000)}.Gmtime()).To(Equal("2012-09-25T16:00:00.123456Z"))
		Expect(meta.Time{Time: time.Unix(5, 7000)}.Gmtime()).To(Equal("5.000007"), "a time within ten years of the epoch is a duration")
	})
	It("dumps the metadata documents as their JSON forms", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: "t", ID: "alice"}
		u.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		u.SubUsers = map[string]meta.SubUser{"sub": {Name: "sub", Perm: 0x1}}
		u.System = 1
		uc := meta.UserCompleteInfo{Info: u, Attrs: meta.AttrsJSON{"user.rgw.x": []byte("v")}}
		data, err := json.Marshal(uc)
		Expect(err).NotTo(HaveOccurred())
		Expect(render(uc, denc.Squid)).To(MatchJSON(data))
		bc := meta.BucketCompleteInfo{Info: meta.NewBucketInfo(), Attrs: meta.AttrsJSON{"user.rgw.acl": {2, 2}}}
		data, err = json.Marshal(bc)
		Expect(err).NotTo(HaveOccurred())
		Expect(render(bc, denc.Tentacle)).To(MatchJSON(data))
		ep := meta.NewBucketEntryPoint()
		ep.Bucket = meta.BucketID{Name: "b", Marker: "m", ID: "i"}
		ep.Linked = true
		data, err = json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(render(ep, denc.Squid)).To(MatchJSON(data))
	})
	It("writes a user document in XML under radosgw's entry names", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: "t", ID: "alice"}
		u.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		u.SubUsers = map[string]meta.SubUser{"sub": {Name: "sub", Perm: 0x1}}
		u.System = 1
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("data")
		meta.UserCompleteInfo{Info: u, Attrs: meta.AttrsJSON{"user.rgw.x": []byte("v")}}.Dump(f, denc.Squid)
		f.CloseSection()
		s := string(f.Bytes())
		Expect(s).To(HavePrefix(`<data><user_id>t$alice</user_id><display_name></display_name><email></email><suspended>0</suspended><max_buckets>1000</max_buckets>` +
			`<subusers><subuser><id>t$alice:sub</id><permissions>read</permissions></subuser></subusers>` +
			`<keys><key><user>t$alice</user><access_key>AK</access_key><secret_key>SK</secret_key><active>true</active><create_date>0.000000</create_date></key></keys>` +
			`<swift_keys></swift_keys><caps></caps><op_mask>read, write, delete</op_mask><system>true</system><default_placement></default_placement>`))
		Expect(s).To(HaveSuffix(`<group_ids></group_ids><attrs><entry><key>user.rgw.x</key><val>dg==</val></entry></attrs></data>`))
	})
	It("leaves a Swift key's id out and names a subuser's key by its user and subuser", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "bob"}
		u.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Subuser: "s3"}}
		u.SwiftKeys = map[string]meta.AccessKey{"bob:sw": {ID: "bob:sw", Secret: "SW", Subuser: "sw", Active: true}}
		out := render(u, denc.Squid)
		Expect(out).To(ContainSubstring(`"keys":[{"user":"bob:s3","access_key":"AK","secret_key":"SK","active":false,"create_date":"0.000000"}]`))
		Expect(out).To(ContainSubstring(`"swift_keys":[{"user":"bob:sw","secret_key":"SW","active":true,"create_date":"0.000000"}]`))
		Expect(out).NotTo(ContainSubstring(`"system"`), "system is written only for a system user")
		Expect(out).NotTo(ContainSubstring(`"admin"`), "admin is written only for an admin")
	})
	It("fails the formatter on a bucket whose website configuration is carried opaque", func() {
		b := meta.NewBucketInfo()
		b.Website = meta.RawStruct{2, 1, 0, 0, 0, 0}
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		meta.BucketCompleteInfo{Info: b}.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).To(MatchError(meta.ErrOpaqueJSON))
		b = meta.NewBucketInfo()
		b.SyncPolicy = meta.RawStruct{1, 1, 4, 0, 0, 0, 1, 0, 0, 0}
		f = formatter.NewJSON(false)
		f.OpenObjectSection("x")
		b.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).To(MatchError(meta.ErrOpaqueJSON), "a sync policy with groups")
	})
	It("dumps an object version as encode_json(obj_version) does", func() {
		Expect(render(meta.ObjVersion{Ver: 3, Tag: "_abc"}, denc.Squid)).To(Equal(`{"tag":"_abc","ver":3}`))
	})
})
