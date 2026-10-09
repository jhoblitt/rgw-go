package meta_test

import (
	"encoding/json"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// richUser is a user every field of RGWUserInfo::dump carries a value for,
// its times at the microsecond precision the dump keeps.
func richUser() meta.UserInfo {
	u := meta.NewUserInfo()
	u.UserID = meta.UserID{Tenant: "t", ID: "alice"}
	u.DisplayName = "Alice"
	u.Email = "alice@example.com"
	u.Suspended = 1
	u.MaxBuckets = 7
	created := meta.Time{Time: time.Date(2026, 9, 29, 6, 45, 34, 50305000, time.UTC)}
	u.AccessKeys = map[string]meta.AccessKey{
		"AK1": {ID: "AK1", Secret: "SK1", Active: true, CreatedAt: created},
		"AK2": {ID: "AK2", Secret: "SK2", Subuser: "sub", Active: false},
	}
	u.SwiftKeys = map[string]meta.AccessKey{"t$alice:sub": {ID: "t$alice:sub", Secret: "SW", Subuser: "sub", Active: true}}
	u.SubUsers = map[string]meta.SubUser{"sub": {Name: "sub", Perm: 0xf}, "ro": {Name: "ro", Perm: 0x1}}
	u.Caps = meta.Caps{"users": meta.CapAll, "buckets": meta.CapRead}
	u.OpMask = 0x3
	u.System = 1
	u.DefaultPlacement = meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
	u.PlacementTags = []string{"fast", "slow"}
	u.BucketQuota = meta.Quota{MaxSize: 1 << 20, MaxObjects: 10, Enabled: true}
	u.UserQuota = meta.Quota{MaxSize: -1, MaxObjects: 99, CheckOnRaw: true}
	u.TempURLKeys = map[int32]string{0: "k0", 1: "k1"}
	u.Type = meta.IdentityRoot
	u.MFAIDs = []string{"m1", "m2"}
	u.AccountID = "RGW00000000000000001"
	u.Path = "/team/"
	u.CreateDate = created
	u.Tags = []meta.UserTag{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}
	u.GroupIDs = []string{"g1"}
	return u
}

var _ = Describe("UserInfo.UnmarshalJSON", func() {
	It("reads back what MarshalJSON writes", func() {
		u := richUser()
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		var got meta.UserInfo
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(u))
	})

	It("reads a document in RGWUserInfo::dump's layout and writes it back unchanged", func() {
		doc, err := os.ReadFile("testdata/user_info.json")
		Expect(err).NotTo(HaveOccurred())
		var u meta.UserInfo
		Expect(json.Unmarshal(doc, &u)).To(Succeed())
		Expect(u.UserID).To(Equal(meta.UserID{Tenant: "tenant1", ID: "alice"}))
		Expect(u.System).To(Equal(uint8(1)))
		Expect(u.AccessKeys).To(HaveKeyWithValue("AKIAALICE0000000000B", HaveField("Subuser", "swift")),
			"the subuser after the ':' of the key's user")
		Expect(u.SwiftKeys).To(HaveKeyWithValue("tenant1$alice:swift", HaveField("Subuser", "swift")),
			"a Swift key keyed by its user")
		again, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(MatchJSON(doc))
	})

	It("resets an absent member to its type's default, as JSONDecoder::decode_json does", func() {
		var u meta.UserInfo
		Expect(json.Unmarshal([]byte(`{"user_id":"bob"}`), &u)).To(Succeed())
		Expect(u.UserID).To(Equal(meta.UserID{ID: "bob"}))
		Expect(u.MaxBuckets).To(BeZero(), "max_buckets")
		Expect(u.OpMask).To(BeZero(), "op_mask, parsed from the empty string")
		Expect(u.Path).To(BeEmpty(), "path")
		Expect(u.BucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: -1}), "RGWQuotaInfo's defaults")
		Expect(u.Type).To(Equal(meta.IdentityNone))
	})

	It("reads a Swift key without active as inactive, and an S3 key without it as active", func() {
		var u meta.UserInfo
		Expect(json.Unmarshal([]byte(`{"user_id":"a",`+
			`"keys":[{"access_key":"AK","secret_key":"SK"}],`+
			`"swift_keys":[{"user":"a:s","secret_key":"SK"}]}`), &u)).To(Succeed())
		Expect(u.AccessKeys["AK"].Active).To(BeTrue(), "the S3 form changes active only when present")
		Expect(u.SwiftKeys["a:s"].Active).To(BeFalse(), "the Swift form resets an absent active to false")
		Expect(json.Unmarshal([]byte(`{"user_id":"a","swift_keys":[{"user":"a:s","secret_key":"SK","active":true}]}`), &u)).To(Succeed())
		Expect(u.SwiftKeys["a:s"].Active).To(BeTrue())
	})

	DescribeTable("refuses a document decode_json throws on or that cannot mean what it says",
		func(doc string) {
			var u meta.UserInfo
			Expect(json.Unmarshal([]byte(doc), &u)).To(MatchError(meta.ErrBadJSONValue), doc)
		},
		Entry("no user_id, which is mandatory", `{"display_name":"x"}`),
		Entry("a max_buckets past int's range", `{"user_id":"a","max_buckets":2147483648}`),
		Entry("a max_buckets that is no number", `{"user_id":"a","max_buckets":"ten"}`),
		Entry("a suspended that is no boolean", `{"user_id":"a","suspended":"yes"}`),
		Entry("keys that are no array", `{"user_id":"a","keys":{"user":"a"}}`),
		Entry("keys that are null", `{"user_id":"a","keys":null}`),
		Entry("a temp url key entry that is no object", `{"user_id":"a","temp_url_keys":[5]}`),
		Entry("a key without its secret, which is mandatory", `{"user_id":"a","keys":[{"access_key":"AK"}]}`),
		Entry("a key without its id, which is mandatory", `{"user_id":"a","keys":[{"secret_key":"SK"}]}`),
		Entry("a key whose id is empty", `{"user_id":"a","keys":[{"access_key":"","secret_key":"SK"}]}`),
		Entry("a Swift key without its user", `{"user_id":"a","swift_keys":[{"secret_key":"SK"}]}`),
		Entry("a Swift key named by subuser, which radosgw leaves without an id", `{"user_id":"a","swift_keys":[{"subuser":"s","secret_key":"SK"}]}`),
		Entry("a key's create_date that is no time", `{"user_id":"a","keys":[{"access_key":"AK","secret_key":"SK","create_date":"tomorrow"}]}`),
		Entry("a temp url key past int's range", `{"user_id":"a","temp_url_keys":[{"key":4294967296,"val":"x"}]}`),
		Entry("caps that are no array", `{"user_id":"a","caps":"users=*"}`),
		Entry("a subuser that is no object", `{"user_id":"a","subusers":["a:s"]}`),
		Entry("a display name that is an object", `{"user_id":"a","display_name":{}}`),
		Entry("a quota whose size overflows", `{"user_id":"a","user_quota":{"max_size":9223372036854775808}}`),
		Entry("a create_date with no fraction, which parse_date's sscanf refuses", `{"user_id":"a","create_date":"5"}`),
	)
})

var _ = Describe("Caps.UnmarshalJSON", func() {
	It("reads RGWUserCaps' list, each perm through parse_cap_perm", func() {
		var c meta.Caps
		Expect(json.Unmarshal([]byte(`[{"type":"users","perm":"read, write"}]`), &c)).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"users": meta.CapAll}))
		Expect(json.Unmarshal([]byte(`[{"type":"buckets","perm":"*"},{"type":"usage","perm":"nonsense"}]`), &c)).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"buckets": meta.CapAll, "usage": 0}), "an unknown word is no permission")
	})
})

var _ = Describe("UserCompleteInfo", func() {
	It("writes the user's fields, then attrs, and reads them back", func() {
		u := richUser()
		uc := meta.UserCompleteInfo{Info: u, Attrs: meta.AttrsJSON{"user.rgw.x": []byte("v")}}
		data, err := json.Marshal(uc)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(HavePrefix(`{"user_id":"t$alice",`))
		Expect(string(data)).To(HaveSuffix(`,"group_ids":["g1"],"attrs":[{"key":"user.rgw.x","val":"dg=="}]}`))
		var got meta.UserCompleteInfo
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got).To(Equal(meta.UserCompleteInfo{Info: u, Attrs: uc.Attrs, HasAttrs: true}))
	})

	It("reads a document without attrs as having none", func() {
		var got meta.UserCompleteInfo
		Expect(json.Unmarshal([]byte(`{"user_id":"bob"}`), &got)).To(Succeed())
		Expect(got.HasAttrs).To(BeFalse())
		Expect(got.Attrs).To(BeNil())
		Expect(json.Unmarshal([]byte(`{"user_id":"bob","attrs":[]}`), &got)).To(Succeed())
		Expect(got.HasAttrs).To(BeTrue(), "an empty attrs is still attrs")
	})

	It("refuses an attr whose value is not base64", func() {
		var got meta.UserCompleteInfo
		Expect(json.Unmarshal([]byte(`{"user_id":"bob","attrs":[{"key":"user.rgw.x","val":"%%"}]}`), &got)).
			To(MatchError(meta.ErrBadJSONValue))
	})
})
