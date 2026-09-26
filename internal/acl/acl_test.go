package acl_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The fixtures below are built field by field from the C++ encode bodies, never
// through the Go encoders, for the versions the corpus lacks and for the
// fields it holds only at their defaults: email and referer grantees, and the
// referer list.

func decodeWhole[T any](b []byte, dec func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := dec(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

type encoder interface {
	Encode(e *denc.Encoder, r denc.Release)
}

func encodeAt(v encoder, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

func mustMarshal(v any) string {
	GinkgoHelper()
	b, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// releases are the releases every encoder must agree on: the ACL encodings
// are identical from Squid through main.
var releases = []denc.Release{denc.Squid, denc.Tentacle}

// permission writes ACLPermission at version 2; a version below 2 carries
// neither compat byte nor length.
func permission(e *denc.Encoder, v uint8, flags int32) {
	if v < 2 {
		e.U8(v)
		e.I32(flags)
		return
	}
	f := e.BeginStruct(v, 2)
	e.I32(flags)
	e.EndStruct(f)
}

// granteeType writes ACLGranteeType as permission writes ACLPermission.
func granteeType(e *denc.Encoder, v uint8, t uint32) {
	if v < 2 {
		e.U8(v)
		e.U32(t)
		return
	}
	f := e.BeginStruct(v, 2)
	e.U32(t)
	e.EndStruct(f)
}

// grantFields is every field ACLGrant writes, whatever its grantee.
type grantFields struct {
	typ                  uint32
	id, uri, email, name string
	perm                 int32
	group                uint32
	urlSpec              string
}

// grant writes ACLGrant at struct version v: version 5 adds the referer URL
// spec, and a version below 3 has neither compat byte nor length.
func grant(e *denc.Encoder, v uint8, g grantFields) {
	var f denc.Frame
	if v < 3 {
		e.U8(v)
	} else {
		f = e.BeginStruct(v, 3)
	}
	granteeType(e, 2, g.typ)
	e.String(g.id)
	e.String(g.uri)
	e.String(g.email)
	permission(e, 2, g.perm)
	e.String(g.name)
	e.U32(g.group)
	if v >= 5 {
		e.String(g.urlSpec)
	}
	if v >= 3 {
		e.EndStruct(f)
	}
}

func grantBytes(v uint8, g grantFields) []byte {
	e := denc.NewEncoder()
	grant(e, v, g)
	return e.Bytes()
}

// referer writes ACLReferer, ENCODE_START(1, 1).
func referer(e *denc.Encoder, spec string, perm uint32) {
	f := e.BeginStruct(1, 1)
	e.String(spec)
	e.U32(perm)
	e.EndStruct(f)
}

var _ = Describe("Grant", func() {
	It("encodes an email grantee with the address in the email field", func() {
		want := grantBytes(5, grantFields{typ: 1, email: "a@example.com", perm: 1})
		g := acl.Grant{Type: acl.GranteeEmail, Email: "a@example.com", Permission: acl.PermRead}
		for _, r := range releases {
			Expect(encodeAt(g, r)).To(Equal(want))
		}
		Expect(decodeWhole(want, acl.DecodeGrant)).To(Equal(g))
	})
	It("encodes a referer grantee with its URL spec last", func() {
		want := grantBytes(5, grantFields{typ: 4, perm: 1, urlSpec: ".example.com"})
		g := acl.Grant{Type: acl.GranteeReferer, URLSpec: ".example.com", Permission: acl.PermRead}
		for _, r := range releases {
			Expect(encodeAt(g, r)).To(Equal(want))
		}
		Expect(decodeWhole(want, acl.DecodeGrant)).To(Equal(g))
	})
	It("encodes a canonical user with its id and name, and the group as none", func() {
		want := grantBytes(5, grantFields{typ: 0, id: "t$alice", name: "Alice", perm: 15})
		g := acl.Grant{Type: acl.GranteeCanonUser, ID: "t$alice", Name: "Alice", Permission: acl.PermFullControl}
		Expect(encodeAt(g, denc.Squid)).To(Equal(want))
	})
	DescribeTable("writes only the fields its grantee type holds",
		func(typ acl.GranteeType, want grantFields) {
			g := acl.Grant{
				Type: typ, ID: "alice", Email: "a@example.com", Name: "Alice", URLSpec: "*",
				Group: acl.GroupAllUsers, Permission: acl.PermRead,
			}
			Expect(encodeAt(g, denc.Squid)).To(Equal(grantBytes(5, want)))
		},
		Entry("canonical user", acl.GranteeCanonUser, grantFields{typ: 0, id: "alice", name: "Alice", perm: 1}),
		Entry("email", acl.GranteeEmail, grantFields{typ: 1, email: "a@example.com", perm: 1}),
		Entry("group", acl.GranteeGroup, grantFields{typ: 2, group: 1, perm: 1}),
		Entry("unknown", acl.GranteeUnknown, grantFields{typ: 3, perm: 1}),
		Entry("referer", acl.GranteeReferer, grantFields{typ: 4, urlSpec: "*", perm: 1}),
	)
	It("writes a type beyond the grantee variant as unknown", func() {
		g := acl.Grant{Type: 9, ID: "x", Permission: acl.PermWrite}
		Expect(encodeAt(g, denc.Squid)).To(Equal(grantBytes(5, grantFields{typ: 3, perm: 2})))
	})
	DescribeTable("decodes only the fields its grantee type holds",
		func(typ uint32, want acl.Grant) {
			b := grantBytes(5, grantFields{
				typ: typ, id: "alice", uri: "http://acs.amazonaws.com/groups/global/AllUsers",
				email: "a@example.com", perm: 3, name: "Alice", group: 2, urlSpec: "*",
			})
			Expect(decodeWhole(b, acl.DecodeGrant)).To(Equal(want))
		},
		Entry("canonical user", uint32(0), acl.Grant{Type: acl.GranteeCanonUser, ID: "alice", Name: "Alice", Permission: 3}),
		Entry("email", uint32(1), acl.Grant{Type: acl.GranteeEmail, Email: "a@example.com", Permission: 3}),
		Entry("group", uint32(2), acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAuthenticatedUsers, Permission: 3}),
		Entry("unknown", uint32(3), acl.Grant{Type: acl.GranteeUnknown, Permission: 3}),
		Entry("referer", uint32(4), acl.Grant{Type: acl.GranteeReferer, URLSpec: "*", Permission: 3}),
		Entry("a type beyond the variant, as unknown", uint32(7), acl.Grant{Type: acl.GranteeUnknown, Permission: 3}),
	)
	DescribeTable("normalizes a canonical user id through parse_owner and to_string",
		func(stored, want string) {
			b := grantBytes(5, grantFields{typ: 0, id: stored})
			Expect(decodeWhole(b, acl.DecodeGrant).ID).To(Equal(want))
		},
		Entry("a bare id", "alice", "alice"),
		Entry("a tenant", "t$alice", "t$alice"),
		Entry("a namespace", "$ns$alice", "$ns$alice"),
		Entry("an empty tenant", "$alice", "alice"),
		Entry("a tenant with an empty namespace", "t$$alice", "t$alice"),
		Entry("an account", "RGW12345678901234567", "RGW12345678901234567"),
	)
	// Squid and later read a group field at every version, where Reef read it
	// only above version 1, so this pins Squid's decode of a header without
	// compat byte or length rather than historical version 1 bytes.
	It("decodes version 1 as Squid does: no compat byte, length or URL spec, but a group", func() {
		b := grantBytes(1, grantFields{typ: 1, email: "a@example.com", perm: 8})
		Expect(decodeWhole(b, acl.DecodeGrant)).To(Equal(
			acl.Grant{Type: acl.GranteeEmail, Email: "a@example.com", Permission: acl.PermWriteACP}))
	})
	It("encodes a canonical user id in its to_string form", func() {
		g := acl.Grant{Type: acl.GranteeCanonUser, ID: "$alice", Permission: acl.PermRead}
		Expect(encodeAt(g, denc.Squid)).To(Equal(grantBytes(5, grantFields{typ: 0, id: "alice", perm: 1})))
	})
	It("reads version 4, which had no URL spec", func() {
		b := grantBytes(4, grantFields{typ: 4, perm: 1})
		Expect(decodeWhole(b, acl.DecodeGrant)).To(Equal(acl.Grant{Type: acl.GranteeReferer, Permission: acl.PermRead}))
	})
	It("reads a grantee type and permission from before their compat byte and length", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(5, 3)
		granteeType(e, 1, 2)
		e.String("")
		e.String("")
		e.String("")
		permission(e, 1, 4)
		e.String("")
		e.U32(1)
		e.String("")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), acl.DecodeGrant)).To(Equal(
			acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermReadACP}))
	})
	DescribeTable("renders the fields of its grantee type as ACLGrant::dump does",
		func(g acl.Grant, want string) {
			Expect(mustMarshal(g)).To(MatchJSON(want))
		},
		Entry("canonical user", acl.Grant{Type: acl.GranteeCanonUser, ID: "alice", Name: "Alice", Email: "x", Permission: 15},
			`{"type":{"type":0},"id":"alice","name":"Alice","permission":{"flags":15}}`),
		Entry("email", acl.Grant{Type: acl.GranteeEmail, Email: "a@example.com", ID: "x", Permission: 1},
			`{"type":{"type":1},"email":"a@example.com","permission":{"flags":1}}`),
		Entry("group, as a signed int", acl.Grant{Type: acl.GranteeGroup, Group: 0xffffffff, Permission: 2},
			`{"type":{"type":2},"group":-1,"permission":{"flags":2}}`),
		Entry("unknown", acl.Grant{Type: acl.GranteeUnknown, ID: "x", Permission: 0},
			`{"type":{"type":3},"permission":{"flags":0}}`),
		Entry("referer", acl.Grant{Type: acl.GranteeReferer, URLSpec: "*", Permission: 1},
			`{"type":{"type":4},"url_spec":"*","permission":{"flags":1}}`),
		Entry("a type beyond the variant, as unknown", acl.Grant{Type: 9, Permission: 1},
			`{"type":{"type":3},"permission":{"flags":1}}`),
	)
})

var _ = It("NewGranteeType is the ACLGranteeType default, unknown", func() {
	Expect(acl.NewGranteeType()).To(Equal(acl.GranteeUnknown))
	Expect(uint32(acl.NewGranteeType())).To(Equal(uint32(3)))
})

var _ = It("Permission renders its flags as the signed int C++ stores", func() {
	Expect(mustMarshal(acl.Permission(0x80000000))).To(MatchJSON(`{"flags":-2147483648}`))
})

var _ = Describe("Referer", func() {
	It("round-trips ENCODE_START(1, 1)", func() {
		e := denc.NewEncoder()
		referer(e, ".example.com", 1)
		r := acl.Referer{URLSpec: ".example.com", Perm: 1}
		Expect(encodeAt(r, denc.Squid)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), acl.DecodeReferer)).To(Equal(r))
	})
})

// listHeader opens RGWAccessControlList at version v and writes
// maps_initialized; a version below 3 has neither compat byte nor length.
func listHeader(e *denc.Encoder, v uint8, mapsInitialized bool) (denc.Frame, bool) {
	var f denc.Frame
	framed := v >= 3
	if framed {
		f = e.BeginStruct(v, 3)
	} else {
		e.U8(v)
	}
	e.Bool(mapsInitialized)
	return f, framed
}

var _ = Describe("List", func() {
	It("encodes the referer list after the group map", func() {
		e := denc.NewEncoder()
		f, _ := listHeader(e, 4, true)
		e.U32(1) // acl_user_map
		e.String("alice")
		e.I32(15)
		e.U32(1) // grant_map
		e.String("alice")
		grant(e, 5, grantFields{typ: 0, id: "alice", perm: 15})
		e.U32(1) // acl_group_map
		e.U32(1)
		e.I32(1)
		e.U32(2) // referer_list
		referer(e, "*", 1)
		referer(e, ".example.com", 3)
		e.EndStruct(f)

		l := acl.List{
			UserMap:     map[string]int32{"alice": 15},
			GroupMap:    map[uint32]int32{1: 1},
			RefererList: []acl.Referer{{URLSpec: "*", Perm: 1}, {URLSpec: ".example.com", Perm: 3}},
			Grants:      []acl.GrantEntry{{Key: "alice", Grant: acl.Grant{ID: "alice", Permission: 15}}},
		}
		for _, r := range releases {
			Expect(encodeAt(l, r)).To(Equal(e.Bytes()))
		}
		Expect(decodeWhole(e.Bytes(), acl.DecodeList)).To(Equal(l))
	})
	It("keeps grants in multimap order: by key, equal keys in insertion order", func() {
		gr := func(p acl.Permission) acl.Grant { return acl.Grant{Type: acl.GranteeGroup, Permission: p} }
		e := denc.NewEncoder()
		f, _ := listHeader(e, 4, true)
		e.U32(0)
		e.U32(4)
		for i, k := range []string{"b", "", "b", ""} {
			e.String(k)
			grant(e, 5, grantFields{typ: 2, perm: int32(i + 1)}) //nolint:gosec // i is below 4
		}
		e.U32(0)
		e.U32(0)
		e.EndStruct(f)

		want := []acl.GrantEntry{{"", gr(2)}, {"", gr(4)}, {"b", gr(1)}, {"b", gr(3)}}
		l := decodeWhole(e.Bytes(), acl.DecodeList)
		Expect(l.Grants).To(Equal(want))

		unsorted := acl.List{Grants: []acl.GrantEntry{{"b", gr(1)}, {"", gr(2)}, {"b", gr(3)}, {"", gr(4)}}}
		Expect(encodeAt(unsorted, denc.Squid)).To(Equal(encodeAt(acl.List{Grants: want}, denc.Squid)))
		Expect(mustMarshal(unsorted)).To(Equal(mustMarshal(acl.List{Grants: want})))
		Expect(unsorted.Grants[0].Key).To(Equal("b"), "encoding must not reorder the caller's slice")
	})
	It("reads version 2, which had no referer list", func() {
		e := denc.NewEncoder()
		listHeader(e, 2, true)
		e.U32(0)
		e.U32(0)
		e.U32(1)
		e.U32(2)
		e.I32(1)
		Expect(decodeWhole(e.Bytes(), acl.DecodeList)).To(Equal(acl.List{GroupMap: map[uint32]int32{2: 1}}))
	})
	It("reads version 1 with its maps initialized as stored, without a group map", func() {
		e := denc.NewEncoder()
		listHeader(e, 1, true)
		e.U32(0)
		e.U32(1)
		e.String("")
		grant(e, 1, grantFields{typ: 2, group: 1, perm: 1})
		Expect(decodeWhole(e.Bytes(), acl.DecodeList)).To(Equal(acl.List{
			Grants: []acl.GrantEntry{{"", acl.Grant{Type: acl.GranteeGroup, Group: 1, Permission: 1}}},
		}))
	})
	It("rebuilds version 1 maps from the grants when they were not initialized", func() {
		e := denc.NewEncoder()
		listHeader(e, 1, false)
		e.U32(1) // acl_user_map, merged with the grants
		e.String("alice")
		e.I32(4)
		entries := []struct {
			key string
			g   grantFields
		}{
			{"", grantFields{typ: 2, group: 2, perm: 1}},
			{"", grantFields{typ: 4, perm: 1}},
			{"", grantFields{typ: 3, perm: 8}},
			{"a@example.com", grantFields{typ: 1, email: "a@example.com", perm: 2}},
			{"alice", grantFields{typ: 0, id: "alice", perm: 1}},
			{"alice", grantFields{typ: 0, id: "alice", perm: 2}},
		}
		e.U32(uint32(len(entries)))
		for _, en := range entries {
			e.String(en.key)
			grant(e, 1, en.g)
		}
		l := decodeWhole(e.Bytes(), acl.DecodeList)
		Expect(l.UserMap).To(Equal(map[string]int32{"alice": 7, "a@example.com": 2}))
		Expect(l.GroupMap).To(Equal(map[uint32]int32{2: 1}))
		// A version 1 grant has no URL spec, so the referer is empty and not the wildcard.
		Expect(l.RefererList).To(Equal([]acl.Referer{{URLSpec: "", Perm: 1}}))
		Expect(l.Grants).To(HaveLen(len(entries)))
	})
	It("rebuilds the all-users group from a wildcard referer grant", func() {
		e := denc.NewEncoder()
		listHeader(e, 1, false)
		e.U32(0)
		e.U32(1)
		e.String("")
		grant(e, 5, grantFields{typ: 4, urlSpec: "*", perm: 1})
		l := decodeWhole(e.Bytes(), acl.DecodeList)
		Expect(l.GroupMap).To(Equal(map[uint32]int32{acl.GroupAllUsers: 1}))
		Expect(l.RefererList).To(Equal([]acl.Referer{{URLSpec: "*", Perm: 1}}))
	})
	It("renders RGWAccessControlList::dump, which leaves out the referer list", func() {
		l := acl.List{
			UserMap:     map[string]int32{"b": 2, "a": -1},
			GroupMap:    map[uint32]int32{2: 1, 1: 3},
			RefererList: []acl.Referer{{URLSpec: "*", Perm: 1}},
			Grants:      []acl.GrantEntry{{"a", acl.Grant{Type: acl.GranteeUnknown, Permission: 1}}},
		}
		Expect(mustMarshal(l)).To(Equal(`{"acl_user_map":[{"user":"a","acl":-1},{"user":"b","acl":2}],` +
			`"acl_group_map":[{"group":1,"acl":3},{"group":2,"acl":1}],` +
			`"grant_map":[{"id":"a","grant":{"type":{"type":3},"permission":{"flags":1}}}]}`))
		Expect(mustMarshal(acl.List{})).To(Equal(`{"acl_user_map":[],"acl_group_map":[],"grant_map":[]}`))
	})
})

var _ = Describe("Owner", func() {
	It("reads version 1, which had no compat byte or length", func() {
		e := denc.NewEncoder()
		e.U8(1)
		e.String("$alice")
		e.String("Alice")
		Expect(decodeWhole(e.Bytes(), acl.DecodeOwner)).To(Equal(acl.Owner{ID: "alice", DisplayName: "Alice"}))
	})
	It("encodes ENCODE_START(3, 2) around the id and display name", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 2)
		e.String("t$alice")
		e.String("Alice")
		e.EndStruct(f)
		for _, r := range releases {
			Expect(encodeAt(acl.Owner{ID: "t$alice", DisplayName: "Alice"}, r)).To(Equal(e.Bytes()))
			Expect(encodeAt(acl.Owner{ID: "t$$alice", DisplayName: "Alice"}, r)).To(Equal(e.Bytes()),
				"the id is written in its to_string form")
		}
	})
})

var _ = Describe("Policy", func() {
	It("reads version 1, which had no compat byte or length", func() {
		e := denc.NewEncoder()
		e.U8(1)
		e.U8(1) // ACLOwner v1
		e.String("alice")
		e.String("Alice")
		listHeader(e, 1, true)
		e.U32(0)
		e.U32(0)
		Expect(decodeWhole(e.Bytes(), acl.DecodePolicy)).To(Equal(
			acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Alice"}}))
	})
	It("encodes the owner before the list at every release", func() {
		p := acl.Policy{
			Owner: acl.Owner{ID: "alice", DisplayName: "Alice"},
			ACL: acl.List{
				UserMap: map[string]int32{"alice": 15},
				Grants:  []acl.GrantEntry{{"alice", acl.Grant{ID: "alice", Name: "Alice", Permission: 15}}},
			},
		}
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 2)
		o := e.BeginStruct(3, 2)
		e.String("alice")
		e.String("Alice")
		e.EndStruct(o)
		lf, _ := listHeader(e, 4, true)
		e.U32(1)
		e.String("alice")
		e.I32(15)
		e.U32(1)
		e.String("alice")
		grant(e, 5, grantFields{typ: 0, id: "alice", name: "Alice", perm: 15})
		e.U32(0)
		e.U32(0)
		e.EndStruct(lf)
		e.EndStruct(f)
		for _, r := range releases {
			Expect(encodeAt(p, r)).To(Equal(e.Bytes()))
		}
		Expect(decodeWhole(e.Bytes(), acl.DecodePolicy)).To(Equal(p))
		Expect(mustMarshal(p)).To(Equal(`{"acl":{"acl_user_map":[{"user":"alice","acl":15}],"acl_group_map":[],` +
			`"grant_map":[{"id":"alice","grant":{"type":{"type":0},"id":"alice","name":"Alice","permission":{"flags":15}}}]},` +
			`"owner":{"id":"alice","display_name":"Alice"}}`))
	})
})
