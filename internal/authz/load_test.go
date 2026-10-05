package authz_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

const (
	attrIAMPolicy     = "user.rgw.iam-policy"
	attrPublicAccess  = "user.rgw.public-access"
	attrUserPolicy    = "user.rgw.user-policy"
	attrManagedPolicy = "user.rgw.managed-policy"

	s3ReadOnlyARN = "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"

	// getObjectPolicy allows s3:GetObject on every object of bucket b; an
	// empty Resource account takes the parse's tenant.
	getObjectPolicy = `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow",
		"Principal": {"AWS": "*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}]}`
)

func encoded(enc func(*denc.Encoder)) []byte {
	e := denc.NewEncoder()
	enc(e)
	return e.Bytes()
}

func encodeUserPolicies(m map[string]string) []byte {
	return encoded(func(e *denc.Encoder) {
		denc.EncodeMap(e, m, (*denc.Encoder).String, (*denc.Encoder).String)
	})
}

func encodeManagedPolicies(arns ...string) []byte {
	return encoded(func(e *denc.Encoder) {
		f := e.BeginStruct(1, 1)
		denc.EncodeSlice(e, arns, (*denc.Encoder).String)
		e.EndStruct(f)
	})
}

func encodeTags(kv ...string) []byte {
	var s tags.Set
	for i := 0; i+1 < len(kv); i += 2 {
		Expect(s.Add(kv[i], kv[i+1], tags.MaxBucketTags)).To(Succeed())
	}
	return encoded(func(e *denc.Encoder) { s.Encode(e, denc.Squid) })
}

func bucketRecord(attrs map[string][]byte) *op.BucketRecord {
	rec := &op.BucketRecord{Attrs: attrs}
	rec.Info.Bucket.Name = "b"
	rec.Info.Owner = meta.UserOwner(meta.ParseUserID("t$alice"))
	return rec
}

func resourceAccounts(p *policy.Policy) []string {
	var out []string
	for _, st := range p.Statements {
		for _, r := range st.Resources {
			out = append(out, r.Account)
		}
	}
	return out
}

var _ = Describe("bucketPolicy", func() {
	It("is nil for a bucket without a policy", func() {
		Expect(authz.BucketPolicy(bucketRecord(nil), "t", denc.Squid)).To(BeNil())
		Expect(authz.BucketPolicy(nil, "t", denc.Squid)).To(BeNil(), "no bucket")
	})

	It("parses the stored policy with the tenant given", func() {
		rec := bucketRecord(map[string][]byte{attrIAMPolicy: []byte(getObjectPolicy)})
		p, err := authz.BucketPolicy(rec, "t", denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Text).To(Equal(getObjectPolicy))
		Expect(resourceAccounts(p)).To(Equal([]string{"t"}))
	})

	It("drops an unsupported principal, as a stored policy is parsed", func() {
		rec := bucketRecord(map[string][]byte{attrIAMPolicy: []byte(`{"Version": "2012-10-17", "Statement": [{
			"Effect": "Allow", "Principal": {"CanonicalUser": "x"}, "Action": "s3:GetObject", "Resource": "*"}]}`)})
		p, err := authz.BucketPolicy(rec, "", denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Statements).To(HaveLen(1))
		Expect(p.Statements[0].Principals).To(BeEmpty())
	})

	It("returns the parse error for a document that does not parse", func() {
		rec := bucketRecord(map[string][]byte{attrIAMPolicy: []byte("{not json")})
		_, err := authz.BucketPolicy(rec, "t", denc.Squid)
		var pe *policy.ParseError
		Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)
	})

	It("parses with the release's vocabulary", func() {
		rec := bucketRecord(map[string][]byte{attrIAMPolicy: []byte(`{"Version": "2012-10-17", "Statement": [{
			"Effect": "Allow", "Principal": "*", "Action": "s3:GetObjectAttributes", "Resource": "*"}]}`)})
		_, err := authz.BucketPolicy(rec, "", denc.Squid)
		Expect(err).To(HaveOccurred(), "squid")
		_, err = authz.BucketPolicy(rec, "", denc.Tentacle)
		Expect(err).NotTo(HaveOccurred(), "tentacle")
	})
})

var _ = Describe("identityPolicies", func() {
	var alice op.Identity

	BeforeEach(func() {
		alice = identityOf(aliceInfo())
		alice.Attrs = map[string][]byte{}
	})

	It("is empty for the anonymous identity and a user without policies", func() {
		anon := op.Anonymous()
		Expect(authz.IdentityPolicies(&anon, denc.Squid)).To(BeEmpty())
		Expect(authz.IdentityPolicies(&alice, denc.Squid)).To(BeEmpty())
	})

	It("parses the user's inline policies with the user's tenant", func() {
		alice.Attrs[attrUserPolicy] = encodeUserPolicies(map[string]string{"p": getObjectPolicy})
		ps, err := authz.IdentityPolicies(&alice, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
		Expect(resourceAccounts(ps[0])).To(Equal([]string{"t"}))
	})

	It("parses an account user's inline policies with no tenant", func() {
		acct := accountIdentityOf(aliceInfo())
		acct.Attrs = map[string][]byte{attrUserPolicy: encodeUserPolicies(map[string]string{"p": getObjectPolicy})}
		ps, err := authz.IdentityPolicies(&acct, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
		Expect(resourceAccounts(ps[0])).To(Equal([]string{""}))
	})

	It("resolves the managed policies", func() {
		alice.Attrs[attrManagedPolicy] = encodeManagedPolicies(s3ReadOnlyARN)
		ps, err := authz.IdentityPolicies(&alice, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		want, ok := policy.ManagedPolicy(s3ReadOnlyARN, denc.Squid)
		Expect(ok).To(BeTrue())
		Expect(ps).To(HaveLen(1))
		Expect(ps[0].Text).To(Equal(want.Text))
	})

	It("puts the inline policies before the managed ones", func() {
		alice.Attrs[attrManagedPolicy] = encodeManagedPolicies(s3ReadOnlyARN)
		alice.Attrs[attrUserPolicy] = encodeUserPolicies(map[string]string{"p": getObjectPolicy})
		ps, err := authz.IdentityPolicies(&alice, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(2))
		Expect(ps[0].Text).To(Equal(getObjectPolicy))
	})

	It("fails on a stored policy that does not parse, or an attr that does not decode", func() {
		alice.Attrs[attrUserPolicy] = encodeUserPolicies(map[string]string{"p": "{not json"})
		_, err := authz.IdentityPolicies(&alice, denc.Squid)
		var pe *policy.ParseError
		Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)

		alice.Attrs[attrUserPolicy] = []byte{1}
		_, err = authz.IdentityPolicies(&alice, denc.Squid)
		Expect(err).To(HaveOccurred(), "user policies")

		delete(alice.Attrs, attrUserPolicy)
		alice.Attrs[attrManagedPolicy] = append(encodeManagedPolicies(s3ReadOnlyARN), 0)
		_, err = authz.IdentityPolicies(&alice, denc.Squid)
		Expect(err).To(HaveOccurred(), "managed policies with a trailing byte")
	})
})

var _ = Describe("publicAccess", func() {
	It("decodes the stored block", func() {
		want := acl.PublicAccessBlock{IgnorePublicACLs: true, RestrictPublicBuckets: true}
		rec := bucketRecord(map[string][]byte{attrPublicAccess: encoded(func(e *denc.Encoder) { want.Encode(e, denc.Squid) })})
		Expect(authz.PublicAccess(rec)).To(HaveValue(Equal(want)))
	})

	It("is nil when absent", func() {
		Expect(authz.PublicAccess(bucketRecord(nil))).To(BeNil(), "absent")
		Expect(authz.PublicAccess(nil)).To(BeNil(), "no bucket")
	})

	It("refuses the request for a block that does not decode, where radosgw drops it", func() {
		for _, b := range [][]byte{{9, 9}, {}, {1, 1, 4, 0, 0, 0, 1}} {
			pab, err := authz.PublicAccess(bucketRecord(map[string][]byte{attrPublicAccess: b}))
			Expect(err).To(MatchError(op.ErrAccessDenied), "attr % x", b)
			Expect(pab).To(BeNil(), "attr % x", b)
		}
	})
})

var _ = Describe("needsTags", func() {
	parse := func(cond string) *policy.Policy {
		p, err := policy.Parse(`{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": "*",
			"Action": "s3:GetObject", "Resource": "*", "Condition": `+cond+`}]}`, policy.ParseOptions{Release: denc.Squid})
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	DescribeTable("finds the tag condition keys and values (rgw_check_policy_condition)",
		func(cond string, existing, resource bool) {
			e, r := authz.NeedsTags(nil, parse(cond))
			Expect([]bool{e, r}).To(Equal([]bool{existing, resource}), "condition %s", cond)
		},
		Entry("an existing object tag", `{"StringEquals": {"s3:ExistingObjectTag/x": "y"}}`, true, false),
		Entry("in another case", `{"StringEquals": {"S3:existingobjecttag/x": "y"}}`, true, false),
		Entry("a resource tag key", `{"StringEquals": {"s3:ResourceTag/team": "a"}}`, false, true),
		Entry("a resource tag value", `{"StringEquals": {"aws:username": "${s3:ResourceTag/x}"}}`, false, true),
		Entry("neither", `{"StringEquals": {"aws:username": "alice"}}`, false, false),
	)

	It("ors over every policy", func() {
		e, r := authz.NeedsTags(parse(`{"StringEquals": {"s3:ExistingObjectTag/x": "y"}}`),
			parse(`{"StringEquals": {"s3:ResourceTag/x": "y"}}`))
		Expect([]bool{e, r}).To(Equal([]bool{true, true}))
		e, r = authz.NeedsTags()
		Expect([]bool{e, r}).To(Equal([]bool{false, false}), "no policies")
	})
})

var _ = Describe("addTags", func() {
	var attrs map[string][]byte

	BeforeEach(func() {
		attrs = map[string][]byte{tags.Attr: encodeTags("env", "prod", "team", "a")}
	})

	It("adds each tag under the keys asked for", func() {
		var env policy.Env
		authz.AddTags(&env, attrs, true, true)
		Expect(env.Lookup("s3:ExistingObjectTag/env")).To(Equal([]string{"prod"}))
		Expect(env.Lookup("s3:ExistingObjectTag/team")).To(Equal([]string{"a"}))
		Expect(env.Lookup("s3:ResourceTag/env")).To(Equal([]string{"prod"}))
		Expect(env.Lookup("s3:ResourceTag/team")).To(Equal([]string{"a"}))

		env = policy.Env{}
		authz.AddTags(&env, attrs, false, true)
		Expect(env.Lookup("s3:ExistingObjectTag/env")).To(BeNil(), "resource only")
		Expect(env.Lookup("s3:ResourceTag/env")).To(Equal([]string{"prod"}), "resource only")
	})

	It("adds a repeated key's values in multimap order", func() {
		attrs[tags.Attr] = encodeTags("k", "b", "k", "a")
		var env policy.Env
		authz.AddTags(&env, attrs, false, true)
		Expect(env.Lookup("s3:ResourceTag/k")).To(Equal([]string{"b", "a"}))
	})

	It("adds nothing without tags or for a tag set that does not decode", func() {
		var env, empty policy.Env
		authz.AddTags(&env, nil, true, true)
		// Neither the binary form nor, as text, a valid x-amz-tagging string.
		for _, b := range [][]byte{{}, {0xff, '&'}} {
			attrs[tags.Attr] = b
			authz.AddTags(&env, attrs, true, true)
		}
		Expect(env.Clone()).To(Equal(empty.Clone()))
	})
})
