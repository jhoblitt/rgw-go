package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("a key radosgw adds for a header the request does not send", func() {
	statement := func(effect, cond string) string {
		return `{"Version": "2012-10-17", "Statement": [{"Effect": "` + effect + `", "Principal": "*",` +
			` "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*", "Condition": ` + cond + `}]}`
	}
	// unsent is radosgw's environment for a PutObject that sends no
	// x-amz-acl: the key, empty, beside another.
	unsent := func() policy.Env {
		e := envOf("s3:x-amz-acl", "", "aws:username", "alice")
		e.MarkUnsent("s3:x-amz-acl")
		return e
	}
	// sent is the same key unmarked, a value the request sets.
	sent := func() policy.Env { return envOf("s3:x-amz-acl", "", "aws:username", "alice") }

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var (
				obj policy.ARN
				sem policy.Semantics
			)
			BeforeEach(func() {
				obj = s3ARN("b/k")
				sem = policy.SemanticsFor(rel)
			})
			judge := func(doc string, env policy.Env) policy.Effect {
				GinkgoHelper()
				return mustParse(doc, parseOpts(rel)).Eval(env, nil, policy.S3PutObject, &obj, sem)
			}

			It("judges a Deny without the key too, and refuses where either holds", func() {
				for _, cond := range []string{
					`{"Null": {"s3:x-amz-acl": "true"}}`,
					`{"StringEqualsIfExists": {"s3:x-amz-acl": "public-read"}}`,
				} {
					Expect(judge(statement("Deny", cond), unsent())).To(Equal(policy.Deny), "%s unsent", cond)
					Expect(judge(statement("Deny", cond), sent())).To(Equal(policy.Pass), "%s unmarked", cond)
				}
				Expect(judge(statement("Deny", `{"StringEquals": {"s3:x-amz-acl": ""}}`), unsent())).To(Equal(policy.Deny),
					"with the key, as radosgw judges it")
			})

			It("needs every condition of a Deny to hold on one of the two", func() {
				both := `{"Null": {"s3:x-amz-acl": "true"}, "StringEquals": {"s3:x-amz-acl": ""}}`
				Expect(judge(statement("Deny", both), unsent())).To(Equal(policy.Pass))
			})

			It("judges an Allow with the key alone, as radosgw does", func() {
				Expect(judge(statement("Allow", `{"StringNotEquals": {"s3:x-amz-acl": "public-read"}}`), unsent())).
					To(Equal(policy.Allow), "a negated operator over the empty value")
				Expect(judge(statement("Allow", `{"Null": {"s3:x-amz-acl": "true"}}`), unsent())).
					To(Equal(policy.Pass), "Null true over a present key")
			})
		})
	}
})
