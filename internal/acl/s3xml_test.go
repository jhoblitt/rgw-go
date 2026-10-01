package acl_test

import (
	"context"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// mapResolver resolves the users and accounts names holds, by to_string id,
// and the addresses emails holds. errs, keyed by to_string id or address,
// holds a lookup's error in place of its answer.
type mapResolver struct {
	names  map[string]string
	emails map[string]meta.Owner
	errs   map[string]error
}

func (m mapResolver) OwnerByEmail(ctx context.Context, email string) (acl.Owner, error) {
	if err := m.errs[email]; err != nil {
		return acl.Owner{}, err
	}
	o, ok := m.emails[email]
	if !ok {
		return acl.Owner{}, acl.ErrUnresolvableEmail
	}
	name, err := m.DisplayName(ctx, o)
	if err != nil {
		return acl.Owner{}, err
	}
	return acl.Owner{ID: o.String(), DisplayName: name}, nil
}

func (m mapResolver) DisplayName(_ context.Context, o meta.Owner) (string, error) {
	if err := m.errs[o.String()]; err != nil {
		return "", err
	}
	name, ok := m.names[o.String()]
	if !ok {
		return "", acl.ErrNoSuchOwner
	}
	return name, nil
}

const account = "RGW12345678901234567"

// newResolver knows alice, bob, carol and an account, and carol's and the
// account's addresses.
func newResolver() mapResolver {
	return mapResolver{
		names: map[string]string{"alice": "Alice", "bob": "Bob", "carol": "Carol", account: "Acct"},
		emails: map[string]meta.Owner{
			"c@x": meta.ParseOwner("carol"),
			"a@x": meta.ParseOwner(account),
		},
		errs: map[string]error{},
	}
}

const (
	s3NS     = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`
	xsiNS    = `xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`
	aliceOwn = `<Owner><ID>alice</ID></Owner>`
)

// aclDoc is an AccessControlPolicy document owned by alice holding grants.
func aclDoc(grants ...string) string {
	return `<AccessControlPolicy ` + s3NS + `>` + aliceOwn + `<AccessControlList>` +
		strings.Join(grants, "") + `</AccessControlList></AccessControlPolicy>`
}

// xmlGrant is a Grant element whose Grantee, of S3 type typ, holds grantee,
// followed by one Permission element per perm.
func xmlGrant(typ, grantee string, perms ...string) string {
	var b strings.Builder
	b.WriteString(`<Grant><Grantee ` + xsiNS + ` xsi:type="` + typ + `">` + grantee + `</Grantee>`)
	for _, p := range perms {
		b.WriteString(`<Permission>` + p + `</Permission>`)
	}
	b.WriteString(`</Grant>`)
	return b.String()
}

// policyXML is the document MarshalS3XML writes for a policy alice owns,
// named Alice, holding the rendered grants.
func policyXML(grants string) string {
	return `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><AccessControlList>` +
		grants + `</AccessControlList></AccessControlPolicy>`
}

const xsiGrantee = `<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" `

func namedGrant(id, name string, p acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeCanonUser, ID: id, Name: name, Permission: p}
}

// policyOf returns the policy owner holds with grants, added in order.
func policyOf(owner acl.Owner, grants ...acl.Grant) acl.Policy {
	p := acl.Policy{Owner: owner}
	for _, g := range grants {
		p.ACL.AddGrant(g)
	}
	return p
}

var (
	aliceOwner = acl.Owner{ID: "alice", DisplayName: "Alice"}
	bobOwner   = acl.Owner{ID: "bob", DisplayName: "Bob"}
)

var _ = Describe("group URIs", func() {
	DescribeTable("GroupForURI is acl_uri_to_group",
		func(uri string, want uint32) {
			Expect(acl.GroupForURI(uri)).To(Equal(want), "%q", uri)
		},
		Entry("AllUsers", acl.URIAllUsers, acl.GroupAllUsers),
		Entry("AuthenticatedUsers", acl.URIAuthenticatedUsers, acl.GroupAuthenticatedUsers),
		Entry("compared case-sensitively", "http://acs.amazonaws.com/groups/global/allusers", acl.GroupNone),
		Entry("any other URI", "http://acs.amazonaws.com/groups/s3/LogDelivery", acl.GroupNone),
	)

	DescribeTable("URIForGroup is acl_group_to_uri",
		func(group uint32, want string, ok bool) {
			uri, found := acl.URIForGroup(group)
			Expect(uri).To(Equal(want), "group %d", group)
			Expect(found).To(Equal(ok), "group %d", group)
		},
		Entry("AllUsers", acl.GroupAllUsers, acl.URIAllUsers, true),
		Entry("AuthenticatedUsers", acl.GroupAuthenticatedUsers, acl.URIAuthenticatedUsers, true),
		Entry("no group", acl.GroupNone, "", false),
		Entry("a group radosgw does not define", uint32(7), "", false),
	)
})

var _ = Describe("MarshalS3XML", func() {
	It("writes the default policy as radosgw does, and ParseS3XML reads it back", func(ctx SpecContext) {
		p := alicesDefault()
		doc := p.MarshalS3XML()
		Expect(string(doc)).To(Equal(`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>alice</ID><DisplayName>Alice</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`))
		Expect(acl.ParseS3XML(ctx, newResolver(), doc)).To(Equal(p))
	})

	DescribeTable("writes each grant's grantee, then its permissions",
		func(g acl.Grant, want string) {
			Expect(string(alicesPolicy(g).MarshalS3XML())).To(Equal(policyXML(want)))
		},
		Entry("READ and WRITE as two Permission elements, READ first",
			userGrant("bob", acl.PermWrite|acl.PermRead),
			`<Grant>`+xsiGrantee+`xsi:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ</Permission><Permission>WRITE</Permission></Grant>`),
		Entry("READ_ACP and WRITE_ACP in that order",
			userGrant("bob", acl.PermWriteACP|acl.PermReadACP),
			`<Grant>`+xsiGrantee+`xsi:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ_ACP</Permission><Permission>WRITE_ACP</Permission></Grant>`),
		Entry("the four S3 flags as FULL_CONTROL, whatever else is set",
			userGrant("bob", acl.PermFullControl|acl.PermReadObjs),
			`<Grant>`+xsiGrantee+`xsi:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant>`),
		Entry("a canonical user's display name when it has one",
			namedGrant("bob", "Bob", acl.PermRead),
			`<Grant>`+xsiGrantee+`xsi:type="CanonicalUser"><ID>bob</ID><DisplayName>Bob</DisplayName></Grantee><Permission>READ</Permission></Grant>`),
		Entry("a group by its URI",
			groupGrant(acl.GroupAllUsers, acl.PermRead),
			`<Grant>`+xsiGrantee+`xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`),
		Entry("a group radosgw has no URI for, with an empty URI",
			groupGrant(7, acl.PermRead),
			`<Grant>`+xsiGrantee+`xsi:type="Group"><URI></URI></Grantee><Permission>READ</Permission></Grant>`),
		Entry("an email grantee by its address",
			emailGrant("c@x", acl.PermWrite),
			`<Grant>`+xsiGrantee+`xsi:type="AmazonCustomerByEmail"><EmailAddress>c@x</EmailAddress></Grantee><Permission>WRITE</Permission></Grant>`),
		Entry("a referer as an empty grantee of type unknown",
			refererGrant("*", acl.PermRead),
			`<Grant>`+xsiGrantee+`xsi:type="unknown"></Grantee><Permission>READ</Permission></Grant>`),
		Entry("a grantee type beyond the referer as unknown",
			acl.Grant{Type: 9, Permission: acl.PermRead},
			`<Grant>`+xsiGrantee+`xsi:type="unknown"></Grantee><Permission>READ</Permission></Grant>`),
		Entry("nothing for a grant without an S3 flag",
			refererGrant("*", acl.PermReadObjs|acl.PermWriteObjs),
			``),
	)

	It("writes the grants in the multimap's key order", func() {
		p := alicesPolicy(userGrant("bob", acl.PermRead), groupGrant(acl.GroupAllUsers, acl.PermWrite))
		Expect(string(p.MarshalS3XML())).To(Equal(policyXML(
			`<Grant>` + xsiGrantee + `xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>WRITE</Permission></Grant>` +
				`<Grant>` + xsiGrantee + `xsi:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ</Permission></Grant>`)))
	})

	It("leaves out the Owner element for an empty owner id, and an empty display name", func() {
		Expect(string(acl.Policy{}.MarshalS3XML())).To(Equal(
			`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><AccessControlList></AccessControlList></AccessControlPolicy>`))
		Expect(string(acl.Policy{Owner: acl.Owner{ID: "alice"}}.MarshalS3XML())).To(Equal(
			`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID></Owner><AccessControlList></AccessControlList></AccessControlPolicy>`))
	})

	It("escapes a display name as radosgw's XML formatter escapes text, and ParseS3XML reads it back", func(ctx SpecContext) {
		const name = `A & B <x> 'q'`
		p := acl.DefaultPolicy(meta.UserOwner(meta.ParseUserID("alice")), name)
		doc := string(p.MarshalS3XML())
		Expect(doc).To(Equal(`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID><DisplayName>A &amp; B &lt;x&gt; &apos;q&apos;</DisplayName></Owner><AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>alice</ID><DisplayName>A &amp; B &lt;x&gt; &apos;q&apos;</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`))

		r := newResolver()
		r.names["alice"] = name
		Expect(acl.ParseS3XML(ctx, r, []byte(doc))).To(Equal(p))
	})

	It("escapes ids and email addresses", func() {
		p := policyOf(acl.Owner{ID: `t$a"b`}, userGrant(`t$c<d`, acl.PermRead), emailGrant("o'neil@x", acl.PermRead))
		Expect(string(p.MarshalS3XML())).To(Equal(
			`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>t$a&quot;b</ID></Owner><AccessControlList>` +
				`<Grant>` + xsiGrantee + `xsi:type="AmazonCustomerByEmail"><EmailAddress>o&apos;neil@x</EmailAddress></Grantee><Permission>READ</Permission></Grant>` +
				`<Grant>` + xsiGrantee + `xsi:type="CanonicalUser"><ID>t$c&lt;d</ID></Grantee><Permission>READ</Permission></Grant>` +
				`</AccessControlList></AccessControlPolicy>`))
	})
})

var _ = Describe("ParseS3XML", func() {
	var r mapResolver
	BeforeEach(func() { r = newResolver() })

	parse := func(ctx context.Context, doc string) (acl.Policy, error) {
		return acl.ParseS3XML(ctx, r, []byte(doc))
	}

	It("resolves each grantee and adds the grants in document order", func(ctx SpecContext) {
		p, err := parse(ctx, aclDoc(
			xmlGrant("Group", `<URI>`+acl.URIAuthenticatedUsers+`</URI>`, "WRITE"),
			xmlGrant("CanonicalUser", `<ID>bob</ID>`, "READ"),
			xmlGrant("Group", `<URI>`+acl.URIAllUsers+`</URI>`, "READ_ACP"),
		))
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(policyOf(aliceOwner,
			groupGrant(acl.GroupAuthenticatedUsers, acl.PermWrite),
			namedGrant("bob", "Bob", acl.PermRead),
			groupGrant(acl.GroupAllUsers, acl.PermReadACP),
		)))
	})

	It("stores an email grantee as a canonical-user grant to the address's owner", func(ctx SpecContext) {
		Expect(parse(ctx, aclDoc(
			xmlGrant("AmazonCustomerByEmail", `<EmailAddress>c@x</EmailAddress>`, "WRITE_ACP"),
			xmlGrant("AmazonCustomerByEmail", `<EmailAddress>a@x</EmailAddress>`, "READ"),
		))).To(Equal(policyOf(aliceOwner,
			namedGrant("carol", "Carol", acl.PermWriteACP),
			namedGrant(account, "Acct", acl.PermRead),
		)))
	})

	It("names a canonical-user grantee by its resolved display name, not the document's", func(ctx SpecContext) {
		Expect(parse(ctx, aclDoc(
			xmlGrant("CanonicalUser", `<ID>bob</ID><DisplayName>Robert</DisplayName>`, "READ"),
		))).To(Equal(policyOf(aliceOwner, namedGrant("bob", "Bob", acl.PermRead))))
	})

	It("keeps the document's owner display name over the resolved one unless it is empty", func(ctx SpecContext) {
		named := `<AccessControlPolicy><Owner><ID>alice</ID><DisplayName>Al</DisplayName></Owner><AccessControlList/></AccessControlPolicy>`
		Expect(parse(ctx, named)).To(Equal(acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Al"}}))

		unnamed := `<AccessControlPolicy><Owner><ID>alice</ID><DisplayName></DisplayName></Owner><AccessControlList/></AccessControlPolicy>`
		Expect(parse(ctx, unnamed)).To(Equal(acl.Policy{Owner: aliceOwner}))
	})

	It("reads the owner id through parse_owner", func(ctx SpecContext) {
		doc := `<AccessControlPolicy><Owner><ID>` + account + `</ID></Owner><AccessControlList/></AccessControlPolicy>`
		Expect(parse(ctx, doc)).To(Equal(acl.Policy{Owner: acl.Owner{ID: account, DisplayName: "Acct"}}))
	})

	DescribeTable("reads a grant's permission",
		func(ctx SpecContext, perms []string, want acl.Permission) {
			Expect(parse(ctx, aclDoc(xmlGrant("Group", `<URI>`+acl.URIAllUsers+`</URI>`, perms...)))).
				To(Equal(policyOf(aliceOwner, groupGrant(acl.GroupAllUsers, want))))
		},
		Entry("case-insensitively, as strcasecmp", []string{"read"}, acl.PermRead),
		Entry("FULL_CONTROL in any case", []string{"Full_Control"}, acl.PermFullControl),
		Entry("from the first Permission element only", []string{"READ", "WRITE"}, acl.PermRead),
	)

	It("reads text split by CDATA and character references as one value", func(ctx SpecContext) {
		Expect(parse(ctx, aclDoc(`<Grant><Grantee `+xsiNS+` xsi:type="CanonicalUser"><ID>b<![CDATA[o]]>&#98;</ID></Grantee><Permission>RE<!-- c -->AD</Permission></Grant>`))).
			To(Equal(policyOf(aliceOwner, namedGrant("bob", "Bob", acl.PermRead))))
	})

	It("matches unprefixed names: a default namespace changes nothing, and none is needed", func(ctx SpecContext) {
		grant := xmlGrant("CanonicalUser", `<ID>bob</ID>`, "READ")
		want := policyOf(aliceOwner, namedGrant("bob", "Bob", acl.PermRead))
		Expect(parse(ctx, aclDoc(grant))).To(Equal(want))
		Expect(parse(ctx, `<AccessControlPolicy>`+aliceOwn+`<AccessControlList>`+grant+`</AccessControlList></AccessControlPolicy>`)).To(Equal(want))
	})

	It("reads xsi:type without a declaration of the xsi prefix", func(ctx SpecContext) {
		Expect(parse(ctx, aclDoc(`<Grant><Grantee xsi:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ</Permission></Grant>`))).
			To(Equal(policyOf(aliceOwner, namedGrant("bob", "Bob", acl.PermRead))))
	})

	It("ignores prefixed elements, elements radosgw does not read, and grants outside the AccessControlList", func(ctx SpecContext) {
		doc := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<!-- an ACL --><AccessControlPolicy xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/">` + aliceOwn +
			xmlGrant("CanonicalUser", `<ID>carol</ID>`, "READ") +
			`<AccessControlList><Extra>x</Extra><s3:Grant><Grantee xsi:type="CanonicalUser"><ID>carol</ID></Grantee><Permission>READ</Permission></s3:Grant>` +
			xmlGrant("CanonicalUser", `<ID>bob</ID><Other/>`, "WRITE") +
			`</AccessControlList></AccessControlPolicy>` + "\n"
		Expect(parse(ctx, doc)).To(Equal(policyOf(aliceOwner, namedGrant("bob", "Bob", acl.PermWrite))))
	})

	It("reads a document with a UTF-8 byte order mark", func(ctx SpecContext) {
		Expect(parse(ctx, "\ufeff"+`<?xml version="1.0"?>`+aclDoc())).To(Equal(acl.Policy{Owner: aliceOwner}))
	})

	It("reads a document with a document type declaration before the root", func(ctx SpecContext) {
		Expect(parse(ctx, `<?xml version="1.0"?><!DOCTYPE AccessControlPolicy>`+aclDoc())).To(Equal(acl.Policy{Owner: aliceOwner}))
	})

	const unresolvable = "The e-mail address you provided does not match any account on record."

	DescribeTable("refuses a document radosgw refuses, with its message",
		func(ctx SpecContext, doc string, setup func(mapResolver), message string, sentinel error) {
			if setup != nil {
				setup(r)
			}
			_, err := parse(ctx, doc)
			Expect(err).To(MatchError(sentinel))
			Expect(err).To(Equal(&acl.ParseError{Message: message, Err: sentinel}))
		},
		Entry("a document that is not XML",
			`not xml`, nil, "", acl.ErrInvalid),
		Entry("an empty document",
			``, nil, "", acl.ErrInvalid),
		Entry("an unclosed root",
			`<AccessControlPolicy>`+aliceOwn+`<AccessControlList/>`, nil, "", acl.ErrInvalid),
		Entry("a mismatched end tag",
			`<AccessControlPolicy>`+aliceOwn+`<AccessControlList></Owner></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("a second root element",
			aclDoc()+`<x/>`, nil, "", acl.ErrInvalid),
		Entry("text after the root",
			aclDoc()+`x`, nil, "", acl.ErrInvalid),
		Entry("text before the root",
			`x`+aclDoc(), nil, "", acl.ErrInvalid),
		Entry("an attribute given twice",
			`<AccessControlPolicy a="1" a="2">`+aliceOwn+`<AccessControlList/></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("an XML declaration after the start",
			" "+`<?xml version="1.0"?>`+aclDoc(), nil, "", acl.ErrInvalid),
		Entry("an XML declaration whose target is written in another case",
			`<?XML version="1.0"?>`+aclDoc(), nil, "", acl.ErrInvalid),
		Entry("a document type declaration after the root",
			aclDoc()+`<!DOCTYPE AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("a root other than AccessControlPolicy",
			`<Policy/>`, nil, "Missing element AccessControlPolicy", acl.ErrInvalid),
		Entry("a prefixed root",
			`<s3:AccessControlPolicy xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"><s3:Owner><s3:ID>alice</s3:ID></s3:Owner><s3:AccessControlList/></s3:AccessControlPolicy>`, nil, "Missing element AccessControlPolicy", acl.ErrInvalid),
		Entry("no Owner, which the root's xml_end refuses before parse_policy names it",
			`<AccessControlPolicy><AccessControlList/></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("no AccessControlList, likewise",
			`<AccessControlPolicy>`+aliceOwn+`</AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("an Owner without an ID",
			`<AccessControlPolicy><Owner><DisplayName>A</DisplayName></Owner><AccessControlList/></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("an owner the resolver does not know",
			`<AccessControlPolicy><Owner><ID>mallory</ID></Owner><AccessControlList/></AccessControlPolicy>`, nil, "Invalid Owner ID", acl.ErrInvalid),
		Entry("an owner whose lookup fails",
			aclDoc(),
			func(m mapResolver) { m.errs["alice"] = errors.New("io") }, "Invalid Owner ID", acl.ErrInvalid),
		Entry("a grant without a Grantee",
			aclDoc(`<Grant><Permission>READ</Permission></Grant>`), nil, "", acl.ErrInvalid),
		Entry("a grant without a Permission",
			aclDoc(xmlGrant("CanonicalUser", `<ID>bob</ID>`)), nil, "", acl.ErrInvalid),
		Entry("a Permission radosgw does not know",
			aclDoc(xmlGrant("CanonicalUser", `<ID>bob</ID>`, "NOPE")), nil, "", acl.ErrInvalid),
		Entry("a Permission radosgw does not know after a valid one",
			aclDoc(xmlGrant("CanonicalUser", `<ID>bob</ID>`, "READ", "NOPE")), nil, "", acl.ErrInvalid),
		Entry("a Permission with surrounding whitespace",
			aclDoc(xmlGrant("CanonicalUser", `<ID>bob</ID>`, " READ ")), nil, "", acl.ErrInvalid),
		Entry("a Permission radosgw does not know outside any grant",
			`<AccessControlPolicy><Owner><ID>alice</ID><Permission>x</Permission></Owner><AccessControlList/></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("an invalid grant outside the AccessControlList",
			`<AccessControlPolicy>`+aliceOwn+`<Grant/><AccessControlList/></AccessControlPolicy>`, nil, "", acl.ErrInvalid),
		Entry("a grantee type radosgw does not know",
			aclDoc(xmlGrant("Nope", `<ID>bob</ID>`, "READ")), nil, "", acl.ErrInvalid),
		Entry("a grantee type compared case-sensitively",
			aclDoc(xmlGrant("canonicaluser", `<ID>bob</ID>`, "READ")), nil, "", acl.ErrInvalid),
		Entry("a grantee whose type attribute has no prefix",
			aclDoc(`<Grant><Grantee type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ</Permission></Grant>`), nil, "", acl.ErrInvalid),
		Entry("a grantee typed only by a DTD attribute default, which expat applies (a recorded difference)",
			`<!DOCTYPE AccessControlPolicy [<!ATTLIST Grantee xsi:type CDATA "CanonicalUser">]>`+
				aclDoc(`<Grant><Grantee><ID>bob</ID></Grantee><Permission>READ</Permission></Grant>`), nil, "", acl.ErrInvalid),
		Entry("a grantee whose type attribute has another prefix bound to the xsi namespace",
			aclDoc(`<Grant><Grantee xmlns:foo="http://www.w3.org/2001/XMLSchema-instance" foo:type="CanonicalUser"><ID>bob</ID></Grantee><Permission>READ</Permission></Grant>`), nil, "", acl.ErrInvalid),
		Entry("a canonical user without an ID",
			aclDoc(xmlGrant("CanonicalUser", `<DisplayName>Bob</DisplayName>`, "READ")), nil, "", acl.ErrInvalid),
		Entry("a group without a URI",
			aclDoc(xmlGrant("Group", ``, "READ")), nil, "", acl.ErrInvalid),
		Entry("an email grantee without an EmailAddress",
			aclDoc(xmlGrant("AmazonCustomerByEmail", `<ID>bob</ID>`, "READ")), nil, "", acl.ErrInvalid),
		Entry("an empty EmailAddress",
			aclDoc(xmlGrant("AmazonCustomerByEmail", `<EmailAddress/>`, "READ")), nil, "", acl.ErrInvalid),
		Entry("an email address no user has",
			aclDoc(xmlGrant("AmazonCustomerByEmail", `<EmailAddress>m@x</EmailAddress>`, "READ")),
			nil, unresolvable, acl.ErrUnresolvableEmail),
		Entry("an email address whose lookup fails",
			aclDoc(xmlGrant("AmazonCustomerByEmail", `<EmailAddress>c@x</EmailAddress>`, "READ")),
			func(m mapResolver) { m.errs["c@x"] = errors.New("io") }, unresolvable, acl.ErrUnresolvableEmail),
		Entry("a canonical user the resolver does not know",
			aclDoc(xmlGrant("CanonicalUser", `<ID>mallory</ID>`, "READ")),
			nil, "Invalid CanonicalUser id", acl.ErrInvalid),
		Entry("a canonical user whose lookup fails",
			aclDoc(xmlGrant("CanonicalUser", `<ID>bob</ID>`, "READ")),
			func(m mapResolver) { m.errs["bob"] = errors.New("io") }, "Invalid CanonicalUser id", acl.ErrInvalid),
		Entry("a group URI radosgw does not know",
			aclDoc(xmlGrant("Group", `<URI>http://acs.amazonaws.com/groups/s3/LogDelivery</URI>`, "READ")),
			nil, "Invalid group uri", acl.ErrInvalid),
		Entry("the first failing grant, in document order",
			aclDoc(
				xmlGrant("Group", `<URI>x</URI>`, "READ"),
				xmlGrant("CanonicalUser", `<ID>mallory</ID>`, "READ"),
			), nil, "Invalid group uri", acl.ErrInvalid),
	)

	It("returns a ParseError whose text carries the sentinel and the message", func(ctx SpecContext) {
		_, err := parse(ctx, aclDoc(xmlGrant("Group", `<URI>x</URI>`, "READ")))
		Expect(err).To(MatchError("acl: invalid: Invalid group uri"))
		_, err = parse(ctx, `<x`)
		Expect(err).To(MatchError("acl: invalid"))
	})
})

var _ = Describe("Canned", func() {
	DescribeTable("is create_canned_acl: FULL_CONTROL to the owner, then the canned grants",
		func(canned string, extra ...acl.Grant) {
			want := policyOf(aliceOwner, append([]acl.Grant{namedGrant("alice", "Alice", acl.PermFullControl)}, extra...)...)
			Expect(acl.Canned(aliceOwner, bobOwner, canned)).To(Equal(want), "%q", canned)
		},
		Entry("none", ""),
		Entry("private", "private"),
		Entry("public-read", "public-read", groupGrant(acl.GroupAllUsers, acl.PermRead)),
		Entry("public-read-write, as two grants", "public-read-write",
			groupGrant(acl.GroupAllUsers, acl.PermRead), groupGrant(acl.GroupAllUsers, acl.PermWrite)),
		Entry("authenticated-read", "authenticated-read", groupGrant(acl.GroupAuthenticatedUsers, acl.PermRead)),
		Entry("bucket-owner-read", "bucket-owner-read", namedGrant("bob", "Bob", acl.PermRead)),
		Entry("bucket-owner-full-control", "bucket-owner-full-control", namedGrant("bob", "Bob", acl.PermFullControl)),
	)

	DescribeTable("adds no bucket-owner grant when the owner owns the bucket",
		func(canned string) {
			Expect(acl.Canned(aliceOwner, aliceOwner, canned)).
				To(Equal(policyOf(aliceOwner, namedGrant("alice", "Alice", acl.PermFullControl))), "%q", canned)
		},
		Entry("bucket-owner-read", "bucket-owner-read"),
		Entry("bucket-owner-full-control", "bucket-owner-full-control"),
	)

	It("makes the bucket owner the owner of an anonymous user's policy, which grants the anonymous user FULL_CONTROL", func() {
		anon := acl.Owner{ID: "anonymous"}
		Expect(acl.Canned(anon, bobOwner, "")).To(Equal(policyOf(bobOwner, namedGrant("anonymous", "", acl.PermFullControl))))
	})

	DescribeTable("refuses a name radosgw does not know",
		func(canned string) {
			_, err := acl.Canned(aliceOwner, bobOwner, canned)
			Expect(err).To(MatchError(acl.ErrInvalid), "%q", canned)
		},
		Entry("an unknown name", "nope"),
		Entry("a known name in another case", "Private"),
	)
})

var _ = Describe("FromHeaders", func() {
	var r mapResolver
	BeforeEach(func() { r = newResolver() })

	It("resolves each header's grantees, in header order", func(ctx SpecContext) {
		p, err := acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{
			Read:        `id="bob", uri="http://acs.amazonaws.com/groups/global/AllUsers"`,
			FullControl: `emailAddress="c@x"`,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(policyOf(aliceOwner,
			namedGrant("bob", "Bob", acl.PermRead),
			groupGrant(acl.GroupAllUsers, acl.PermRead),
			namedGrant("carol", "Carol", acl.PermFullControl),
		)))
	})

	It("reads the headers in the order READ, WRITE, READ_ACP, WRITE_ACP, FULL_CONTROL", func(ctx SpecContext) {
		all := `uri="` + acl.URIAllUsers + `"`
		p, err := acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{
			FullControl: all, WriteACP: all, ReadACP: all, Write: all, Read: all,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(policyOf(aliceOwner,
			groupGrant(acl.GroupAllUsers, acl.PermRead),
			groupGrant(acl.GroupAllUsers, acl.PermWrite),
			groupGrant(acl.GroupAllUsers, acl.PermReadACP),
			groupGrant(acl.GroupAllUsers, acl.PermWriteACP),
			groupGrant(acl.GroupAllUsers, acl.PermFullControl),
		)))
	})

	It("gives no grants for empty headers", func(ctx SpecContext) {
		Expect(acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{})).To(Equal(acl.Policy{Owner: aliceOwner}))
	})

	DescribeTable("parses an item as parse_key_value and rgw_trim_quotes do",
		func(ctx SpecContext, header string, want ...acl.Grant) {
			Expect(acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{Write: header})).
				To(Equal(policyOf(aliceOwner, want...)), "%q", header)
		},
		Entry("keys case-insensitively", `ID=bob,EMAILADDRESS=c@x,Uri=`+acl.URIAllUsers,
			namedGrant("bob", "Bob", acl.PermWrite), namedGrant("carol", "Carol", acl.PermWrite), groupGrant(acl.GroupAllUsers, acl.PermWrite)),
		Entry("whitespace trimmed around key and value", " id \t= \"bob\" ", namedGrant("bob", "Bob", acl.PermWrite)),
		Entry("an unquoted value", `id=bob`, namedGrant("bob", "Bob", acl.PermWrite)),
		Entry("empty items skipped", `,id=bob,,`, namedGrant("bob", "Bob", acl.PermWrite)),
		Entry("an account id through parse_owner", `id=`+account, namedGrant(account, "Acct", acl.PermWrite)),
	)

	DescribeTable("refuses an item radosgw refuses",
		func(ctx SpecContext, header string, setup func(mapResolver), want error) {
			if setup != nil {
				setup(r)
			}
			_, err := acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{ReadACP: header})
			Expect(err).To(MatchError(want), "%q", header)
		},
		Entry("an unknown key", `nope="x"`, nil, acl.ErrInvalid),
		Entry("an item without =", `bob`, nil, acl.ErrInvalid),
		Entry("an item of whitespace", `id=bob, `, nil, acl.ErrInvalid),
		Entry("a group URI radosgw does not know", `uri="http://acs.amazonaws.com/groups/s3/LogDelivery"`, nil, acl.ErrInvalid),
		Entry("an id no user has, as -ENOENT", `id="mallory"`, nil, acl.ErrGranteeNotFound),
		Entry("the value after the first =, which no user has", `id=bob=x`, nil, acl.ErrGranteeNotFound),
		Entry("quotes kept unless both ends have one", `id="bob`, nil, acl.ErrGranteeNotFound),
		Entry("whitespace inside the quotes kept", `id=" bob"`, nil, acl.ErrGranteeNotFound),
		Entry("an address no user has, as -ENOENT", `emailAddress="m@x"`, nil, acl.ErrGranteeNotFound),
		Entry("an address whose owner cannot be read, as -ENOENT", `emailAddress="d@x"`,
			func(m mapResolver) { m.emails["d@x"] = meta.ParseOwner("dave") }, acl.ErrGranteeNotFound),
	)

	It("returns a miss as ErrGranteeNotFound alone, not the resolver's sentinel", func(ctx SpecContext) {
		_, err := acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{Read: `id=mallory`})
		Expect(err).NotTo(MatchError(acl.ErrNoSuchOwner))
		_, err = acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{Read: `emailAddress=m@x`})
		Expect(err).NotTo(MatchError(acl.ErrUnresolvableEmail))
	})

	It("returns any other resolver error unchanged", func(ctx SpecContext) {
		ioErr := errors.New("io")
		r.errs["bob"] = ioErr
		r.errs["c@x"] = ioErr
		_, err := acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{Read: `id=bob`})
		Expect(err).To(BeIdenticalTo(ioErr))
		_, err = acl.FromHeaders(ctx, r, aliceOwner, acl.GrantHeaders{Read: `emailAddress=c@x`})
		Expect(err).To(BeIdenticalTo(ioErr))
	})
})
