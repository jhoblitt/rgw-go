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

			// radosgw adds these keys to PutObject's s3:PutObject check alone,
			// and rgw-go to all three writes', so that a policy written for
			// every write of an object binds each. A copy sets its tag header's
			// tags under the REPLACE metadata directive alone, so the copy here
			// replaces its source's metadata.
			Describe("a policy that conditions s3:PutObject on the ACL's and the tags' keys", func() {
				type writeCase struct {
					hdr  []string
					want int
				}
				// eachCase sends every case on each write.
				eachCase := func(ctx context.Context, bucket string, cases []writeCase) {
					GinkgoHelper()
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						for _, c := range cases {
							hdr := c.hdr
							if name == "CopyObject" {
								hdr = slices.Concat(hdr, []string{"X-Amz-Metadata-Directive", "REPLACE"})
							}
							Expect(write(ctx, bucket, "o", hdr...)).To(Equal(c.want), "%s %v", name, c.hdr)
						}
					})
				}
				condition := func(effect, operator, key, value string) string {
					return `{"Effect": "` + effect + `", "Principal": "*", "Action": "s3:PutObject",` +
						` "Resource": "arn:aws:s3:::*", "Condition": {"` + operator + `": {` + strconv.Quote(key) + `: ` + strconv.Quote(value) + `}}}`
				}

				DescribeTable("refuses each write a bucket policy's Deny refuses",
					func(ctx SpecContext, deny string, cases []writeCase) {
						who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
						f.setPolicy("acctbkt", doc(deny))
						eachCase(ctx, "acctbkt", cases)
					},
					Entry("on a canned ACL", condition("Deny", "StringEquals", "s3:x-amz-acl", "public-read"), []writeCase{
						{[]string{setACL, "public-read"}, 403},
						{[]string{setACL, "authenticated-read"}, 200},
						{nil, 200},
					}),
					Entry("on any canned ACL but bucket-owner-full-control",
						condition("Deny", "StringNotEquals", "s3:x-amz-acl", "bucket-owner-full-control"), []writeCase{
							{[]string{setACL, "bucket-owner-full-control"}, 200},
							{[]string{setACL, "public-read"}, 403},
							{nil, 403},
						}),
					Entry("on a grant", condition("Deny", "StringEquals", "s3:x-amz-grant-read", allUsers), []writeCase{
						{[]string{setGrant, allUsers}, 403},
						{nil, 200},
					}),
					Entry("on a tag", condition("Deny", "StringEquals", "s3:RequestObjectTag/classified", "yes"), []writeCase{
						{[]string{setTags, "classified=yes"}, 403},
						{[]string{setTags, "other=1"}, 200},
						{nil, 200},
					}),
					Entry("on any value of a tag but one", condition("Deny", "StringNotEquals", "s3:RequestObjectTag/project", "a"), []writeCase{
						{[]string{setTags, "project=a"}, 200},
						{[]string{setTags, "project=b"}, 403},
						{nil, 403},
					}),
				)

				// bob is no account's, so polbkt's policy alone lets him write.
				DescribeTable("serves each write a bucket policy's conditioned Allow serves",
					func(ctx SpecContext, allow string, cases []writeCase) {
						who = bob()
						f.setPolicy("polbkt", doc(allow,
							`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::polbkt/*"}`))
						eachCase(ctx, "polbkt", cases)
					},
					Entry("on bucket-owner-full-control",
						condition("Allow", "StringEquals", "s3:x-amz-acl", "bucket-owner-full-control"), []writeCase{
							{[]string{setACL, "bucket-owner-full-control"}, 200},
							{[]string{setACL, "public-read"}, 403},
							{nil, 403},
						}),
					Entry("on a tag", condition("Allow", "StringEquals", "s3:RequestObjectTag/project", "a"), []writeCase{
						{[]string{setTags, "project=a"}, 200},
						{[]string{setTags, "project=b"}, 403},
						{nil, 403},
					}),
				)

				// A tag header that does not parse adds no tag keys, so each
				// write that would store it refuses it: PutObject and a copy
				// replacing its metadata before they are authorized,
				// CreateMultipartUpload once authorized, before it creates
				// the upload.
				It("serves no write whose tag header does not parse under a Deny on a tag", func(ctx SpecContext) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
					f.setPolicy("acctbkt", doc(condition("Deny", "StringEquals", "s3:RequestObjectTag/classified", "yes")))
					eachCase(ctx, "acctbkt", []writeCase{
						{[]string{setTags, "classified=yes&"}, 400},
						{[]string{setTags, "classified=yes&a=1&b=2&c=3&d=4&e=5&f=6&g=7&h=8&i=9&j=10"}, 400},
						{[]string{setTags, "classified=yes"}, 403},
						{[]string{setTags, "other=1"}, 200},
					})
					Expect(serve(ctx, h, http.MethodPut, "/acctbkt/k", "", setTags, "classified=yes&", "X-Amz-Copy-Source", "/acctbkt/o")).
						To(Equal(200), "a copy that keeps its source's metadata, and so does not store the header")
				})

				// A copy that replaces its metadata stores its tag header
				// through formatXattr, which Q-encodes one holding a control
				// character or a byte that is not UTF-8 into text that
				// decodes to no tags; PutObject and CreateMultipartUpload
				// store the parsed set.
				It("refuses a copy whose tag header would be stored as other text than the tags its checks judge", func(ctx SpecContext) {
					for _, hdr := range []string{"project=a&z=1\t2", "project=a&z=\xff"} {
						who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
						f.setPolicy("acctbkt", doc(condition("Deny", "StringNotEquals", "s3:RequestObjectTag/project", "a")))
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							want := 200
							if name == "CopyObject" {
								want = 400
							}
							Expect(write(ctx, "acctbkt", "o", setTags, hdr, "X-Amz-Metadata-Directive", "REPLACE")).
								To(Equal(want), "%s %q under the Deny", name, hdr)
						})
						who = bob()
						f.setPolicy("polbkt", doc(condition("Allow", "StringEquals", "s3:RequestObjectTag/project", "a"),
							`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::polbkt/*"}`))
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							want := 200
							if name == "CopyObject" {
								want = 400
							}
							Expect(write(ctx, "polbkt", "o", setTags, hdr, "X-Amz-Metadata-Directive", "REPLACE")).
								To(Equal(want), "%s %q under the Allow", name, hdr)
						})
					}
				})

				// radosgw adds s3:x-amz-acl empty to a PutObject's check that
				// sends no x-amz-acl, and none to a copy's or an upload's: a
				// Deny refuses where it holds with the empty key or without
				// it, and an Allow is judged as radosgw judges it.
				It("judges a Deny on the canned ACL key with and without radosgw's empty key, and an Allow with it", func(ctx SpecContext) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
					f.setPolicy("acctbkt", doc(condition("Deny", "Null", "s3:x-amz-acl", "true")))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o")).To(Equal(403), "%s without the header under a Null Deny", name)
						Expect(write(ctx, "acctbkt", "o", setACL, "private")).To(Equal(200), "%s with it", name)
					})
					f.setPolicy("acctbkt", doc(condition("Deny", "StringEqualsIfExists", "s3:x-amz-acl", "public-read")))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o")).To(Equal(403), "%s without the header under an IfExists Deny", name)
						Expect(write(ctx, "acctbkt", "o", setACL, "private")).To(Equal(200), "%s naming another", name)
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(403), "%s naming it", name)
					})
					who = bob()
					f.setPolicy("polbkt", doc(condition("Allow", "StringEqualsIfExists", "s3:x-amz-acl", "bucket-owner-full-control"),
						`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::polbkt/*"}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						want := 200
						if name == "PutObject" {
							want = 403
						}
						Expect(write(ctx, "polbkt", "o")).To(Equal(want), "%s without the header under an IfExists Allow", name)
						Expect(write(ctx, "polbkt", "o", setACL, "bucket-owner-full-control")).To(Equal(200), "%s naming it", name)
						Expect(write(ctx, "polbkt", "o", setACL, "public-read")).To(Equal(403), "%s naming another", name)
					})
				})

				// sendBody sends one request through h and returns its status and body.
				sendBody := func(ctx context.Context, method, target, body string, hdr ...string) (int, string) {
					var req *http.Request
					if body == "" {
						req = httptest.NewRequestWithContext(ctx, method, target, nil)
						req.Header.Set("Content-Length", "0")
					} else {
						req = httptest.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
					}
					for i := 0; i+1 < len(hdr); i += 2 {
						req.Header.Add(hdr[i], hdr[i+1])
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					GinkgoWriter.Printf("%s %s %v: %d %s\n", method, target, hdr, rec.Code, rec.Body.String())
					return rec.Code, rec.Body.String()
				}
				// startUpload begins an upload of bucket's k with hdr, and returns its id.
				startUpload := func(ctx context.Context, bucket string, hdr ...string) string {
					GinkgoHelper()
					code, body := sendBody(ctx, http.MethodPost, "/"+bucket+"/k?uploads", "", hdr...)
					Expect(code).To(Equal(200))
					_, after, ok := strings.Cut(body, "<UploadId>")
					Expect(ok).To(BeTrue(), body)
					id, _, _ := strings.Cut(after, "</UploadId>")
					return id
				}
				// parts send an UploadPart and an UploadPartCopy from bucket's o
				// into the upload id, each with hdr, and return their statuses.
				parts := func(ctx context.Context, bucket, id string, hdr ...string) (int, int) {
					part, _ := sendBody(ctx, http.MethodPut, "/"+bucket+"/k?partNumber=1&uploadId="+id, "x", hdr...)
					partCopy, _ := sendBody(ctx, http.MethodPut, "/"+bucket+"/k?partNumber=2&uploadId="+id, "",
						append(hdr, "X-Amz-Copy-Source", "/"+bucket+"/o")...)
					return part, partCopy
				}

				It("judges an Allow on the canned ACL key as radosgw does, with its empty key", func(ctx SpecContext) {
					who = bob()
					f.setPolicy("polbkt", doc(readPolbkt, condition("Allow", "StringNotEquals", "s3:x-amz-acl", "public-read")))
					Expect(serve(ctx, h, http.MethodPut, "/polbkt/k", "x")).To(Equal(200), "PutObject without the header")
					id := startUpload(ctx, "polbkt", setACL, "private")
					part, partCopy := parts(ctx, "polbkt", id)
					Expect(part).To(Equal(200), "UploadPart without the header")
					Expect(partCopy).To(Equal(200), "UploadPartCopy without the header")
					Expect(serve(ctx, h, http.MethodPut, "/polbkt/k", "x", setACL, "public-read")).To(Equal(403), "PutObject naming it")

					// Squid's Null holds exactly when the key is absent, and
					// Tentacle's compares its value with that, as radosgw's do.
					f.setPolicy("polbkt", doc(readPolbkt, condition("Allow", "Null", "s3:x-amz-acl", "false")))
					want := 200
					if rel.release == denc.Squid {
						want = 403
					}
					Expect(serve(ctx, h, http.MethodPut, "/polbkt/k", "x")).To(Equal(want), "PutObject without the header under an Allow on Null false")
				})

				It("refuses parts sent without the canned ACL under a Deny for its absence", func(ctx SpecContext) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
					for _, deny := range []string{
						condition("Deny", "Null", "s3:x-amz-acl", "true"),
						condition("Deny", "StringEqualsIfExists", "s3:x-amz-acl", "public-read"),
					} {
						f.setPolicy("acctbkt", doc(deny))
						id := startUpload(ctx, "acctbkt", setACL, "private")
						part, partCopy := parts(ctx, "acctbkt", id)
						Expect(part).To(Equal(403), "UploadPart without the header, %s", deny)
						Expect(partCopy).To(Equal(403), "UploadPartCopy without the header, %s", deny)
						part, partCopy = parts(ctx, "acctbkt", id, setACL, "private")
						Expect(part).To(Equal(200), "UploadPart with it, %s", deny)
						Expect(partCopy).To(Equal(200), "UploadPartCopy with it, %s", deny)
					}
				})

				It("refuses an ACL operation that sets its ACL by its body under a Deny for the header's absence", func(ctx SpecContext) {
					who = alice()
					body := `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID></Owner>` +
						`<AccessControlList><Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser">` +
						`<ID>alice</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`
					targets := []string{"/polbkt/o?acl", "/polbkt/o?acl&versionId=null", "/polbkt?acl"}
					for _, target := range targets {
						code, _ := sendBody(ctx, http.MethodPut, target, body)
						Expect(code).To(Equal(200), "%s with no policy", target)
					}
					for _, cond := range []string{`{"Null": {"s3:x-amz-acl": "true"}}`, `{"StringEqualsIfExists": {"s3:x-amz-acl": "public-read"}}`} {
						f.setPolicy("polbkt", doc(`{"Effect": "Deny", "Principal": "*",`+
							` "Action": ["s3:PutObjectAcl", "s3:PutObjectVersionAcl", "s3:PutBucketAcl"],`+
							` "Resource": ["arn:aws:s3:::polbkt", "arn:aws:s3:::polbkt/*"], "Condition": `+cond+`}`))
						for _, target := range targets {
							code, _ := sendBody(ctx, http.MethodPut, target, body)
							Expect(code).To(Equal(403), "%s by its body under %s", target, cond)
							code, _ = sendBody(ctx, http.MethodPut, target, "", setACL, "private")
							Expect(code).To(Equal(200), "%s by its header under %s", target, cond)
						}
					}
				})

				It("refuses a write that sets its ACL by a grant header alone under a Deny of s3:PutObjectAcl for the canned ACL's absence", func(ctx SpecContext) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
					f.setPolicy("acctbkt", doc(`{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObjectAcl", "Resource": "arn:aws:s3:::*",`+
						` "Condition": {"Null": {"s3:x-amz-acl": "true"}}}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setGrant, allUsers)).To(Equal(403), "%s with a grant header alone", name)
						Expect(write(ctx, "acctbkt", "o", setACL, "public-read")).To(Equal(200), "%s with a canned ACL", name)
						Expect(write(ctx, "acctbkt", "o")).To(Equal(200), "%s setting no ACL", name)
					})
				})

				// An empty x-amz-acl names no canned ACL, as radosgw's
				// s->canned_acl is empty for it as for none, and an empty
				// grant header grants nothing, so a Deny written for the
				// header's absence refuses a write that sends it empty as one
				// that does not send it, and an Allow answers as before.
				It("judges a write that sends an empty ACL header as one that sends none", func(ctx SpecContext) {
					who = withUserPolicy(accountIdentity(ownAcct, "u1", meta.IdentityRGW), doc(policyStmt("Allow", "Action", "*", "s3:*")))
					for _, c := range []struct{ deny, header, value string }{
						{condition("Deny", "Null", "s3:x-amz-acl", "true"), setACL, "private"},
						{condition("Deny", "StringEqualsIfExists", "s3:x-amz-acl", "public-read"), setACL, "private"},
						{condition("Deny", "Null", "s3:x-amz-grant-read", "true"), setGrant, allUsers},
					} {
						f.setPolicy("acctbkt", doc(c.deny))
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							Expect(write(ctx, "acctbkt", "o")).To(Equal(403), "%s without %s, %s", name, c.header, c.deny)
							Expect(write(ctx, "acctbkt", "o", c.header, "")).To(Equal(403), "%s with %s empty, %s", name, c.header, c.deny)
							Expect(write(ctx, "acctbkt", "o", c.header, c.value)).To(Equal(200), "%s with %s set, %s", name, c.header, c.deny)
						})
						id := startUpload(ctx, "acctbkt", c.header, c.value)
						for _, hdr := range [][]string{nil, {c.header, ""}} {
							part, partCopy := parts(ctx, "acctbkt", id, hdr...)
							Expect(part).To(Equal(403), "UploadPart with %v, %s", hdr, c.deny)
							Expect(partCopy).To(Equal(403), "UploadPartCopy with %v, %s", hdr, c.deny)
						}
						part, partCopy := parts(ctx, "acctbkt", id, c.header, c.value)
						Expect(part).To(Equal(200), "UploadPart with %s set, %s", c.header, c.deny)
						Expect(partCopy).To(Equal(200), "UploadPartCopy with %s set, %s", c.header, c.deny)
					}

					// A write makes the s3:PutObjectAcl check for any grant
					// header, an empty one included, and none without one.
					f.setPolicy("acctbkt", doc(`{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObjectAcl", "Resource": "arn:aws:s3:::*",`+
						` "Condition": {"Null": {"s3:x-amz-grant-read": "true"}}}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						Expect(write(ctx, "acctbkt", "o", setGrant, "")).To(Equal(403), "%s with an empty grant header", name)
						Expect(write(ctx, "acctbkt", "o", setGrant, allUsers)).To(Equal(200), "%s with a grant", name)
						Expect(write(ctx, "acctbkt", "o")).To(Equal(200), "%s with no ACL header, which makes no s3:PutObjectAcl check", name)
					})

					who = bob()
					f.setPolicy("polbkt", doc(readPolbkt, condition("Allow", "StringEqualsIfExists", "s3:x-amz-acl", "bucket-owner-full-control")))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						want := 200
						if name == "PutObject" {
							want = 403
						}
						Expect(write(ctx, "polbkt", "o")).To(Equal(want), "%s without the header under an IfExists Allow", name)
						Expect(write(ctx, "polbkt", "o", setACL, "")).To(Equal(want), "%s with it empty under an IfExists Allow", name)
					})
				})

				// A grant header whose items, as the write parses them, name no
				// grantee grants nothing, as an empty one does; one whose item
				// does not parse is sent, and the write refuses it once
				// authorized; one naming the owner names a grantee.
				It("judges a grant header that names no grantee as one not sent, under a Deny for its absence", func(ctx SpecContext) {
					who = alice()
					f.setPolicy("polbkt", doc(`{"Effect": "Deny", "Principal": "*", "Action": ["s3:PutObject", "s3:PutObjectAcl"],`+
						` "Resource": "arn:aws:s3:::polbkt/*", "Condition": {"Null": {"s3:x-amz-grant-read": "true"}}}`))
					for _, c := range []struct {
						value string
						want  int
					}{
						{"", 403},
						{",", 403},
						{",,,", 403},
						{", ,", 400},
						{`id="alice"`, 200},
						{`id="alice",`, 200},
					} {
						each(func(name string, write func(context.Context, string, string, ...string) int) {
							Expect(write(ctx, "polbkt", "o", setGrant, c.value)).To(Equal(c.want), "%s with %q", name, c.value)
						})
						code, _ := sendBody(ctx, http.MethodPut, "/polbkt/o?acl", "", setGrant, c.value)
						Expect(code).To(Equal(c.want), "PutObjectAcl with %q", c.value)
					}
				})

				// radosgw adds no grant key to a copy's or an upload's check,
				// and an empty grant header grants nothing, so it adds none
				// there either; a PutObject's check holds it empty, as
				// radosgw's does.
				It("judges a copy or an upload that sends an empty grant header as one that sends none, under an Allow", func(ctx SpecContext) {
					who = bob()
					f.setPolicy("polbkt", doc(readPolbkt, `{"Effect": "Allow", "Principal": "*", "Action": ["s3:PutObject", "s3:PutObjectAcl"],`+
						` "Resource": "arn:aws:s3:::polbkt/*", "Condition": {"StringNotEquals": {"s3:x-amz-grant-read": "foo"}}}`))
					each(func(name string, write func(context.Context, string, string, ...string) int) {
						withEmpty := 403
						if name == "PutObject" {
							withEmpty = 200
						}
						Expect(write(ctx, "polbkt", "o")).To(Equal(403), "%s without a grant header", name)
						Expect(write(ctx, "polbkt", "o", setGrant, "")).To(Equal(withEmpty), "%s with it empty", name)
						Expect(write(ctx, "polbkt", "o", setGrant, allUsers)).To(Equal(200), "%s with a grant", name)
					})
				})

				It("judges a copy that keeps its source's metadata without the tags of its tag header, which it does not set", func(ctx SpecContext) {
					copyKeeping := func(hdr ...string) int {
						return serve(ctx, h, http.MethodPut, "/polbkt/k", "", append(hdr, "X-Amz-Copy-Source", "/polbkt/o")...)
					}
					who = bob()
					readO := `{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::polbkt/*"}`
					writeO := `{"Effect": "Allow", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::polbkt/*"}`
					f.setPolicy("polbkt", doc(readO, condition("Allow", "StringEquals", "s3:RequestObjectTag/project", "a")))
					Expect(copyKeeping(setTags, "project=a")).To(Equal(403), "an Allow on the tag")
					Expect(copyKeeping(setTags, "project=a", "X-Amz-Metadata-Directive", "COPY")).To(Equal(403), "an Allow on the tag, under COPY")
					f.setPolicy("polbkt", doc(readO, writeO, condition("Deny", "StringNotEquals", "s3:RequestObjectTag/project", "a")))
					Expect(copyKeeping(setTags, "project=a")).To(Equal(403), "a Deny on any other value of the tag")
					f.setPolicy("polbkt", doc(readO, writeO, condition("Deny", "StringEquals", "s3:x-amz-acl", "public-read")))
					Expect(copyKeeping(setACL, "public-read")).To(Equal(403), "a Deny on the ACL, which a copy sets under every directive")
					Expect(copyKeeping(setACL, "authenticated-read")).To(Equal(200), "the same Deny, on another ACL")
				})
			})
		})
	}
})

// readPolbkt is a bucket policy statement letting everyone read polbkt's objects.
const readPolbkt = `{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::polbkt/*"}`

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
