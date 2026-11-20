package authz_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// A bucket policy's Deny whose condition names a key in another case than the
// request holds it in takes Amazon S3's answer for that key, and holds where
// the IAM User Guide gives none (docs/differences.md, "A Deny's condition key
// names match whatever their case"). Each spec is a request by alice, the
// bucket's owner, whom nothing but the Deny refuses.
var _ = Describe("a Deny on a condition key in another case than the request's", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var f *fixture

			// The clock stands at noon on 2026-11-20, for the specs that
			// compare with the request's time.
			BeforeEach(func() {
				f = newFixture(rel)
				cfg := evalConfig(rel)
				cfg.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
				f.eval = authz.New(cfg)
				f.env.Authz = f.eval
			})

			deny := func(action, cond string) {
				GinkgoHelper()
				f.setPolicy("priv", doc(stmt("Deny", `"*"`, action, "arn:aws:s3:::priv/*", cond)))
			}

			expectOutcome := func(err error, refused bool) {
				GinkgoHelper()
				if refused {
					Expect(err).To(MatchError(op.ErrAccessDenied))
					return
				}
				Expect(err).NotTo(HaveOccurred())
			}

			// A write's tags reach the policy as s3:RequestObjectTag/<key>, one
			// key for each tag key as the request writes it, so a request can
			// hold the key as the condition writes it beside another spelling.
			// Each spelling counts alone: the key as written by radosgw's
			// reading, another spelling by Amazon S3's answer.
			DescribeTable("counts each spelling of a tag key alone",
				func(cond, tagging string, refused bool) {
					deny("s3:PutObject", cond)
					expectOutcome(f.putObject(alice(), "priv", "o", "X-Amz-Tagging", tagging), refused)
				},
				Entry("a later date at a UTC offset, under another spelling alone",
					`{"DateGreaterThan": {"s3:RequestObjectTag/when": "2024-01-01T00:00:00Z"}}`,
					"When=2030-01-01T00%3A00%3A00%2B00%3A00", true),
				Entry("a later date at a UTC offset, beside the key as written with an earlier date",
					`{"DateGreaterThan": {"s3:RequestObjectTag/when": "2024-01-01T00:00:00Z"}}`,
					"when=2020-01-01T00%3A00%3A00Z&When=2030-01-01T00%3A00%3A00%2B00%3A00", true),
				Entry("an earlier date under the key as written alone",
					`{"DateGreaterThan": {"s3:RequestObjectTag/when": "2024-01-01T00:00:00Z"}}`,
					"when=2020-01-01T00%3A00%3A00Z", false),
				Entry("one less than a number past 2^53, under another spelling alone",
					`{"NumericNotEquals": {"s3:RequestObjectTag/n": "9007199254740993"}}`,
					"N=9007199254740992", true),
				Entry("one less than a number past 2^53, beside the key as written with the number",
					`{"NumericNotEquals": {"s3:RequestObjectTag/n": "9007199254740993"}}`,
					"n=9007199254740993&N=9007199254740992", true),
				Entry("the number past 2^53 under the key as written alone",
					`{"NumericNotEquals": {"s3:RequestObjectTag/n": "9007199254740993"}}`,
					"n=9007199254740993", false),
				Entry("a pattern's own brackets, under another spelling alone",
					`{"StringLike": {"s3:RequestObjectTag/path": "home/[a]/*"}}`,
					"Path=home%2F%5Ba%5D%2Fx", true),
				Entry("a pattern's own brackets, beside the key as written with another path",
					`{"StringLike": {"s3:RequestObjectTag/path": "home/[a]/*"}}`,
					"path=tmp%2Fx&Path=home%2F%5Ba%5D%2Fx", true),
				Entry("another path under the key as written alone",
					`{"StringLike": {"s3:RequestObjectTag/path": "home/[a]/*"}}`,
					"path=tmp%2Fx", false),
			)

			// The list is the IAM User Guide's own, for covering both
			// families
			// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L268-L272).
			DescribeTable("answers an address against a list of ranges of both families",
				func(operator, remote string, refused bool) {
					deny("s3:GetObject", `{"`+operator+`": {"aws:SourceIP": ["203.0.113.0/24", "2001:DB8:1234:5678::/64"]}}`)
					r := f.req(alice(), "priv", "o")
					r.RemoteAddr = remote
					expectOutcome(f.eval.VerifyObject(f.ctx, r, policy.S3GetObject, acl.PermRead), refused)
				},
				Entry("IpAddress, from an address outside both ranges", "IpAddress", "10.9.9.9:1", false),
				Entry("IpAddress, from an address within the IPv4 range", "IpAddress", "203.0.113.7:1", true),
				Entry("IpAddress, from an address within the IPv6 range", "IpAddress", "[2001:db8:1234:5678::1]:1", true),
				Entry("NotIpAddress, from an address outside both ranges", "NotIpAddress", "10.9.9.9:1", true),
				Entry("NotIpAddress, from an address within the IPv4 range", "NotIpAddress", "203.0.113.7:1", false),
				Entry("NotIpAddress, from an address within the IPv6 range", "NotIpAddress", "[2001:db8:1234:5678::1]:1", false),
			)

			// aws:CurrentTime is the request's time, noon on 2026-11-20. A date
			// without a time is any instant of its day in any time zone.
			DescribeTable("answers a date without a time where every instant of its day agrees",
				func(cond string, refused bool) {
					deny("s3:GetObject", cond)
					expectOutcome(f.getObject(alice(), "priv", "o"), refused)
				},
				Entry("before a day long past", `{"DateLessThan": {"AWS:CurrentTime": "2020-01-01"}}`, false),
				Entry("after a day long past", `{"DateGreaterThan": {"AWS:CurrentTime": "2020-01-01"}}`, true),
				Entry("before the day that holds the request's time", `{"DateLessThan": {"AWS:CurrentTime": "2026-11-20"}}`, true),
				Entry("after a day past 2554, which a 64-bit count of nanoseconds wraps to 2015",
					`{"DateGreaterThan": {"AWS:CurrentTime": "2600-01-01"}}`, false),
				Entry("before a day past 2554", `{"DateLessThan": {"AWS:CurrentTime": "2600-01-01"}}`, true),
			)

			// radosgw sets aws:EpochTime to an ISO 8601 time, which is no
			// number
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L853-L854,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L888-L889),
			// and rgw-go sets what radosgw sets.
			DescribeTable("holds a Numeric operator on aws:EpochTime in another case, whose value is a date and time",
				func(cond string, refused bool) {
					deny("s3:GetObject", cond)
					expectOutcome(f.getObject(alice(), "priv", "o"), refused)
				},
				Entry("a Numeric operator on the key in another case", `{"NumericLessThan": {"aws:epochtime": "1700000000"}}`, true),
				Entry("a Numeric operator on the key as written, as radosgw answers it",
					`{"NumericLessThan": {"aws:EpochTime": "1700000000"}}`, false),
				Entry("a Date operator on the key in another case", `{"DateLessThan": {"aws:epochtime": "1700000000"}}`, false),
			)
		})
	}
})
