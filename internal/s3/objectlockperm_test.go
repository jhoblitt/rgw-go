package s3_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// bucketPolicy is a policy of one statement giving user effect on actions
// over plain and its objects.
func bucketPolicy(effect, user string, actions ...string) []byte {
	acts, err := json.Marshal(actions)
	Expect(err).NotTo(HaveOccurred())
	return []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Principal":{"AWS":["arn:aws:iam:::user/` + user +
		`"]},"Action":` + string(acts) + `,"Resource":["arn:aws:s3:::plain","arn:aws:s3:::plain/*"]}]}`)
}

// lockRetentionHeaders and lockHoldHeaders name a COMPLIANCE retention to
// lockUntil and a legal hold ON.
var (
	lockRetentionHeaders = []string{"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader}
	lockHoldHeaders      = []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}
)

var _ = Describe("the permissions a write's object-lock headers need", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var (
				w *writeWorld
				// writes are who's three writes of k in plain naming hdr, a
				// copy's source plain's src.
				writes map[string]func(who *op.UserRecord, hdr ...string) *httptest.ResponseRecorder
			)
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.lockPlain(ctx, nil)
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
			// expectServed checks that every write as who naming hdr succeeds.
			expectServed := func(who *op.UserRecord, hdr []string) {
				GinkgoHelper()
				for name, write := range writes {
					rec := write(who, hdr...)
					Expect(rec.Code).To(Equal(200), "%s: %s", name, rec.Body.String())
				}
			}
			// expectRefused checks that every write as who naming hdr is 403
			// AccessDenied, writing no version and creating no upload.
			expectRefused := func(ctx context.Context, who *op.UserRecord, hdr []string) {
				GinkgoHelper()
				versions, uploads := len(w.versionsOf(ctx, "k")), len(w.uploads(ctx))
				for name, write := range writes {
					expectError(write(who, hdr...), 403, "AccessDenied")
					Expect(w.versionsOf(ctx, "k")).To(HaveLen(versions), name)
					Expect(w.uploads(ctx)).To(HaveLen(uploads), name)
				}
			}

			It("lets the bucket's owner name both with no policy", func() {
				expectServed(w.alice, append(slices.Clone(lockRetentionHeaders), lockHoldHeaders...))
			})
			It("refuses the bucket's owner a header whose action a policy denies, and serves the other", func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Deny", "alice", "s3:PutObjectRetention")})
				expectRefused(ctx, w.alice, lockRetentionHeaders)
				expectRefused(ctx, w.alice, append(slices.Clone(lockHoldHeaders), lockRetentionHeaders...))
				expectServed(w.alice, lockHoldHeaders)
				expectServed(w.alice, nil)
			})
			DescribeTable("needs each header's own action beside s3:PutObject",
				func(ctx SpecContext, actions []string, retentionServed, holdServed bool) {
					allowed := append([]string{"s3:PutObject", "s3:GetObject"}, actions...)
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", allowed...)})
					for _, c := range []struct {
						hdr    []string
						served bool
					}{{lockRetentionHeaders, retentionServed}, {lockHoldHeaders, holdServed}} {
						if c.served {
							expectServed(w.bob, c.hdr)
						} else {
							expectRefused(ctx, w.bob, c.hdr)
						}
					}
					expectServed(w.bob, nil)
				},
				Entry("s3:PutObject alone", nil, false, false),
				Entry("s3:PutObjectRetention", []string{"s3:PutObjectRetention"}, true, false),
				Entry("s3:PutObjectLegalHold", []string{"s3:PutObjectLegalHold"}, false, true),
				Entry("both", []string{"s3:PutObjectRetention", "s3:PutObjectLegalHold"}, true, true),
			)
			It("refuses a grantee of the bucket's WRITE either header, which no ACL grant authorizes", func(ctx SpecContext) {
				w.grantBob(ctx, acl.PermWrite)
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", "s3:GetObject")})
				expectRefused(ctx, w.bob, lockRetentionHeaders)
				expectRefused(ctx, w.bob, lockHoldHeaders)
				expectServed(w.bob, nil)
			})
			It("refuses a requester lacking the action 403 on a bucket without object lock, where the bucket's owner gets 400 InvalidRequest", func(ctx SpecContext) {
				w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags &^= meta.BucketObjLockEnabled })
				w.grantBob(ctx, acl.PermWrite)
				for _, hdr := range [][]string{lockRetentionHeaders, lockHoldHeaders} {
					expectRefused(ctx, w.bob, hdr)
					for name, write := range writes {
						expectError(write(w.alice, hdr...), 400, "InvalidRequest")
						Expect(w.versionsOf(ctx, "k")).To(BeEmpty(), name)
					}
				}
			})
			It("refuses a CreateMultipartUpload's invalid header value 403 to a requester lacking the action, as its headers are read after the permission check",
				func(ctx SpecContext) {
					w.grantBob(ctx, acl.PermWrite)
					for _, hdr := range [][]string{
						{"X-Amz-Object-Lock-Legal-Hold", "on"},
						{"X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader},
					} {
						expectError(w.send(w.bob, http.MethodPost, "/plain/k?uploads", "", hdr...), 403, "AccessDenied")
						expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", hdr...), 400, "InvalidArgument")
						expectError(w.send(w.bob, http.MethodPut, "/plain/k", "x", hdr...), 400, "InvalidArgument")
					}
					Expect(w.uploads(ctx)).To(BeEmpty())
				})
			It("stores the retention and the legal hold a permitted requester names", func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob",
					"s3:PutObject", "s3:PutObjectRetention", "s3:PutObjectLegalHold")})
				attrs := w.written(ctx, w.send(w.bob, http.MethodPut, "/plain/k", "x", lockHeaders...), "k")
				expectLock(attrs)
			})
			It("checks an UploadPart's headers, which only its part's head stores, against s3:PutObject alone", func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", "s3:PutObject")})
				id := w.initUpload(w.bob, "/plain/k")
				Expect(w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", lockHeaders...).Code).To(Equal(200))
				Expect(w.parts(ctx, "k", id)).To(HaveLen(1))
			})
		})
	}
})

// The retentions the dedicated ops' specs put on a version: a COMPLIANCE one
// longer than any the version holds, and a GOVERNANCE one shorter than
// oneDay's.
var (
	lockLonger  = retentionBody("COMPLIANCE", "2030-01-01T00:00:00Z")
	lockShorter = retentionBody("GOVERNANCE", "2026-01-01T00:00:00Z")
)

var _ = Describe("the permissions PutObjectRetention and PutObjectLegalHold need", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.lockPlain(ctx, nil)
			})
			// write is who's PutObject of k with hdr, and the version it wrote.
			write := func(who *op.UserRecord, hdr ...string) string {
				GinkgoHelper()
				rec := w.send(who, http.MethodPut, "/plain/k", "x", hdr...)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				return exactHeader(rec.Header(), "x-amz-version-id")
			}
			retention := func(who *op.UserRecord, v, doc string, hdr ...string) *httptest.ResponseRecorder {
				return w.send(who, http.MethodPut, "/plain/k?retention&versionId="+v, doc, hdr...)
			}
			hold := func(who *op.UserRecord, v, status string) *httptest.ResponseRecorder {
				return w.send(who, http.MethodPut, "/plain/k?legal-hold&versionId="+v, legalHoldBody(status))
			}
			lockOf := func(ctx context.Context, v string) (retention, hold []byte) {
				GinkgoHelper()
				return w.versionAttr(ctx, "k", v, meta.AttrObjectRetention), w.versionAttr(ctx, "k", v, meta.AttrObjectLegalHold)
			}
			expectBypassRefused := func(rec *httptest.ResponseRecorder) {
				GinkgoHelper()
				expectError(rec, 403, "AccessDenied")
				Expect(rec.Body.String()).To(ContainSubstring("governance bypass check failed"))
			}

			It("refuses a requester allowed s3:PutObject alone a lock on the version it wrote, in its write or after it", func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", "s3:PutObject")})
				expectError(w.send(w.bob, http.MethodPut, "/plain/k", "x", lockRetentionHeaders...), 403, "AccessDenied")
				v := write(w.bob)
				expectError(retention(w.bob, v, lockLonger), 403, "AccessDenied")
				expectError(hold(w.bob, v, "ON"), 403, "AccessDenied")
				r, h := lockOf(ctx, v)
				Expect(r).To(BeNil())
				Expect(h).To(BeNil())
			})
			It("lets the bucket's owner lock and release a version another user wrote, with no policy", func(ctx SpecContext) {
				w.grantBob(ctx, acl.PermWrite)
				v := write(w.bob)
				Expect(retention(w.alice, v, lockLonger).Code).To(Equal(200))
				Expect(hold(w.alice, v, "ON").Code).To(Equal(200))
				Expect(hold(w.alice, v, "OFF").Code).To(Equal(200))
				r, h := lockOf(ctx, v)
				Expect(r).NotTo(BeNil())
				Expect(h).To(Equal(locktest.LegalHoldAttr("OFF")))
			})
			It("refuses the version's owner the release of a legal hold the bucket's owner set", func(ctx SpecContext) {
				w.grantBob(ctx, acl.PermWrite)
				v := write(w.bob)
				Expect(hold(w.alice, v, "ON").Code).To(Equal(200))
				expectError(hold(w.bob, v, "OFF"), 403, "AccessDenied")
				_, h := lockOf(ctx, v)
				Expect(h).To(Equal(locktest.LegalHoldAttr("ON")))
			})
			It("refuses the version's owner the governance bypass no policy allows, keeping the default retention", func(ctx SpecContext) {
				w.lockPlain(ctx, oneDay)
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", "s3:PutObject", "s3:PutObjectRetention")})
				v := write(w.bob)
				before, _ := lockOf(ctx, v)
				Expect(before).To(Equal(locktest.RetentionAttr("GOVERNANCE", lockDefaultUntil)))
				expectBypassRefused(retention(w.bob, v, lockShorter, "X-Amz-Bypass-Governance-Retention", "true"))
				after, _ := lockOf(ctx, v)
				Expect(after).To(Equal(before))
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob",
					"s3:PutObject", "s3:PutObjectRetention", "s3:BypassGovernanceRetention")})
				Expect(retention(w.bob, v, lockShorter, "X-Amz-Bypass-Governance-Retention", "true").Code).To(Equal(200))
			})
			It("lets a requester a policy allows set both", func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob",
					"s3:PutObject", "s3:PutObjectRetention", "s3:PutObjectLegalHold")})
				v := write(w.bob)
				Expect(retention(w.bob, v, lockLonger).Code).To(Equal(200))
				Expect(hold(w.bob, v, "ON").Code).To(Equal(200))
			})
			DescribeTable("refuses the bucket's owner the action a policy denies her, and serves the other",
				func(ctx SpecContext, denied string, retentionServed bool) {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Deny", "alice", denied)})
					v := write(w.alice)
					want := map[bool]int{true: 200, false: 403}
					Expect(retention(w.alice, v, lockLonger).Code).To(Equal(want[retentionServed]))
					Expect(hold(w.alice, v, "ON").Code).To(Equal(want[!retentionServed]))
				},
				Entry("s3:PutObjectRetention", "s3:PutObjectRetention", false),
				Entry("s3:PutObjectLegalHold", "s3:PutObjectLegalHold", true),
			)
			It("refuses the bucket's owner the governance bypass a policy denies her", func(ctx SpecContext) {
				w.lockPlain(ctx, oneDay)
				v := write(w.alice)
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Deny", "alice", "s3:BypassGovernanceRetention")})
				expectBypassRefused(retention(w.alice, v, lockShorter, "X-Amz-Bypass-Governance-Retention", "true"))
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bucketPolicy("Allow", "bob", "s3:GetObject")})
				Expect(retention(w.alice, v, lockShorter, "X-Amz-Bypass-Governance-Retention", "true").Code).To(Equal(200))
			})
			It("refuses the anonymous user and bob the WRITE of an object's public-read-write ACL", func(ctx SpecContext) {
				v := write(w.alice, "X-Amz-Acl", "public-read-write")
				anonymous := s3.NewHandler(w.env, s3.AnonymousOnly{}, testConfig(s3.Config{}))
				for _, target := range []string{"/plain/k?retention&versionId=" + v, "/plain/k?legal-hold&versionId=" + v} {
					doc := lockLonger
					if strings.Contains(target, "legal-hold") {
						doc = legalHoldBody("ON")
					}
					expectError(serveReq(anonymous, http.MethodPut, target, strings.NewReader(doc)), 403, "AccessDenied")
					expectError(w.send(w.bob, http.MethodPut, target, doc), 403, "AccessDenied")
				}
				r, h := lockOf(ctx, v)
				Expect(r).To(BeNil())
				Expect(h).To(BeNil())
			})
			DescribeTable("takes a tag-conditioned Deny from the version the request names",
				func(ctx SpecContext, key string) {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[` +
						`{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
						`"Action":["s3:PutObjectRetention","s3:PutObjectLegalHold","s3:BypassGovernanceRetention"],"Resource":["arn:aws:s3:::plain/*"]},` +
						`{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
						`"Action":["s3:PutObjectRetention","s3:PutObjectLegalHold"],"Resource":["arn:aws:s3:::plain/*"],` +
						`"Condition":{"StringEquals":{"` + key + `":"a"}}}]}`)})
					tagged, untagged := write(w.alice, "X-Amz-Tagging", "team=a"), write(w.alice)
					doc := retentionBody("GOVERNANCE", "2030-01-01T00:00:00Z")
					expectError(retention(w.bob, tagged, doc), 403, "AccessDenied")
					expectError(hold(w.bob, tagged, "ON"), 403, "AccessDenied")
					Expect(retention(w.bob, untagged, doc).Code).To(Equal(200))
					Expect(hold(w.bob, untagged, "ON").Code).To(Equal(200))
					Expect(hold(w.bob, untagged, "OFF").Code).To(Equal(200))
					r, h := lockOf(ctx, tagged)
					Expect(r).To(BeNil())
					Expect(h).To(BeNil())
				},
				Entry("s3:ExistingObjectTag", "s3:ExistingObjectTag/team"),
				Entry("s3:ResourceTag", "s3:ResourceTag/team"),
			)

			Describe("account identities", func() {
				// Account a owns plain; aroot is its root and amember a user of
				// it, broot account b's root.
				const (
					acctA = "RGW11111111111111111"
					acctB = "RGW22222222222222222"
				)
				var (
					infoA, infoB          meta.AccountInfo
					aroot, amember, broot meta.UserInfo
				)
				// as is the handler that authenticates every request as u of
				// account a.
				as := func(u *meta.UserInfo, a *meta.AccountInfo) http.Handler {
					return s3.NewHandler(w.env, s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
						return &op.AuthResult{Identity: op.Identity{User: u, Owner: meta.AccountOwner(a.ID), Account: a, OpMask: op.OpTypeAll}}, nil
					}), testConfig(s3.Config{}))
				}
				put := func(h http.Handler, target, body string, hdr ...string) *httptest.ResponseRecorder {
					return serveReq(h, http.MethodPut, target, strings.NewReader(body), hdr...)
				}
				// written is the version h's PutObject of k writes.
				written := func(h http.Handler) string {
					GinkgoHelper()
					rec := put(h, "/plain/k", "x")
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					return exactHeader(rec.Header(), "x-amz-version-id")
				}
				// policyFor is a bucket policy allowing principal actions on
				// plain and its objects.
				policyFor := func(principal string, actions ...string) []byte {
					acts, err := json.Marshal(actions)
					Expect(err).NotTo(HaveOccurred())
					return []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["` + principal +
						`"]},"Action":` + string(acts) + `,"Resource":["arn:aws:s3:::plain","arn:aws:s3:::plain/*"]}]}`)
				}
				BeforeEach(func(ctx SpecContext) {
					infoA, infoB = meta.AccountInfo{ID: acctA, Name: "a"}, meta.AccountInfo{ID: acctB, Name: "b"}
					w.store.AddAccount(infoA)
					w.store.AddAccount(infoB)
					user := func(id, acct string, root bool) meta.UserInfo {
						u := meta.NewUserInfo()
						u.UserID = meta.UserID{ID: id}
						u.DisplayName = id
						u.AccountID = acct
						u.OpMask = op.OpTypeAll
						if root {
							u.Type = meta.IdentityRoot
						}
						w.store.AddUser(u)
						return u
					}
					aroot, amember, broot = user("aroot", acctA, true), user("amember", acctA, false), user("broot", acctB, true)
					owner := meta.AccountOwner(acctA)
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Owner = owner })
					w.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "a"))})
				})

				It("passes the bucket-owning account's root with no policy", func() {
					h := as(&aroot, &infoA)
					v := written(h)
					Expect(put(h, "/plain/k?retention&versionId="+v, retentionBody("GOVERNANCE", "2030-01-01T00:00:00Z")).Code).To(Equal(200))
					Expect(put(h, "/plain/k?legal-hold&versionId="+v, legalHoldBody("ON")).Code).To(Equal(200))
				})
				It("refuses a user of the bucket's account a policy allows s3:PutObject alone", func(ctx SpecContext) {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: policyFor("arn:aws:iam::"+acctA+":user/amember", "s3:PutObject")})
					h := as(&amember, &infoA)
					v := written(h)
					expectError(put(h, "/plain/k?retention&versionId="+v, retentionBody("COMPLIANCE", "2030-01-01T00:00:00Z")), 403, "AccessDenied")
					expectError(put(h, "/plain/k?legal-hold&versionId="+v, legalHoldBody("ON")), 403, "AccessDenied")
					expectError(put(h, "/plain/k", "x", "X-Amz-Object-Lock-Legal-Hold", "ON"), 403, "AccessDenied")
				})
				It("refuses another account's root the lock of the version it wrote until the bucket's policy allows it", func(ctx SpecContext) {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: policyFor("arn:aws:iam::"+acctB+":root", "s3:PutObject")})
					h := as(&broot, &infoB)
					v := written(h)
					expectError(put(h, "/plain/k?retention&versionId="+v, retentionBody("COMPLIANCE", "2030-01-01T00:00:00Z")), 403, "AccessDenied")
					expectError(put(h, "/plain/k?legal-hold&versionId="+v, legalHoldBody("ON")), 403, "AccessDenied")
					r, hl := lockOf(ctx, v)
					Expect(r).To(BeNil())
					Expect(hl).To(BeNil())
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: policyFor("arn:aws:iam::"+acctB+":root", "s3:PutObject", "s3:PutObjectLegalHold")})
					Expect(put(h, "/plain/k?legal-hold&versionId="+v, legalHoldBody("ON")).Code).To(Equal(200))
					Expect(put(as(&aroot, &infoA), "/plain/k?legal-hold&versionId="+v, legalHoldBody("OFF")).Code).To(Equal(200), "the bucket-owning root lifts it")
				})
			})
		})
	}
})
