package authz_test

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// lockedTo is a request's lock of a retention in mode to until and, when
// hold is not empty, a legal hold of that status.
func lockedTo(mode string, until time.Time, hold string) *op.LockConditions {
	l := &op.LockConditions{Retention: &meta.ObjectRetention{Mode: mode, RetainUntil: until}}
	if hold != "" {
		l.LegalHold = &meta.ObjectLegalHold{Status: hold}
	}
	return l
}

// thirtyDays is envClock's instant thirty days on.
var thirtyDays = envClock.AddDate(0, 0, 30)

// The lock keys of a COMPLIANCE retention to thirtyDays and of a legal hold
// ON.
var (
	modeKey  = [2]string{"s3:object-lock-mode", "COMPLIANCE"}
	untilKey = [2]string{"s3:object-lock-retain-until-date", "2026-10-27T12:00:00.500000000Z"}
	daysKey  = [2]string{"s3:object-lock-remaining-retention-days", "30"}
	holdKey  = [2]string{"s3:object-lock-legal-hold", "ON"}
)

var _ = Describe("the object-lock condition keys", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var (
				cfg authz.Config
				sem policy.Semantics
			)
			BeforeEach(func() {
				cfg = envConfig(rel)
				sem = policy.SemanticsFor(rel)
			})
			put := func(lock *op.LockConditions) *op.Request {
				r := envRequest(http.MethodPut, "b", "o", "")
				r.Lock = lock
				return r
			}

			DescribeTable("adds the keys the Service Authorization Reference lists for the action, after the op's own",
				func(a policy.Action, want ...[2]string) {
					Expect(pairsOf(cfg, put(lockedTo("COMPLIANCE", thirtyDays, "ON")), a)).To(Equal(with(baseFor(""), flatten(want)...)))
				},
				Entry("s3:PutObject, all four", policy.S3PutObject,
					[2]string{"s3:x-amz-acl", ""}, modeKey, untilKey, daysKey, holdKey),
				Entry("s3:PutObjectRetention, the retention's three", policy.S3PutObjectRetention, modeKey, untilKey, daysKey),
				Entry("s3:PutObjectLegalHold, the legal hold's", policy.S3PutObjectLegalHold, holdKey),
				Entry("s3:BypassGovernanceRetention, none", policy.S3BypassGovernanceRetention),
				Entry("s3:GetObject, none", policy.S3GetObject),
			)
			It("adds a retention's keys or a legal hold's alone when the request sets that alone", func() {
				Expect(pairsOf(cfg, put(&op.LockConditions{LegalHold: &meta.ObjectLegalHold{Status: "OFF"}}), policy.S3PutObject)).To(Equal(
					with(baseFor(""), "s3:x-amz-acl", "", "s3:object-lock-legal-hold", "OFF")))
				Expect(pairsOf(cfg, put(lockedTo("GOVERNANCE", thirtyDays, "")), policy.S3PutObject)).To(Equal(
					with(baseFor(""), "s3:x-amz-acl", "", "s3:object-lock-mode", "GOVERNANCE", untilKey[0], untilKey[1], daysKey[0], daysKey[1])))
			})
			It("adds none for a request that sets no lock", func() {
				for _, a := range []policy.Action{policy.S3PutObject, policy.S3PutObjectRetention, policy.S3PutObjectLegalHold} {
					var keys []string
					for _, p := range pairsOf(cfg, put(nil), a) {
						keys = append(keys, p[0])
					}
					Expect(keys).NotTo(ContainElement(HavePrefix("s3:object-lock-")), a.String())
				}
			})

			DescribeTable("counts the remaining days up to the next whole day, in seconds past any Duration's range",
				func(until time.Time, want string) {
					env := authz.BuildEnv(cfg, put(lockedTo("GOVERNANCE", until, "")), policy.S3PutObjectRetention)
					Expect(env.Lookup("s3:object-lock-remaining-retention-days")).To(Equal([]string{want}))
				},
				Entry("exactly thirty days", thirtyDays, "30"),
				Entry("thirty days and a nanosecond", thirtyDays.Add(time.Nanosecond), "31"),
				Entry("a nanosecond short of thirty days", thirtyDays.Add(-time.Nanosecond), "30"),
				Entry("a nanosecond", envClock.Add(time.Nanosecond), "1"),
				Entry("the clock's own instant", envClock, "0"),
				Entry("half a day past, which only a document can name and Execute refuses", envClock.Add(-12*time.Hour), "0"),
				Entry("a day past", envClock.AddDate(0, 0, -1), "-1"),
				Entry("real_time's last instant, 2554-07-21T23:34:33.709551615Z", meta.MaxRealTime, "192781"),
			)

			Describe("evaluated by the typed operators, whose Deny would fail open on a value that does not convert", func() {
				holds := func(lock *op.LockConditions, c policy.Condition) bool {
					var a policy.Action
					switch c.Key {
					case "s3:object-lock-legal-hold":
						a = policy.S3PutObjectLegalHold
					default:
						a = policy.S3PutObjectRetention
					}
					return c.Eval(authz.BuildEnv(cfg, put(lock), a), sem)
				}
				DescribeTable("holds for the request's values",
					func(c policy.Condition, want bool) {
						Expect(holds(lockedTo("COMPLIANCE", thirtyDays, "ON"), c)).To(Equal(want))
					},
					Entry("StringEquals mode COMPLIANCE", policy.Condition{Op: policy.OpStringEquals, Key: modeKey[0], Values: []string{"COMPLIANCE"}}, true),
					Entry("StringEquals mode GOVERNANCE", policy.Condition{Op: policy.OpStringEquals, Key: modeKey[0], Values: []string{"GOVERNANCE"}}, false),
					Entry("NumericGreaterThan 29 days", policy.Condition{Op: policy.OpNumericGreaterThan, Key: daysKey[0], Values: []string{"29"}}, true),
					Entry("NumericGreaterThan 30 days", policy.Condition{Op: policy.OpNumericGreaterThan, Key: daysKey[0], Values: []string{"30"}}, false),
					Entry("DateGreaterThan the date's second", policy.Condition{Op: policy.OpDateGreaterThan, Key: untilKey[0], Values: []string{"2026-10-27T12:00:00Z"}}, true),
					Entry("DateGreaterThan its epoch seconds", policy.Condition{Op: policy.OpDateGreaterThan, Key: untilKey[0], Values: []string{"1793102400"}}, true),
					Entry("DateLessThan the date's second", policy.Condition{Op: policy.OpDateLessThan, Key: untilKey[0], Values: []string{"2026-10-27T12:00:00Z"}}, false),
					Entry("StringEquals legal hold ON", policy.Condition{Op: policy.OpStringEquals, Key: holdKey[0], Values: []string{"ON"}}, true),
				)
				It("reads real_time's last instant, a date the parser takes, as that instant", func() {
					limit := lockedTo("GOVERNANCE", meta.MaxRealTime, "")
					Expect(holds(limit, policy.Condition{Op: policy.OpDateEquals, Key: untilKey[0], Values: []string{"2554-07-21T23:34:33.709551615Z"}})).To(BeTrue())
					Expect(holds(limit, policy.Condition{Op: policy.OpNumericGreaterThan, Key: daysKey[0], Values: []string{"36500"}})).To(BeTrue())
				})
				It("finds every key absent for a request that sets no lock, as the operators treat an absent key", func() {
					for _, key := range []string{modeKey[0], untilKey[0], daysKey[0], holdKey[0]} {
						Expect(holds(nil, policy.Condition{Op: policy.OpStringEquals, Key: key, Values: []string{"COMPLIANCE"}})).To(BeFalse(), key)
						Expect(holds(nil, policy.Condition{Op: policy.OpNumericGreaterThan, Key: key, Values: []string{"0"}, IfExists: true})).To(BeTrue(), key)
						Expect(holds(nil, policy.Condition{Op: policy.OpNull, Key: key, Values: []string{"true"}})).To(BeTrue(), key)
					}
				})
			})
		})
	}
})

// flatten is pairs as with's alternating keys and values.
func flatten(pairs [][2]string) []string {
	var kv []string
	for _, p := range pairs {
		kv = append(kv, p[0], p[1])
	}
	return kv
}
