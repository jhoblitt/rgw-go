package s3_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// keyStatement is a statement giving user effect on actions over plain's
// objects under cond, a Condition object's JSON, or none when cond is empty.
func keyStatement(effect, user, cond string, actions ...string) string {
	acts, err := json.Marshal(actions)
	Expect(err).NotTo(HaveOccurred())
	c := ""
	if cond != "" {
		c = `,"Condition":` + cond
	}
	return `{"Effect":"` + effect + `","Principal":{"AWS":["arn:aws:iam:::user/` + user + `"]},"Action":` + string(acts) +
		`,"Resource":["arn:aws:s3:::plain/*"]` + c + `}`
}

// keyPolicy is a bucket policy of statements.
func keyPolicy(statements ...string) []byte {
	return []byte(`{"Version":"2012-10-17","Statement":[` + strings.Join(statements, ",") + `]}`)
}

// The locks the specs' writes name: retentions to 2030, past every guardrail
// they set, and to 2025-10-01, within all of them, four days after the
// world's clock; and legal holds.
// lockLateWords is a date iso8601.Parse ends at its white space, where as_date
// refuses the whole string.
const (
	lockSoon      = "2025-10-01T00:00:00Z"
	lockLateWords = "2030-06-15 and some words"
)

var (
	complianceLate  = []string{"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader}
	governanceLate  = []string{"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader}
	governanceWords = []string{"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockLateWords}
	governanceSoon  = []string{"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockSoon}
	holdOn          = []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}
	holdOff         = []string{"X-Amz-Object-Lock-Legal-Hold", "OFF"}
)

// The conditions the specs' policies name.
const (
	modeCompliance = `{"StringEquals":{"s3:object-lock-mode":"COMPLIANCE"}}`
	modeGovernance = `{"StringEquals":{"s3:object-lock-mode":"GOVERNANCE"}}`
	over30Days     = `{"NumericGreaterThan":{"s3:object-lock-remaining-retention-days":"30"}}`
	over10Days     = `{"NumericGreaterThan":{"s3:object-lock-remaining-retention-days":"10"}}`
	after2026      = `{"DateGreaterThan":{"s3:object-lock-retain-until-date":"2026-01-01T00:00:00Z"}}`
	holdIsOn       = `{"StringEquals":{"s3:object-lock-legal-hold":"ON"}}`
	holdIsOff      = `{"StringEquals":{"s3:object-lock-legal-hold":"OFF"}}`
	atMost30Days   = `{"NumericLessThanEquals":{"s3:object-lock-remaining-retention-days":"30"}}`
	atMost10Days   = `{"NumericLessThanEquals":{"s3:object-lock-remaining-retention-days":"10"}}`
	before2026     = `{"DateLessThan":{"s3:object-lock-retain-until-date":"2026-01-01T00:00:00Z"}}`
	// modePresent is AWS's way to forbid a write that sets a retention:
	// Null false holds where the key is present.
	modePresent = `{"Null":{"s3:object-lock-mode":"false"}}`
)

var _ = Describe("the object-lock condition keys a policy names", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.lockPlain(ctx, nil)
				cfg := authz.DefaultConfig(rel)
				cfg.Now = w.env.Now
				w.env.Authz = authz.New(cfg)
			})
			setPolicy := func(ctx context.Context, statements ...string) {
				GinkgoHelper()
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: keyPolicy(statements...)})
			}

			Describe("on the writes whose headers name a lock", func() {
				// writes are who's three writes of k in plain naming hdr, a
				// copy's source plain's src.
				var writes map[string]func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder
				BeforeEach(func() {
					writes = map[string]func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder{
						"PutObject": func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder {
							return w.send(who, http.MethodPut, "/plain/k", "x", hdr...)
						},
						"CopyObject": func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder {
							return w.send(who, http.MethodPut, "/plain/k", "", append([]string{"X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
						},
						"CreateMultipartUpload": func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder {
							return w.send(who, http.MethodPost, "/plain/k?uploads", "", hdr...)
						},
					}
				})
				expectServed := func(who *op.UserRecord, hdr []string) {
					GinkgoHelper()
					for name, write := range writes {
						rec := write(who, hdr...)
						Expect(rec.Code).To(Equal(200), "%s %v: %s", name, hdr, rec.Body.String())
					}
				}
				// expectRefused checks that every write as who naming hdr is
				// answered status and code, writing no version and creating no
				// upload.
				expectRefused := func(ctx context.Context, who *op.UserRecord, hdr []string, status int, code string) {
					GinkgoHelper()
					for name, write := range writes {
						rec := write(who, hdr...)
						Expect(rec.Code).To(Equal(status), "%s %v: %s", name, hdr, rec.Body.String())
						Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code>"), name)
					}
					Expect(w.versionsOf(ctx, "k")).To(BeEmpty())
					Expect(w.uploads(ctx)).To(BeEmpty())
				}

				DescribeTable("refuses the bucket's owner each write a Deny on a key matches, and serves one it does not and one naming no lock",
					func(ctx SpecContext, action, cond string, matching, other []string) {
						setPolicy(ctx, keyStatement("Deny", "alice", cond, action))
						expectRefused(ctx, w.alice, matching, 403, "AccessDenied")
						expectServed(w.alice, other)
						expectServed(w.alice, nil)
					},
					Entry("s3:PutObject on the mode COMPLIANCE", "s3:PutObject", modeCompliance, complianceLate, governanceLate),
					Entry("s3:PutObjectRetention on the mode COMPLIANCE", "s3:PutObjectRetention", modeCompliance, complianceLate, governanceLate),
					Entry("s3:PutObject on more than 30 remaining days", "s3:PutObject", over30Days, governanceLate, governanceSoon),
					Entry("s3:PutObjectRetention on more than 30 remaining days", "s3:PutObjectRetention", over30Days, governanceLate, governanceSoon),
					Entry("s3:PutObject on a retain-until date after 2026", "s3:PutObject", after2026, governanceLate, governanceSoon),
					Entry("s3:PutObject on a retain-until date after 2026, sent with words after it", "s3:PutObject", after2026, governanceWords, governanceSoon),
					Entry("s3:PutObject on the legal hold ON", "s3:PutObject", holdIsOn, holdOn, holdOff),
					Entry("s3:PutObjectLegalHold on the legal hold ON", "s3:PutObjectLegalHold", holdIsOn, holdOn, holdOff),
				)
				DescribeTable("lets an Allow conditioned on a key pass bob's write only for a lock it matches, never for one naming none",
					func(ctx SpecContext, action, cond string, matching, other []string) {
						setPolicy(ctx, keyStatement("Allow", "bob", "", "s3:GetObject"),
							keyStatement("Allow", "bob", cond, "s3:PutObject", action))
						expectRefused(ctx, w.bob, other, 403, "AccessDenied")
						expectRefused(ctx, w.bob, nil, 403, "AccessDenied")
						expectServed(w.bob, matching)
					},
					Entry("the mode GOVERNANCE, with s3:PutObjectRetention", "s3:PutObjectRetention", modeGovernance, governanceLate, complianceLate),
					Entry("at most 30 remaining days, with s3:PutObjectRetention", "s3:PutObjectRetention", atMost30Days, governanceSoon, governanceLate),
					Entry("a retain-until date before 2026, with s3:PutObjectRetention", "s3:PutObjectRetention", before2026, governanceSoon, governanceLate),
					Entry("the legal hold OFF, with s3:PutObjectLegalHold", "s3:PutObjectLegalHold", holdIsOff, holdOff, holdOn),
				)
				It("takes a Deny on Null false for the mode as the release's radosgw takes Null, which on Squid serves the writes it means to refuse", func(ctx SpecContext) {
					setPolicy(ctx, keyStatement("Deny", "alice", modePresent, "s3:PutObject"))
					if rel == denc.Squid {
						expectRefused(ctx, w.alice, nil, 403, "AccessDenied")
						expectServed(w.alice, complianceLate)
						return
					}
					expectRefused(ctx, w.alice, complianceLate, 403, "AccessDenied")
					expectServed(w.alice, nil)
				})
				It("refuses a lock header that does not parse 400 InvalidArgument, whatever a Deny on its key says, storing nothing", func(ctx SpecContext) {
					setPolicy(ctx, keyStatement("Deny", "alice", modeCompliance, "s3:PutObject"))
					for _, hdr := range [][]string{
						{"X-Amz-Object-Lock-Mode", "compliance", "X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader},
						{"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", "not-a-date"},
						{"X-Amz-Object-Lock-Mode", "COMPLIANCE"},
						{"X-Amz-Object-Lock-Legal-Hold", "on"},
					} {
						expectRefused(ctx, w.alice, hdr, 400, "InvalidArgument")
					}
				})
				It("stores the lock of a write a Deny on its key does not match", func(ctx SpecContext) {
					setPolicy(ctx, keyStatement("Deny", "alice", modeGovernance, "s3:PutObject"))
					attrs := w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/k", "x", lockHeaders...), "k")
					expectLock(attrs)
				})
			})

			Describe("on PutObjectRetention and PutObjectLegalHold", func() {
				var v string
				BeforeEach(func(ctx SpecContext) {
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					v = exactHeader(rec.Header(), "x-amz-version-id")
				})
				put := func(who *op.UserRecord, doc string, hdr ...string) *httptest.ResponseRecorder {
					target := "/plain/k?retention&versionId=" + v
					if strings.HasPrefix(doc, "<LegalHold") {
						target = "/plain/k?legal-hold&versionId=" + v
					}
					return w.send(who, http.MethodPut, target, doc, hdr...)
				}
				lockOf := func(ctx context.Context) (retention, hold []byte) {
					GinkgoHelper()
					return w.versionAttr(ctx, "k", v, meta.AttrObjectRetention), w.versionAttr(ctx, "k", v, meta.AttrObjectLegalHold)
				}
				expectNoLock := func(ctx context.Context) {
					GinkgoHelper()
					r, h := lockOf(ctx)
					Expect(r).To(BeNil())
					Expect(h).To(BeNil())
				}

				DescribeTable("refuses the bucket's owner each document a Deny on a key matches, and serves one it does not",
					func(ctx SpecContext, action, cond, matching, other string) {
						setPolicy(ctx, keyStatement("Deny", "alice", cond, action))
						expectError(put(w.alice, matching), 403, "AccessDenied")
						expectNoLock(ctx)
						Expect(put(w.alice, other).Code).To(Equal(200))
					},
					Entry("s3:PutObjectRetention on the mode COMPLIANCE", "s3:PutObjectRetention", modeCompliance,
						retentionBody("COMPLIANCE", lockUntilHeader), retentionBody("GOVERNANCE", lockUntilHeader)),
					Entry("AWS's guardrail, s3:PutObjectRetention on more than 10 remaining days", "s3:PutObjectRetention", over10Days,
						retentionBody("GOVERNANCE", lockUntilHeader), retentionBody("GOVERNANCE", lockSoon)),
					Entry("s3:PutObjectRetention on a retain-until date after 2026", "s3:PutObjectRetention", after2026,
						retentionBody("GOVERNANCE", lockUntilHeader), retentionBody("GOVERNANCE", lockSoon)),
					Entry("s3:PutObjectRetention on a retain-until date after 2026, written with words after it", "s3:PutObjectRetention", after2026,
						retentionBody("GOVERNANCE", lockLateWords), retentionBody("GOVERNANCE", lockSoon)),
					Entry("s3:PutObjectLegalHold on the legal hold ON", "s3:PutObjectLegalHold", holdIsOn,
						legalHoldBody("ON"), legalHoldBody("OFF")),
				)
				DescribeTable("lets an Allow conditioned on a key pass bob's document only for a lock it matches",
					func(ctx SpecContext, action, cond, matching, other string) {
						setPolicy(ctx, keyStatement("Allow", "bob", cond, action))
						expectError(put(w.bob, other), 403, "AccessDenied")
						expectNoLock(ctx)
						Expect(put(w.bob, matching).Code).To(Equal(200))
					},
					Entry("the mode GOVERNANCE", "s3:PutObjectRetention", modeGovernance,
						retentionBody("GOVERNANCE", lockUntilHeader), retentionBody("COMPLIANCE", lockUntilHeader)),
					Entry("at most 10 remaining days", "s3:PutObjectRetention", atMost10Days,
						retentionBody("GOVERNANCE", lockSoon), retentionBody("GOVERNANCE", lockUntilHeader)),
					Entry("a retain-until date before 2026", "s3:PutObjectRetention", before2026,
						retentionBody("GOVERNANCE", lockSoon), retentionBody("GOVERNANCE", lockUntilHeader)),
					Entry("the legal hold ON", "s3:PutObjectLegalHold", holdIsOn, legalHoldBody("ON"), legalHoldBody("OFF")),
				)
				It("answers a document that does not parse 400 MalformedXML under a Deny on its key, and still 403 to a requester no policy allows", func(ctx SpecContext) {
					setPolicy(ctx, keyStatement("Deny", "alice", modeCompliance, "s3:PutObjectRetention"),
						keyStatement("Deny", "alice", holdIsOn, "s3:PutObjectLegalHold"))
					for _, doc := range []string{
						"<Retention>",
						retentionBody("compliance", lockUntilHeader),
						retentionBody("COMPLIANCE", "not-a-date"),
						legalHoldBody("on"),
					} {
						expectError(put(w.alice, doc), 400, "MalformedXML")
						expectError(put(w.bob, doc), 403, "AccessDenied")
					}
					expectNoLock(ctx)
				})
				It("leaves the governance bypass unbound by a Deny on the mode, as the Reference lists no lock key for it", func(ctx SpecContext) {
					Expect(put(w.alice, retentionBody("GOVERNANCE", lockUntilHeader)).Code).To(Equal(200))
					setPolicy(ctx, keyStatement("Deny", "alice", modeGovernance, "s3:BypassGovernanceRetention"))
					Expect(put(w.alice, retentionBody("GOVERNANCE", lockSoon), "X-Amz-Bypass-Governance-Retention", "true").Code).To(Equal(200))
					r, _ := lockOf(ctx)
					Expect(r).To(Equal(locktest.RetentionAttr("GOVERNANCE", time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC))))
				})
			})
		})
	}
})
