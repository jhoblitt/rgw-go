package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("caps parsing", func() {
	DescribeTable("ParseCap is RGWUserCaps::get_cap",
		func(in, wantType string, wantPerm uint32, wantErr error) {
			typ, perm, err := meta.ParseCap(in)
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr), "%q", in)
				return
			}
			Expect(err).NotTo(HaveOccurred(), "%q", in)
			Expect(typ).To(Equal(wantType), "%q", in)
			Expect(perm).To(Equal(wantPerm), "%q", in)
		},
		Entry("read", "users=read", "users", meta.CapRead, nil),
		Entry("star", "buckets=*", "buckets", meta.CapAll, nil),
		Entry("read, write", "usage=read, write", "usage", meta.CapAll, nil),
		Entry("perms split on a space as get_str_list splits them", "usage=read write", "usage", meta.CapAll, nil),
		Entry("a second '=' splits perms too", "usage=read=write", "usage", meta.CapAll, nil),
		Entry("trimmed type", " metadata =write", "metadata", meta.CapWrite, nil),
		Entry("type trimmed of a tab", "\tzone=read", "zone", meta.CapRead, nil),
		Entry("no perm", "info=", "info", uint32(0), nil),
		Entry("unknown word is ignored", "zone=bogus", "zone", uint32(0), nil),
		Entry("perm words are case-sensitive", "zone=READ", "zone", uint32(0), nil),
		Entry("unknown type", "kittens=read", "", uint32(0), meta.ErrInvalidCap),
		Entry("no equals", "users", "", uint32(0), meta.ErrInvalidCap),
		Entry("empty", "", "", uint32(0), meta.ErrInvalidCap),
	)
	It("knows every type is_valid_cap_type lists, and no other", func() {
		for _, t := range []string{
			"user", "users", "buckets", "metadata", "info", "usage", "zone", "bilog", "mdlog", "datalog",
			"roles", "user-policy", "amz-cache", "oidc-provider", "user-info-without-keys", "ratelimit", "accounts",
		} {
			Expect(meta.ValidCapType(t)).To(BeTrue(), "%q", t)
		}
		for _, t := range []string{"", "Users", "account", "*"} {
			Expect(meta.ValidCapType(t)).To(BeFalse(), "%q", t)
		}
	})
	It("adds and removes ';'-separated caps as add_from_string and remove_from_string do", func() {
		c := meta.Caps{}
		Expect(c.AddString("users=read;buckets=*")).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"users": meta.CapRead, "buckets": meta.CapAll}))
		Expect(c.AddString("users=write")).To(Succeed())
		Expect(c["users"]).To(Equal(meta.CapAll))
		Expect(c.RemoveString("users=read;buckets=*")).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"users": meta.CapWrite}))
		Expect(c.RemoveString("nosuch=read")).To(MatchError(meta.ErrInvalidCap))
		Expect(c.RemoveString("info=read")).To(Succeed(), "removing an absent type is a no-op")
	})
	It("adds to the nil Caps a new user and a capless stored user carry", func() {
		var zero meta.Caps
		Expect(zero.AddString("users=read")).To(Succeed(), "a zero Caps")
		Expect(zero).To(Equal(meta.Caps{"users": meta.CapRead}), "a zero Caps")

		e := denc.NewEncoder()
		meta.Caps{}.Encode(e, denc.Squid)
		decoded := meta.DecodeCaps(denc.NewDecoder(e.Bytes()))
		Expect(decoded.AddString("buckets=*")).To(Succeed(), "a decoded empty Caps")
		Expect(decoded).To(Equal(meta.Caps{"buckets": meta.CapAll}), "a decoded empty Caps")

		u := meta.NewUserInfo()
		Expect(u.Caps.AddString("usage=write")).To(Succeed(), "NewUserInfo's Caps")
		Expect(u.Caps).To(Equal(meta.Caps{"usage": meta.CapWrite}), "NewUserInfo's Caps")
	})
	It("leaves a nil Caps nil when nothing parses", func() {
		var zero meta.Caps
		Expect(zero.AddString("kittens=read")).To(MatchError(meta.ErrInvalidCap))
		Expect(zero).To(BeNil())
	})
	It("adds a type with no perm as get_cap's zero", func() {
		c := meta.Caps{}
		Expect(c.AddString("info=")).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"info": 0}))
	})
	DescribeTable("splits on ';' as add_from_string's loop does",
		func(in string, want meta.Caps, wantErr error) {
			c := meta.Caps{}
			err := c.AddString(in)
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr), "%q", in)
			} else {
				Expect(err).NotTo(HaveOccurred(), "%q", in)
			}
			Expect(c).To(Equal(want), "%q", in)
		},
		Entry("a trailing ';' ends the loop", "users=read;", meta.Caps{"users": meta.CapRead}, nil),
		Entry("an empty string parses one empty cap", "", meta.Caps{}, meta.ErrInvalidCap),
		Entry("a lone ';' parses one empty cap", ";", meta.Caps{}, meta.ErrInvalidCap),
		Entry("an empty cap mid-string fails after the caps before it", "users=read;;buckets=read", meta.Caps{"users": meta.CapRead}, meta.ErrInvalidCap),
	)
	It("checks caps as check_cap does", func() {
		c := meta.Caps{"users": meta.CapRead}
		Expect(c.Check("users", meta.CapRead)).To(BeTrue())
		Expect(c.Check("users", meta.CapWrite)).To(BeFalse())
		Expect(c.Check("buckets", meta.CapRead)).To(BeFalse())
		Expect(c.Check("buckets", 0)).To(BeFalse(), "a type the caps lack fails whatever the perm")
		Expect(c.Check("users", 0)).To(BeTrue(), "a held type passes a zero perm")
	})
	DescribeTable("op types",
		func(in string, want uint32, wantStr string) {
			Expect(meta.ParseOpTypeList(in)).To(Equal(want), "%q", in)
			Expect(meta.OpTypeString(want)).To(Equal(wantStr), "%#x", want)
		},
		Entry("all", "read, write, delete", uint32(meta.OpTypeAll), "read, write, delete"),
		Entry("delete only", "delete", uint32(0x4), "delete"),
		Entry("star", "*", uint32(meta.OpTypeAll), "read, write, delete"),
		Entry("bogus is ignored", "bogus", uint32(0), "<none>"),
	)
	DescribeTable("ParseSubuserPerm is rgw_str_to_perm",
		func(in string, want uint32) { Expect(meta.ParseSubuserPerm(in)).To(Equal(want), "%q", in) },
		Entry("empty", "", uint32(0)),
		Entry("read", "read", uint32(0x1)),
		Entry("write", "Write", uint32(0x2)),
		Entry("readwrite", "readwrite", uint32(0x3)),
		Entry("full", "FULL", uint32(0xf)),
		Entry("invalid", "read-write", meta.PermInvalid),
	)
	It("renders perms as perm_to_str", func() {
		Expect(meta.PermString(0x3)).To(Equal("read-write"))
		Expect(meta.PermString(0xf)).To(Equal("full-control"))
		Expect(meta.PermString(0)).To(Equal("<none>"))
	})
})
