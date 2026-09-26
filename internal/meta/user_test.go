package meta_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// legacyHeader writes the header DECODE_START_LEGACY_COMPAT_LEN_32 reads for
// a struct_v below both its compat and length versions: the version byte and
// three bytes the decoder skips.
func legacyHeader(e *denc.Encoder, v uint8) {
	e.U8(v)
	e.Raw([]byte{0, 0, 0})
}

func mustMarshal(v any) string {
	GinkgoHelper()
	b, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

var _ = Describe("AccessKey legacy decoding", func() {
	It("reads version 1, which had no compat byte, length, active flag or create date", func() {
		e := denc.NewEncoder()
		legacyHeader(e, 1)
		e.String("AK")
		e.String("SK")
		e.String("sub")
		Expect(decodeWhole(e.Bytes(), meta.DecodeAccessKey)).To(Equal(
			meta.AccessKey{ID: "AK", Secret: "SK", Subuser: "sub", Active: true}))
	})
	It("reads version 3, which added the active flag", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 2)
		e.String("AK")
		e.String("SK")
		e.String("")
		e.Bool(false)
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeAccessKey)).To(Equal(meta.AccessKey{ID: "AK", Secret: "SK"}))
	})
})

var _ = It("SubUser reads legacy version 1 without a compat byte or length", func() {
	e := denc.NewEncoder()
	legacyHeader(e, 1)
	e.String("swift")
	e.U32(0xf)
	Expect(decodeWhole(e.Bytes(), meta.DecodeSubUser)).To(Equal(meta.SubUser{Name: "swift", Perm: 0xf}))
})

var _ = DescribeTable("SubUser permissions render as perm_to_str does",
	func(perm uint32, want string) {
		Expect(mustMarshal(meta.SubUser{Name: "s", Perm: perm})).To(MatchJSON(`{"id":"s","permissions":` + mustMarshal(want) + `}`))
	},
	Entry("none", uint32(0), "<none>"),
	Entry("read", uint32(1), "read"),
	Entry("write", uint32(2), "write"),
	Entry("read-write", uint32(3), "read-write"),
	Entry("full control", uint32(0xf), "full-control"),
	Entry("read and read-acp", uint32(5), "read, read-acp"),
	Entry("both acp bits", uint32(0xc), "read-acp, write-acp"),
	Entry("full control and an unnamed bit", uint32(0x1f), "full-control"),
	// C++ leaves its buffer unwritten here; the Go form is empty.
	Entry("only unnamed bits", uint32(0x10), ""),
)

var _ = DescribeTable("Caps render each perm as RGWUserCaps::dump does",
	func(perm uint32, want string) {
		Expect(mustMarshal(meta.Caps{"users": perm})).To(MatchJSON(`[{"type":"users","perm":` + mustMarshal(want) + `}]`))
	},
	Entry("read and write is *", uint32(3), "*"),
	Entry("read", uint32(1), "read"),
	Entry("write", uint32(2), "write"),
	Entry("none", uint32(0), "<none>"),
	Entry("only unnamed bits", uint32(4), "<none>"),
)

var _ = It("empty Caps marshal as an empty list", func() {
	Expect(mustMarshal(meta.Caps(nil))).To(Equal("[]"))
})

// userInfoPrefix writes the fields every RGWUserInfo version below 6 shares
// after its header: auid (from v2), access key, secret, display name, email.
func userInfoPrefix(e *denc.Encoder, v uint8) {
	if v >= 2 {
		e.U64(0)
	}
	e.String("AK")
	e.String("SK")
	e.String("Display")
	e.String("e@x")
}

var _ = Describe("UserInfo legacy decoding", func() {
	It("reads version 1, deriving the user id and access key from the key pair", func() {
		e := denc.NewEncoder()
		legacyHeader(e, 1)
		userInfoPrefix(e, 1)
		want := meta.NewUserInfo()
		want.UserID = meta.UserID{ID: "AK"}
		want.DisplayName = "Display"
		want.Email = "e@x"
		want.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserInfo)).To(Equal(want))
	})
	It("reads version 5, which stored the user id but not the key maps", func() {
		e := denc.NewEncoder()
		legacyHeader(e, 5)
		userInfoPrefix(e, 5)
		e.String("swift-name")
		e.String("swift-key")
		e.String("alice")
		want := meta.NewUserInfo()
		want.UserID = meta.UserID{ID: "alice"}
		want.DisplayName = "Display"
		want.Email = "e@x"
		want.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserInfo)).To(Equal(want))
	})

	// v6 through v9: the key and subuser maps, suspended, and swift keys.
	keyed := func(e *denc.Encoder) {
		userInfoPrefix(e, 8)
		e.String("")
		e.String("")
		e.String("alice")
		denc.EncodeMap(e, map[string]meta.AccessKey{"AK2": {ID: "AK2", Secret: "SK2", Active: true}},
			(*denc.Encoder).String, func(e *denc.Encoder, k meta.AccessKey) { k.Encode(e, denc.Squid) })
		denc.EncodeMap(e, map[string]meta.SubUser{"swift": {Name: "swift", Perm: 1}},
			(*denc.Encoder).String, func(e *denc.Encoder, s meta.SubUser) { s.Encode(e, denc.Squid) })
		e.U8(1)
		denc.EncodeMap(e, map[string]meta.AccessKey{"alice:swift": {ID: "alice:swift", Secret: "SW", Subuser: "swift", Active: true}},
			(*denc.Encoder).String, func(e *denc.Encoder, k meta.AccessKey) { k.Encode(e, denc.Squid) })
	}
	keyedWant := func() meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "alice"}
		u.DisplayName = "Display"
		u.Email = "e@x"
		u.AccessKeys = map[string]meta.AccessKey{"AK2": {ID: "AK2", Secret: "SK2", Active: true}}
		u.SubUsers = map[string]meta.SubUser{"swift": {Name: "swift", Perm: 1}}
		u.Suspended = 1
		u.SwiftKeys = map[string]meta.AccessKey{"alice:swift": {ID: "alice:swift", Secret: "SW", Subuser: "swift", Active: true}}
		return u
	}
	It("reads version 8, the last without a compat byte or length", func() {
		e := denc.NewEncoder()
		legacyHeader(e, 8)
		keyed(e)
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserInfo)).To(Equal(keyedWant()))
	})
	It("reads version 9, framed but still without max_buckets", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(9, 9)
		keyed(e)
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserInfo)).To(Equal(keyedWant()))
	})
})

var _ = Describe("UserInfo containers", func() {
	encodeDecode := func(u meta.UserInfo) (meta.UserInfo, []byte) {
		GinkgoHelper()
		e := denc.NewEncoder()
		u.Encode(e, denc.Squid)
		return decodeWhole(e.Bytes(), meta.DecodeUserInfo), e.Bytes()
	}
	It("encodes tags in key order, equal keys in insertion order, as std::multimap does", func() {
		u := meta.NewUserInfo()
		u.Tags = []meta.UserTag{{Key: "b", Value: "1"}, {Key: "a", Value: "2"}, {Key: "b", Value: "0"}}
		got, b := encodeDecode(u)
		Expect(got.Tags).To(Equal([]meta.UserTag{{Key: "a", Value: "2"}, {Key: "b", Value: "1"}, {Key: "b", Value: "0"}}))

		tail := denc.NewEncoder()
		tail.U32(3)
		for _, kv := range [][2]string{{"a", "2"}, {"b", "1"}, {"b", "0"}} {
			tail.String(kv[0])
			tail.String(kv[1])
		}
		tail.U32(0) // group_ids
		Expect(b).To(HaveSuffix(string(tail.Bytes())))
	})
	It("sorts a tag list decoded out of order, keeping equal keys in wire order", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(23, 9)
		// Every field before tags, at its zero or default value.
		zero := meta.NewUserInfo()
		ze := denc.NewEncoder()
		zero.Encode(ze, denc.Squid)
		body := ze.Bytes()[6 : len(ze.Bytes())-8] // drop the header and the empty tags and group_ids
		e.Raw(body)
		e.U32(3)
		for _, kv := range [][2]string{{"b", "1"}, {"a", "2"}, {"b", "0"}} {
			e.String(kv[0])
			e.String(kv[1])
		}
		e.U32(0)
		e.EndStruct(f)
		got := decodeWhole(e.Bytes(), meta.DecodeUserInfo)
		Expect(got.Tags).To(Equal([]meta.UserTag{{Key: "a", Value: "2"}, {Key: "b", Value: "1"}, {Key: "b", Value: "0"}}))
	})
	It("encodes mfa and group ids sorted and unique, as std::set and flat_set do", func() {
		u := meta.NewUserInfo()
		u.MFAIDs = []string{"m2", "m1", "m2"}
		u.GroupIDs = []string{"g2", "g1", "g1"}
		got, _ := encodeDecode(u)
		Expect(got.MFAIDs).To(Equal([]string{"m1", "m2"}))
		Expect(got.GroupIDs).To(Equal([]string{"g1", "g2"}))
		Expect(u.MFAIDs).To(Equal([]string{"m2", "m1", "m2"}), "Encode must not reorder the caller's slice")
	})
	It("writes the first access and swift keys into the legacy key fields", func() {
		u := meta.NewUserInfo()
		u.AccessKeys = map[string]meta.AccessKey{
			"B": {ID: "B", Secret: "sb"},
			"A": {ID: "A", Secret: "sa"},
		}
		u.SwiftKeys = map[string]meta.AccessKey{"u:s": {ID: "u:s", Secret: "sw"}}
		e := denc.NewEncoder()
		u.Encode(e, denc.Squid)
		d := denc.NewDecoder(e.Bytes())
		h := d.BeginStruct(23)
		Expect(d.U64()).To(BeZero())
		Expect(d.String()).To(Equal("A"))
		Expect(d.String()).To(Equal("sa"))
		Expect(d.String()).To(BeEmpty())
		Expect(d.String()).To(BeEmpty())
		Expect(d.String()).To(Equal("u:s"))
		Expect(d.String()).To(Equal("sw"))
		d.EndStruct(h)
		Expect(d.Err()).NotTo(HaveOccurred())
	})
})

var _ = Describe("UserInfo JSON", func() {
	It("renders every field as RGWUserInfo::dump does", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: "t1", ID: "alice"}
		u.DisplayName = "Alice"
		u.Email = "a@x"
		u.SubUsers = map[string]meta.SubUser{"swift": {Name: "swift", Perm: 3}}
		u.AccessKeys = map[string]meta.AccessKey{
			"AK": {ID: "AK", Secret: "SK", Active: true},
			"SA": {ID: "SA", Secret: "SS", Subuser: "swift"},
		}
		u.SwiftKeys = map[string]meta.AccessKey{"t1$alice:swift": {ID: "t1$alice:swift", Secret: "SW", Subuser: "swift", Active: true}}
		u.Caps = meta.Caps{"users": 3, "buckets": 1}
		u.OpMask = 5
		u.System = 1
		u.Admin = 1
		u.DefaultPlacement = meta.PlacementRule{Name: "p", StorageClass: "COLD"}
		u.PlacementTags = []string{"x", "y"}
		u.TempURLKeys = map[int32]string{1: "k1", -1: "k0"}
		u.Type = meta.IdentityRoot
		u.MFAIDs = []string{"m1"}
		u.AccountID = "RGW1"
		u.Path = "/a/"
		u.CreateDate = meta.Time{Time: time.Date(2026, 9, 25, 1, 2, 3, 4000, time.UTC)}
		u.Tags = []meta.UserTag{{Key: "k", Value: "v"}}
		u.GroupIDs = []string{"g1"}
		Expect(mustMarshal(u)).To(MatchJSON(`{
			"user_id": "t1$alice",
			"display_name": "Alice",
			"email": "a@x",
			"suspended": 0,
			"max_buckets": 1000,
			"subusers": [{"id": "t1$alice:swift", "permissions": "read-write"}],
			"keys": [
				{"user": "t1$alice", "access_key": "AK", "secret_key": "SK", "active": true, "create_date": "0.000000"},
				{"user": "t1$alice:swift", "access_key": "SA", "secret_key": "SS", "active": false, "create_date": "0.000000"}
			],
			"swift_keys": [
				{"user": "t1$alice:swift", "secret_key": "SW", "active": true, "create_date": "0.000000"}
			],
			"caps": [{"type": "buckets", "perm": "read"}, {"type": "users", "perm": "*"}],
			"op_mask": "read, delete",
			"system": true,
			"admin": true,
			"default_placement": "p",
			"default_storage_class": "COLD",
			"placement_tags": ["x", "y"],
			"bucket_quota": {"enabled": false, "check_on_raw": false, "max_size": -1, "max_size_kb": 0, "max_objects": -1},
			"user_quota": {"enabled": false, "check_on_raw": false, "max_size": -1, "max_size_kb": 0, "max_objects": -1},
			"temp_url_keys": [{"key": -1, "val": "k0"}, {"key": 1, "val": "k1"}],
			"type": "root",
			"mfa_ids": ["m1"],
			"account_id": "RGW1",
			"path": "/a/",
			"create_date": "2026-09-25T01:02:03.000004Z",
			"tags": [{"key": "k", "val": "v"}],
			"group_ids": ["g1"]
		}`))
	})
	DescribeTable("names the identity type as RGWUserInfo::dump does",
		func(t meta.IdentityType, want string) {
			u := meta.NewUserInfo()
			u.Type = t
			var m map[string]any
			Expect(json.Unmarshal([]byte(mustMarshal(u)), &m)).To(Succeed())
			Expect(m).To(HaveKeyWithValue("type", want))
		},
		Entry("none", meta.IdentityNone, "none"),
		Entry("rgw", meta.IdentityRGW, "rgw"),
		Entry("keystone", meta.IdentityKeystone, "keystone"),
		Entry("ldap", meta.IdentityLDAP, "ldap"),
		Entry("role has no name", meta.IdentityRole, "none"),
		Entry("web has no name", meta.IdentityWeb, "none"),
		Entry("root", meta.IdentityRoot, "root"),
	)
	It("omits system and admin when clear, and renders an empty op mask as <none>", func() {
		u := meta.NewUserInfo()
		u.OpMask = 0
		var m map[string]any
		Expect(json.Unmarshal([]byte(mustMarshal(u)), &m)).To(Succeed())
		Expect(m).NotTo(HaveKey("system"))
		Expect(m).NotTo(HaveKey("admin"))
		Expect(m).To(HaveKeyWithValue("op_mask", "<none>"))
		// Squid and Tentacle dump the stored class, not the canonical STANDARD.
		Expect(m).To(HaveKeyWithValue("default_storage_class", ""))
	})
})

var _ = It("AccountInfo reads version 1, which had no bucket quota", func() {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	e.String("RGW1")
	e.String("t")
	e.String("n")
	e.String("e")
	meta.Quota{MaxSize: 4096, MaxObjects: 10, Enabled: true}.Encode(e, denc.Squid)
	for _, n := range []int32{1, 2, 3, 4, 5} {
		e.I32(n)
	}
	e.EndStruct(f)
	want := meta.NewAccountInfo()
	want.ID, want.Tenant, want.Name, want.Email = "RGW1", "t", "n", "e"
	want.Quota = meta.Quota{MaxSize: 4096, MaxObjects: 10, Enabled: true}
	want.MaxUsers, want.MaxRoles, want.MaxGroups, want.MaxBuckets, want.MaxAccessKeys = 1, 2, 3, 4, 5
	Expect(decodeWhole(e.Bytes(), meta.DecodeAccountInfo)).To(Equal(want))
	Expect(want.BucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: -1}))
})

var _ = Describe("UserObject", func() {
	It("encodes the UID then the user info, as the users.uid object stores them", func() {
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "alice"}
		o := meta.UserObject{UID: "alice", Info: info}

		want := denc.NewEncoder()
		meta.UID("alice").Encode(want, denc.Squid)
		info.Encode(want, denc.Squid)

		e := denc.NewEncoder()
		o.Encode(e, denc.Squid)
		Expect(e.Bytes()).To(Equal(want.Bytes()))
		Expect(e.Bytes()[:9]).To(Equal([]byte{5, 0, 0, 0, 'a', 'l', 'i', 'c', 'e'}))
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserObject)).To(Equal(o))
	})
	It("decodes an object holding only the UID with default user info", func() {
		e := denc.NewEncoder()
		meta.UID("alice").Encode(e, denc.Squid)
		Expect(decodeWhole(e.Bytes(), meta.DecodeUserObject)).To(Equal(meta.UserObject{UID: "alice", Info: meta.NewUserInfo()}))
	})
})
