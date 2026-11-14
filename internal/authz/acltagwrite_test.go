package authz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// policyStmt is a statement of effect over resource that names its actions
// under key, "Action" or "NotAction".
func policyStmt(effect, key, resource string, actions ...string) string {
	quoted := make([]string, len(actions))
	for i, a := range actions {
		quoted[i] = strconv.Quote(a)
	}
	return `{"Effect": "` + effect + `", "` + key + `": [` + strings.Join(quoted, ", ") + `], "Resource": "` + resource + `"}`
}

// The object writes that carry an ACL and tags: Amazon S3 requires
// s3:PutObjectAcl and s3:PutObjectTagging of PutObject for the headers that
// set them, and radosgw checks s3:PutObject alone.
var _ = Describe("an object write's ACL and tags behind the S3 handler", func() {
	for _, rel := range []struct {
		name    string
		release denc.Release
	}{{"Squid", denc.Squid}, {"Tentacle", denc.Tentacle}} {
		Describe("on "+rel.name, func() {
			var (
				f   *fixture
				who op.Identity
				// queryMeta is what the authenticator folds into the headers.
				queryMeta map[string]string
				h         *s3.Handler
			)
			BeforeEach(func() {
				f = newFixture(rel.release)
				queryMeta = nil
				// A write checks its bucket owner's quota, which reads the owner.
				for _, id := range []string{"alice", "bob"} {
					f.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: id}, DisplayName: id, OpMask: op.OpTypeAll})
				}
				f.store.AddAccount(meta.AccountInfo{ID: ownAcct, Name: "own"})
				f.store.AddAccount(meta.AccountInfo{ID: otherAcct, Name: "other"})
				owner := userOwner("", "alice")
				f.bucket("pubw", owner, aclAttr(withGrant(publicRead(owner), groupGrant(acl.GroupAllUsers, acl.PermWrite))))
				f.object("pubw", "src", aclAttr(publicRead(owner)), nil)
				env := &op.Env{
					Zone: f.store, Users: f.store, Accounts: f.store, Buckets: f.store, Objects: f.store, Multipart: f.store,
					Stats: f.store, Usage: f.store, Metadata: f.store, Metrics: op.NopMetrics{},
					Authz: f.eval,
					Now:   func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: "4155-z-zg",
				}
				as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
					return &op.AuthResult{Identity: who, QueryMeta: queryMeta}, nil
				})
				h = s3.NewHandler(env, as, s3.Config{TransIDSuffix: "-4155-z", ServerHeader: "Ceph Object Gateway"})
			})

			// each runs body for every write, naming it.
			each := func(body func(name string, write func(ctx context.Context, bucket, src string, hdr ...string) int)) {
				for _, w := range aclTagWrites {
					body(w.name, func(ctx context.Context, bucket, src string, hdr ...string) int {
						return w.send(ctx, h, bucket, src, hdr...)
					})
				}
			}
			const (
				setACL   = "X-Amz-Acl"
				setGrant = "X-Amz-Grant-Read"
				setTags  = "X-Amz-Tagging"
				allUsers = `uri="http://acs.amazonaws.com/groups/global/AllUsers"`
			)

			// An account's user needs a policy that allows each action.
			DescribeTable("authorizes an account user's writes by the policies that allow or deny the actions their headers set",
				func(ctx SpecContext, policies []string, wantPlain, wantACL, wantTags, wantBoth int) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policies...))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o")).To(Equal(wantPlain), name+" without the headers")
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(wantACL), name+" setting an ACL")
						Expect(write(ctx, "acctbkt", "o", setGrant, allUsers)).To(Equal(wantACL), name+" setting a grant")
						Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(wantTags), name+" setting tags")
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(wantBoth), name+" setting both")
						Expect(write(ctx, "acctbkt", "o", setACL, "private")).To(Equal(wantPlain), name+" naming the private ACL")
						Expect(write(ctx, "acctbkt", "o", setACL, "bucket-owner-full-control")).To(Equal(wantPlain),
							name+" naming bucket-owner-full-control, which s3:PutObject alone allows")
					})
				},
				Entry("allowed s3:PutObject alone", []string{
					policyStmt("Allow", "Action", "*", "s3:PutObject", "s3:GetObject"),
				}, 200, 403, 403, 403),
				Entry("allowed s3:PutObjectAcl too", []string{
					policyStmt("Allow", "Action", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectAcl"),
				}, 200, 200, 403, 403),
				Entry("allowed s3:PutObjectTagging too", []string{
					policyStmt("Allow", "Action", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectTagging"),
				}, 200, 403, 200, 403),
				Entry("allowed both", []string{
					policyStmt("Allow", "Action", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectAcl", "s3:PutObjectTagging"),
				}, 200, 200, 200, 200),
				Entry("allowed every s3 action", []string{policyStmt("Allow", "Action", "*", "s3:*")}, 200, 200, 200, 200),
				Entry("allowed every s3 action but denied s3:PutObjectAcl", []string{
					policyStmt("Allow", "Action", "*", "s3:*"), policyStmt("Deny", "Action", "*", "s3:PutObjectAcl"),
				}, 200, 403, 200, 403),
				Entry("allowed every s3 action but denied s3:PutObjectTagging", []string{
					policyStmt("Allow", "Action", "*", "s3:*"), policyStmt("Deny", "Action", "*", "s3:PutObjectTagging"),
				}, 200, 200, 403, 403),
				Entry("allowed every s3 action but denied both", []string{
					policyStmt("Allow", "Action", "*", "s3:*"), policyStmt("Deny", "Action", "*", "s3:PutObjectAcl", "s3:PutObjectTagging"),
				}, 200, 403, 403, 403),
				Entry("allowed every s3 action but denied all it does not name, s3:PutObjectAcl not named", []string{
					policyStmt("Allow", "Action", "*", "s3:*"),
					policyStmt("Deny", "NotAction", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectTagging"),
				}, 200, 403, 200, 403),
				Entry("allowed every s3 action but denied all it does not name, s3:PutObjectTagging not named", []string{
					policyStmt("Allow", "Action", "*", "s3:*"),
					policyStmt("Deny", "NotAction", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectAcl"),
				}, 200, 200, 403, 403),
				Entry("allowed every s3 action but denied all it does not name, neither named", []string{
					policyStmt("Allow", "Action", "*", "s3:*"),
					policyStmt("Deny", "NotAction", "*", "s3:PutObject", "s3:GetObject"),
				}, 200, 403, 403, 403),
				Entry("allowed every s3 action and denied none it names, both named", []string{
					policyStmt("Allow", "Action", "*", "s3:*"),
					policyStmt("Deny", "NotAction", "*", "s3:PutObject", "s3:GetObject", "s3:PutObjectAcl", "s3:PutObjectTagging"),
				}, 200, 200, 200, 200),
			)

			It("lets an admin through the same denials, as op.Run overrides an access denial", func(ctx SpecContext) {
				who = adminOf(withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(
					policyStmt("Allow", "Action", "*", "s3:*"), policyStmt("Deny", "Action", "*", "s3:PutObjectAcl", "s3:PutObjectTagging"))))
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name)
				})
			})

			It("judges the condition keys of the headers, as the actions' own ops do", func(ctx SpecContext) {
				who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(
					policyStmt("Allow", "Action", "*", "s3:PutObject", "s3:GetObject"),
					`{"Effect": "Allow", "Action": "s3:PutObjectAcl", "Resource": "*",`+
						` "Condition": {"StringEquals": {"s3:x-amz-acl": "authenticated-read"}}}`,
					`{"Effect": "Allow", "Action": "s3:PutObjectTagging", "Resource": "*",`+
						` "Condition": {"StringEquals": {"s3:RequestObjectTag/project": "a"}}}`,
					`{"Effect": "Deny", "Action": "s3:PutObjectTagging", "Resource": "*",`+
						` "Condition": {"StringEquals": {"s3:RequestObjectTag/classified": "yes"}}}`,
				))
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "authenticated-read")).To(Equal(200), name+" naming the canned ACL allowed")
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), name+" naming another")
					Expect(write(ctx, "acctbkt", "o", setTags, "project=a")).To(Equal(200), name+" tagging as allowed")
					Expect(write(ctx, "acctbkt", "o", setTags, "project=b")).To(Equal(403), name+" tagging otherwise")
					Expect(write(ctx, "acctbkt", "o", setTags, "project=a&classified=yes")).To(Equal(403), name+" tagging as denied")
				})
			})

			Describe("who needs what where no policy decides", func() {
				It("passes an account's root, as it passes every action, but for a Deny", func(ctx SpecContext) {
					who = accountIdentity(ownAcct, "root1", meta.IdentityRoot)
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" under no policy")
					})
					who = withUserPolicy(accountIdentity(ownAcct, "root1", meta.IdentityRoot), doc(
						policyStmt("Deny", "Action", "*", "s3:PutObjectAcl")))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
						Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(200), name+" setting tags")
					})
				})

				It("needs a role in an account to be allowed each action, as an account's user does", func(ctx SpecContext) {
					roleIn := func(actions ...string) op.Identity {
						return withUserPolicy(accountIdentity(ownAcct, "role1", meta.IdentityRole), doc(
							policyStmt("Allow", "Action", "arn:aws:s3:::acctbkt/*", actions...)))
					}
					who = roleIn("s3:PutObject", "s3:GetObject")
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o")).To(Equal(200), name+" without the headers")
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
						Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(403), name+" setting tags")
					})
					who = roleIn("s3:PutObject", "s3:GetObject", "s3:PutObjectAcl", "s3:PutObjectTagging")
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" setting both")
					})
				})

				// pubw's ACL lets everyone write, so these requesters pass
				// s3:PutObject by the ACL alone.
				DescribeTable("passes a requester no account holds, which the policies alone refuse by a Deny",
					func(ctx SpecContext, requester func() op.Identity) {
						who = requester()
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							Expect(write(ctx, "pubw", "src", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" under no policy")
						})
						f.setPolicy("pubw", doc(
							`{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObjectAcl", "Resource": "arn:aws:s3:::pubw/*"}`))
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							Expect(write(ctx, "pubw", "src", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
							Expect(write(ctx, "pubw", "src", setTags, "k=v")).To(Equal(200), name+" setting tags")
						})
					},
					Entry("a role outside an account", func() op.Identity {
						role := userIdentity("", "role2")
						role.User.Type = meta.IdentityRole
						return role
					}),
					Entry("the anonymous user", op.Anonymous),
				)
			})

			Describe("a user outside any account, whom the ACLs and policies both reach", func() {
				It("serves the owner of the bucket its ACL and tags, but for a bucket policy's Deny", func(ctx SpecContext) {
					who = alice()
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "polbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" under no policy")
					})
					f.setPolicy("polbkt", doc(
						`{"Effect": "Deny", "Principal": "*", "Action": ["s3:PutObjectAcl"], "Resource": "arn:aws:s3:::polbkt/*"}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "polbkt", "o")).To(Equal(200), name+" without the headers")
						Expect(write(ctx, "polbkt", "o", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
						Expect(write(ctx, "polbkt", "o", setTags, "k=v")).To(Equal(200), name+" setting tags")
					})
					f.setPolicy("polbkt", doc(
						`{"Effect": "Deny", "Principal": "*", "NotAction": ["s3:PutObject", "s3:GetObject", "s3:PutObjectAcl"],`+
							` "Resource": "arn:aws:s3:::polbkt/*"}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "polbkt", "o")).To(Equal(200), name+" without the headers")
						Expect(write(ctx, "polbkt", "o", setACL, "public-read")).To(Equal(200), name+" setting an ACL")
						Expect(write(ctx, "polbkt", "o", setTags, "k=v")).To(Equal(403), name+" setting tags")
					})
				})

				It("serves a stranger the bucket's ACL lets write its ACL and tags, but for a Deny", func(ctx SpecContext) {
					who = bob()
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "pubw", "src")).To(Equal(200), name+" without the headers")
						Expect(write(ctx, "pubw", "src", setACL, "public-read")).To(Equal(200), name+" setting an ACL")
						Expect(write(ctx, "pubw", "src", setTags, "k=v")).To(Equal(200), name+" setting tags")
					})
					f.setPolicy("pubw", doc(
						`{"Effect": "Deny", "Principal": `+awsPrincipal("arn:aws:iam:::user/bob")+`, "Action": "s3:PutObjectAcl",`+
							` "Resource": "arn:aws:s3:::pubw/*"}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "pubw", "src", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
						Expect(write(ctx, "pubw", "src", setTags, "k=v")).To(Equal(200), name+" setting tags")
					})
				})

				It("serves a stranger a bucket policy lets write its ACL and tags too, but for a Deny", func(ctx SpecContext) {
					who = bob()
					bobP := awsPrincipal("arn:aws:iam:::user/bob")
					allowBobWrites := `{"Effect": "Allow", "Principal": ` + bobP + `, "Action": ["s3:PutObject", "s3:GetObject"], "Resource": "arn:aws:s3:::polbkt/*"}`
					f.setPolicy("polbkt", doc(allowBobWrites))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "polbkt", "o")).To(Equal(200), name+" without the headers")
						Expect(write(ctx, "polbkt", "o", setACL, "public-read")).To(Equal(200), name+" setting an ACL")
						Expect(write(ctx, "polbkt", "o", setTags, "k=v")).To(Equal(200), name+" setting tags")
					})
					f.setPolicy("polbkt", doc(allowBobWrites,
						`{"Effect": "Deny", "Principal": `+bobP+`, "NotAction": ["s3:PutObject", "s3:GetObject", "s3:PutObjectAcl"],`+
							` "Resource": "arn:aws:s3:::polbkt/*"}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "polbkt", "o", setACL, "public-read")).To(Equal(200), name+" setting an ACL")
						Expect(write(ctx, "polbkt", "o", setTags, "k=v")).To(Equal(403), name+" setting tags")
					})
				})
			})

			It("needs another account's user to be allowed by its own policies, and refuses a Deny of the bucket's", func(ctx SpecContext) {
				otherRoot := awsPrincipal("arn:aws:iam::" + otherAcct + ":root")
				allowOther := `{"Effect": "Allow", "Principal": ` + otherRoot + `, "Action": ["s3:PutObject", "s3:GetObject"], "Resource": "arn:aws:s3:::acctbkt/*"}`
				f.setPolicy("acctbkt", doc(allowOther))
				other := func(actions ...string) op.Identity {
					return withUserPolicy(accountIdentity(otherAcct, "other", meta.IdentityRGW), doc(
						policyStmt("Allow", "Action", "arn:aws:s3:::acctbkt/*", actions...)))
				}
				who = other("s3:PutObject", "s3:GetObject")
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o")).To(Equal(200), name+" without the headers")
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
					Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(403), name+" setting tags")
				})
				who = other("s3:PutObject", "s3:GetObject", "s3:PutObjectAcl", "s3:PutObjectTagging")
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" setting both")
				})
				f.setPolicy("acctbkt", doc(allowOther,
					`{"Effect": "Deny", "Principal": `+otherRoot+`, "Action": "s3:PutObjectTagging", "Resource": "arn:aws:s3:::acctbkt/*"}`))
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(200), name+" setting an ACL")
					Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(403), name+" setting tags")
				})
			})

			It("passes another account's root as the root it is, which needs no identity policy, but for a Deny of the bucket's", func(ctx SpecContext) {
				otherRoot := awsPrincipal("arn:aws:iam::" + otherAcct + ":root")
				allowOther := `{"Effect": "Allow", "Principal": ` + otherRoot + `, "Action": ["s3:PutObject", "s3:GetObject"], "Resource": "arn:aws:s3:::acctbkt/*"}`
				f.setPolicy("acctbkt", doc(allowOther))
				who = accountIdentity(otherAcct, "root2", meta.IdentityRoot)
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setTags, "k=v")).To(Equal(200), name+" setting both")
				})
				f.setPolicy("acctbkt", doc(allowOther,
					`{"Effect": "Deny", "Principal": `+otherRoot+`, "Action": "s3:PutObjectAcl", "Resource": "arn:aws:s3:::acctbkt/*"}`))
				each(func(name string, write func(context.Context, string, string, ...string) int) {
					Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), name+" setting an ACL")
					Expect(write(ctx, "acctbkt", "o", setTags, "k=v")).To(Equal(200), name+" setting tags")
				})
			})

			// The check and the write read the request's headers through the
			// same primitives, so the value that decides the check is the one
			// the write stores.
			Describe("the header a write's check and its ACL both read", func() {
				allow := func(actions ...string) op.Identity {
					return withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(
						policyStmt("Allow", "Action", "arn:aws:s3:::acctbkt/*", actions...)))
				}
				// readableByAll reports whether the ACL the last write stored
				// on acctbkt's k grants the AllUsers group.
				readableByAll := func() bool {
					GinkgoHelper()
					rec := f.record("", "acctbkt")
					p, err := op.ObjectACLFor(f.ctx, f.state(rec, "k"), rec)
					Expect(err).NotTo(HaveOccurred())
					return slices.ContainsFunc(p.ACL.Grants, func(e acl.GrantEntry) bool {
						return e.Grant.Type == acl.GranteeGroup && e.Grant.Group == acl.GroupAllUsers
					})
				}

				It("takes a repeated x-amz-acl as its last value, for the check and for the ACL stored", func(ctx SpecContext) {
					who = allow("s3:PutObject", "s3:GetObject")
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setACL, "bucket-owner-full-control")).To(Equal(200),
							name+" ending with the ACL that needs no action")
						Expect(write(ctx, "acctbkt", "o", setACL, "bucket-owner-full-control", setACL, "public-read")).To(Equal(403),
							name+" ending with an ACL that needs s3:PutObjectAcl")
					})
					who = allow("s3:PutObject", "s3:GetObject", "s3:PutObjectAcl")
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read", setACL, "bucket-owner-full-control")).To(Equal(200), name)
						// A CreateMultipartUpload keeps the ACL on the upload, not on an object.
						if name != "CreateMultipartUpload" {
							Expect(readableByAll()).To(BeFalse(), name+" storing the last ACL, which grants AllUsers nothing")
						}
						Expect(write(ctx, "acctbkt", "o", setACL, "bucket-owner-full-control", setACL, "public-read")).To(Equal(200), name)
						if name != "CreateMultipartUpload" {
							Expect(readableByAll()).To(BeTrue(), name+" storing the last ACL, public-read")
						}
					})
				})

				// The SigV2 authenticator folds x-amz-meta-* and
				// x-amz-security-token into the headers and no other name
				// (internal/auth/sigv2.go, qsMetaV2), so a result naming
				// x-amz-acl is one it never makes: the spec pins that the
				// check reads whatever the fold sets, as the write does.
				It("takes a header the SigV2 query meta folds in as one sent in the headers, replacing the one sent", func(ctx SpecContext) {
					who = allow("s3:PutObject", "s3:GetObject")
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						queryMeta = map[string]string{"x-amz-acl": "public-read"}
						Expect(write(ctx, "acctbkt", "o")).To(Equal(403), name+" with an ACL only the query meta names")
						Expect(write(ctx, "acctbkt", "o", setACL, "bucket-owner-full-control")).To(Equal(403),
							name+" with the query meta's ACL over the header's")
						queryMeta = map[string]string{"x-amz-acl": "bucket-owner-full-control"}
						Expect(write(ctx, "acctbkt", "o")).To(Equal(200), name+" with an ACL that needs no action")
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(200),
							name+" with the query meta's ACL over the header's")
						queryMeta = map[string]string{"x-amz-tagging": "k=v"}
						Expect(write(ctx, "acctbkt", "o")).To(Equal(403), name+" with tags only the query meta names")
					})
				})
			})
		})
	}
})

// aclTagWrites send each write of key "k" into bucket, with hdr: a
// PutObject, a CopyObject from the bucket's object src, and a
// CreateMultipartUpload.
var aclTagWrites = []struct {
	name string
	send func(ctx context.Context, h *s3.Handler, bucket, src string, hdr ...string) int
}{
	{"PutObject", func(ctx context.Context, h *s3.Handler, bucket, _ string, hdr ...string) int {
		return serve(ctx, h, http.MethodPut, "/"+bucket+"/k", "x", hdr...)
	}},
	{"CopyObject", func(ctx context.Context, h *s3.Handler, bucket, src string, hdr ...string) int {
		return serve(ctx, h, http.MethodPut, "/"+bucket+"/k", "", append(hdr, "X-Amz-Copy-Source", "/"+bucket+"/"+src)...)
	}},
	{"CreateMultipartUpload", func(ctx context.Context, h *s3.Handler, bucket, _ string, hdr ...string) int {
		return serve(ctx, h, http.MethodPost, "/"+bucket+"/k?uploads", "", hdr...)
	}},
}

// serve sends one request through h and returns its status. hdr is header
// name, value pairs added in order, so a name given twice is sent twice.
func serve(ctx context.Context, h *s3.Handler, method, target, body string, hdr ...string) int {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequestWithContext(ctx, method, target, nil)
	} else {
		req = httptest.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	GinkgoWriter.Printf("%s %s %v: %d %s\n", method, target, hdr, rec.Code, rec.Body.String())
	return rec.Code
}
