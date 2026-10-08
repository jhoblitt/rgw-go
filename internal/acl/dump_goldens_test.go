package acl_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// dumper is a type with a C++ dump().
type dumper interface {
	Dump(f formatter.Formatter, rel denc.Release)
}

// dumpGolden renders v as ceph-dencoder's dump_json does
// (ceph_dencoder.cc:189-194): a pretty JSONFormatter, the value's dump()
// inside an "object" section, then a line break. The corpus JSON comes from
// the v19 dencoder.
func dumpGolden(v dumper) string {
	GinkgoHelper()
	f := formatter.NewJSON(true)
	f.OpenObjectSection("object")
	v.Dump(f, denc.Squid)
	f.CloseSection()
	Expect(f.Err()).NotTo(HaveOccurred())
	return string(f.Bytes()) + "\n"
}

// dumpGoldens compares the dump of every corpus object of typ with the
// dencoder's bytes, which carry dump()'s key order and number formatting.
func dumpGoldens[T dumper](typ string, decode func(*denc.Decoder) T) {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty(), "no goldens for %s", typ)
	for _, c := range cs {
		Expect(dumpGolden(decodeWhole(c.Bin, decode))).To(Equal(string(c.JSON)), c.Type+"/"+c.Archive+"/"+c.Name)
	}
}

var _ = Describe("Dump against the corpus", func() {
	It("RGWAccessControlPolicy", func() { dumpGoldens("RGWAccessControlPolicy", acl.DecodePolicy) })
	It("RGWAccessControlList", func() { dumpGoldens("RGWAccessControlList", acl.DecodeList) })
	It("ACLOwner", func() { dumpGoldens("ACLOwner", acl.DecodeOwner) })
	It("ACLGrant", func() { dumpGoldens("ACLGrant", acl.DecodeGrant) })
	It("ACLPermission", func() { dumpGoldens("ACLPermission", acl.DecodePermission) })
	It("ACLGranteeType", func() { dumpGoldens("ACLGranteeType", acl.DecodeGranteeType) })
})

var _ = Describe("Dump", func() {
	It("writes every grantee kind's fields and the group as a signed int", func() {
		l := acl.List{
			UserMap:  map[string]int32{"bob": 1, "alice": 15},
			GroupMap: map[uint32]int32{2: 1, 1: 2},
			Grants: []acl.GrantEntry{
				{Key: "bob", Grant: acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermRead}},
				{Key: "", Grant: acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermWrite}},
				{Key: "a@b", Grant: acl.Grant{Type: acl.GranteeEmail, Email: "a@b", Permission: acl.PermReadACP}},
				{Key: "", Grant: acl.Grant{Type: acl.GranteeReferer, URLSpec: "*.x", Permission: acl.PermRead}},
				{Key: "", Grant: acl.Grant{Type: 9, Permission: acl.PermRead}},
			},
		}
		f := formatter.NewJSON(false)
		f.OpenObjectSection("")
		l.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(string(f.Bytes())).To(Equal(`{"acl_user_map":[{"user":"alice","acl":15},{"user":"bob","acl":1}],` +
			`"acl_group_map":[{"group":1,"acl":2},{"group":2,"acl":1}],"grant_map":[` +
			`{"id":"","grant":{"type":{"type":2},"group":1,"permission":{"flags":2}}},` +
			`{"id":"","grant":{"type":{"type":4},"url_spec":"*.x","permission":{"flags":1}}},` +
			`{"id":"","grant":{"type":{"type":3},"permission":{"flags":1}}},` +
			`{"id":"a@b","grant":{"type":{"type":1},"email":"a@b","permission":{"flags":4}}},` +
			`{"id":"bob","grant":{"type":{"type":0},"id":"bob","name":"Bob","permission":{"flags":1}}}]}`))
	})
})
