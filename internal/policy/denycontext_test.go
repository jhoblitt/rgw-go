package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("the keys Amazon S3's request context sets for a Deny", func() {
	statement := func(effect, cond string) string {
		return `{"Version": "2012-10-17", "Statement": [{"Effect": "` + effect + `", "Principal": "*",` +
			` "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*", "Condition": ` + cond + `}]}`
	}
	// radosgw is radosgw's environment for a plain request at noon on
	// 2026-11-20: the time in seconds under aws:CurrentTime and in ISO 8601
	// under aws:EpochTime, and no aws:SecureTransport.
	radosgw := func() policy.Env {
		return envOf("aws:CurrentTime", "1795089600", "aws:EpochTime", "2026-11-20T12:00:00.000000000Z",
			"aws:username", "alice")
	}
	// amazon is radosgw's environment with Amazon S3's values for a Deny: the
	// two times the other way round, aws:SecureTransport false, and an
	// authentication type radosgw does not set.
	amazon := func(authType string) policy.Env {
		e := radosgw()
		e.SetForDeny("aws:CurrentTime", "2026-11-20T12:00:00Z")
		e.SetForDeny("aws:EpochTime", "1795089600")
		e.SetForDeny("aws:SecureTransport", "false")
		e.SetForDeny("s3:authType", authType)
		return e
	}

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
			judge := func(effect, cond string, env policy.Env) policy.Effect {
				GinkgoHelper()
				return mustParse(statement(effect, cond), parseOpts(rel)).Eval(env, nil, policy.S3GetObject, &obj, sem)
			}

			DescribeTable("judges a Deny on Amazon S3's values and on radosgw's, and refuses where either holds",
				func(cond string, onAmazon, onRadosgw policy.Effect) {
					Expect(judge("Deny", cond, amazon("REST-HEADER"))).To(Equal(onAmazon), "with Amazon S3's values")
					Expect(judge("Deny", cond, radosgw())).To(Equal(onRadosgw), "with radosgw's alone")
				},
				Entry("a value radosgw never sets, aws:SecureTransport false",
					`{"Bool": {"aws:SecureTransport": "false"}}`, policy.Deny, policy.Pass),
				Entry("a negated operator on a key radosgw leaves out, whose value meets it",
					`{"StringNotEquals": {"s3:authType": "REST-HEADER"}}`, policy.Pass, policy.Deny),
				Entry("a negated operator on a key radosgw leaves out, whose value fails it",
					`{"StringNotEquals": {"s3:authType": "REST-QUERY-STRING"}}`, policy.Deny, policy.Deny),
				Entry("a plain operator on a key radosgw leaves out",
					`{"StringEquals": {"s3:authType": "REST-HEADER"}}`, policy.Deny, policy.Pass),
				Entry("a Numeric operator on aws:EpochTime, which Amazon S3 sets in seconds",
					`{"NumericGreaterThan": {"aws:EpochTime": "1700000000"}}`, policy.Deny, policy.Pass),
				Entry("a Numeric operator on aws:CurrentTime, which radosgw sets in seconds",
					`{"NumericGreaterThan": {"aws:CurrentTime": "1700000000"}}`, policy.Deny, policy.Deny),
				Entry("a Date operator on aws:CurrentTime, which both set to the request's time",
					`{"DateGreaterThan": {"aws:CurrentTime": "2026-01-01T00:00:00Z"}}`, policy.Deny, policy.Deny),
				Entry("a Date operator on aws:EpochTime that neither time meets",
					`{"DateLessThan": {"aws:EpochTime": "2026-01-01T00:00:00Z"}}`, policy.Pass, policy.Pass),
				Entry("a pattern on aws:CurrentTime that Amazon S3's date and time meets",
					`{"StringLike": {"aws:CurrentTime": "2026-11-20T*"}}`, policy.Deny, policy.Pass),
				Entry("a pattern on aws:EpochTime that radosgw's date and time meets",
					`{"StringLike": {"aws:EpochTime": "2026-11-20T*"}}`, policy.Deny, policy.Deny),
				Entry("Amazon S3's key in another case",
					`{"Bool": {"AWS:SecureTransport": "false"}}`, policy.Deny, policy.Pass),
				Entry("an IfExists form on a key radosgw leaves out, which holds there as radosgw judges it",
					`{"StringNotEqualsIfExists": {"s3:authType": "REST-HEADER"}}`, policy.Deny, policy.Deny),
			)

			It("reads a negated operator over several values as Amazon S3 does on its context, and as the release's radosgw does on radosgw's", func() {
				cond := `{"StringNotEquals": {"s3:authType": ["REST-HEADER", "REST-QUERY-STRING"]}}`
				Expect(judge("Deny", cond, amazon("REST-HEADER"))).To(Equal(policy.Pass), "a value it names")
				Expect(judge("Deny", cond, amazon("POST"))).To(Equal(policy.Deny), "a value it does not name")
				Expect(judge("Deny", cond, radosgw())).To(Equal(policy.Deny), "no value")
				typed := `{"NumericNotEquals": {"aws:EpochTime": ["1795089600", "1"]}}`
				Expect(judge("Deny", typed, amazon("REST-HEADER"))).To(Equal(policy.Pass), "a typed operator, a value it names")
				// Squid's radosgw holds a negated operator where some value
				// differs, so its reading of aws:username, which it holds,
				// still refuses there.
				held := `{"StringNotEquals": {"aws:username": ["alice", "bob"]}}`
				want := policy.Pass
				if rel == denc.Squid {
					want = policy.Deny
				}
				Expect(judge("Deny", held, amazon("REST-HEADER"))).To(Equal(want), "a key radosgw holds")
			})

			It("judges each condition of a Deny on either, as it judges a Deny's evaluation", func() {
				both := `{"Bool": {"aws:SecureTransport": "false"}, "NumericGreaterThan": {"aws:CurrentTime": "1700000000"}}`
				Expect(judge("Deny", both, amazon("REST-HEADER"))).To(Equal(policy.Deny))
			})

			DescribeTable("judges an Allow on radosgw's values alone",
				func(cond string, want policy.Effect) {
					Expect(judge("Allow", cond, amazon("REST-HEADER"))).To(Equal(want), "with Amazon S3's values set")
					Expect(judge("Allow", cond, radosgw())).To(Equal(want), "without them")
				},
				Entry("aws:SecureTransport false", `{"Bool": {"aws:SecureTransport": "false"}}`, policy.Pass),
				Entry("a key radosgw leaves out", `{"StringEquals": {"s3:authType": "REST-HEADER"}}`, policy.Pass),
				Entry("a Numeric operator on aws:EpochTime", `{"NumericGreaterThan": {"aws:EpochTime": "1700000000"}}`, policy.Pass),
				Entry("a Numeric operator on aws:CurrentTime", `{"NumericGreaterThan": {"aws:CurrentTime": "1700000000"}}`, policy.Allow),
				Entry("Null on a key radosgw leaves out", `{"Null": {"s3:authType": "true"}}`, policy.Allow),
			)

			It("keeps the values for a Deny through the judgement without the keys radosgw adds unsent", func() {
				e := amazon("REST-QUERY-STRING")
				e.Add("s3:x-amz-acl", "")
				e.MarkUnsent("s3:x-amz-acl")
				cond := `{"Null": {"s3:x-amz-acl": "true"}, "StringEquals": {"s3:authType": "REST-QUERY-STRING"}}`
				Expect(judge("Deny", cond, e)).To(Equal(policy.Deny))
				c := e.Clone()
				Expect(judge("Deny", cond, c)).To(Equal(policy.Deny), "a clone")
			})

			// open is Amazon S3's values with s3:x-amz-content-sha256 read as
			// UNSIGNED-PAYLOAD and a second way: as other, or without the key
			// when other is empty.
			open := func(other string) policy.Env {
				e := amazon("REST-HEADER")
				e.SetForDeny("s3:x-amz-content-sha256", "UNSIGNED-PAYLOAD")
				if other == "" {
					e.SetForDenyOrAbsent("s3:x-amz-content-sha256")
				} else {
					e.SetForDenyOr("s3:x-amz-content-sha256", other)
				}
				return e
			}

			DescribeTable("judges a Deny on each reading of a key Amazon's texts leave open, and refuses where any holds",
				func(cond string, onAbsent, onSent policy.Effect) {
					Expect(judge("Deny", cond, open(""))).To(Equal(onAbsent), "read without the key too")
					Expect(judge("Deny", cond, open("STREAMING-UNSIGNED-PAYLOAD-TRAILER"))).To(Equal(onSent), "read as sent too")
					Expect(judge("Allow", cond, open(""))).To(Equal(policy.Pass), "an Allow, the key read without it too")
					Expect(judge("Allow", cond, open("STREAMING-UNSIGNED-PAYLOAD-TRAILER"))).To(Equal(policy.Pass), "an Allow, the key read as sent too")
				},
				Entry("a plain operator on the first reading",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`, policy.Deny, policy.Deny),
				Entry("a negated operator on the first reading",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`, policy.Deny, policy.Deny),
				Entry("a plain operator on the value as sent",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`, policy.Pass, policy.Deny),
				Entry("a pattern the value as sent meets",
					`{"StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`, policy.Pass, policy.Deny),
				Entry("a plain operator on neither reading",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}}`, policy.Pass, policy.Pass),
				Entry("a negated operator over both readings",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": ["UNSIGNED-PAYLOAD", "STREAMING-UNSIGNED-PAYLOAD-TRAILER"]}}`,
					policy.Deny, policy.Pass),
			)

			It("judges each condition of a Deny on any reading, so conditions met by different readings refuse together", func() {
				cond := `{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"},` +
					` "StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`
				Expect(judge("Deny", cond, open("STREAMING-UNSIGNED-PAYLOAD-TRAILER"))).To(Equal(policy.Deny))
			})

			It("keeps the second readings through a clone and the judgement without the keys radosgw adds unsent", func() {
				e := open("STREAMING-UNSIGNED-PAYLOAD-TRAILER")
				e.Add("s3:x-amz-acl", "")
				e.MarkUnsent("s3:x-amz-acl")
				cond := `{"Null": {"s3:x-amz-acl": "true"}, "StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`
				Expect(judge("Deny", cond, e)).To(Equal(policy.Deny))
				c := e.Clone()
				Expect(judge("Deny", cond, c)).To(Equal(policy.Deny), "a clone")
				absent := open("")
				Expect(judge("Deny", `{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`, absent.Clone())).
					To(Equal(policy.Deny), "a clone of the reading without the key")
				Expect(e.Lookup("s3:x-amz-content-sha256")).To(BeNil(), "radosgw's environment as it is")
			})

			It("leaves radosgw's environment as it is", func() {
				e := amazon("REST-HEADER")
				Expect(judge("Deny", `{"Bool": {"aws:SecureTransport": "false"}}`, e)).To(Equal(policy.Deny))
				Expect(e.Lookup("aws:SecureTransport")).To(BeNil())
				Expect(e.Lookup("aws:CurrentTime")).To(Equal([]string{"1795089600"}))
				Expect(e.Lookup("s3:authType")).To(BeNil())
			})
		})
	}
})
