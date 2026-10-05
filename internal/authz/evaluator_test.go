package authz_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The two accounts of the account specs.
const (
	ownAcct   = "RGW00000000000000001"
	otherAcct = "RGW00000000000000002"
)

// doc is a 2012-10-17 policy of the statements given.
func doc(stmts ...string) string {
	return `{"Version": "2012-10-17", "Statement": [` + strings.Join(stmts, ", ") + `]}`
}

// stmt is one statement; principal is the JSON of its Principal, "" for none,
// and cond the JSON of its Condition, "" for none.
func stmt(effect, principal, action, resource, cond string) string {
	s := `{"Effect": "` + effect + `", "Action": "` + action + `", "Resource": "` + resource + `"`
	if principal != "" {
		s += `, "Principal": ` + principal
	}
	if cond != "" {
		s += `, "Condition": ` + cond
	}
	return s + "}"
}

func awsPrincipal(arn string) string { return `{"AWS": "` + arn + `"}` }

func aclAttr(p acl.Policy) []byte {
	return encoded(func(e *denc.Encoder) { p.Encode(e, denc.Squid) })
}

func pabAttr(b acl.PublicAccessBlock) []byte {
	return encoded(func(e *denc.Encoder) { b.Encode(e, denc.Squid) })
}

func userOwner(tenant, id string) meta.Owner {
	return meta.UserOwner(meta.UserID{Tenant: tenant, ID: id})
}

func ownerACL(o meta.Owner) acl.Policy { return acl.DefaultPolicy(o, o.String()) }

func publicRead(o meta.Owner) acl.Policy {
	owner := acl.Owner{ID: o.String(), DisplayName: o.String()}
	p, err := acl.Canned(owner, owner, "public-read")
	Expect(err).NotTo(HaveOccurred())
	return p
}

func withGrant(p acl.Policy, g acl.Grant) acl.Policy {
	p.ACL.AddGrant(g)
	return p
}

func userGrant(id string, perm acl.Permission) acl.Grant {
	return acl.Grant{Type: acl.GranteeCanonUser, ID: id, Name: id, Permission: perm}
}

func userIdentity(tenant, id string) op.Identity {
	u := meta.NewUserInfo()
	u.UserID = meta.UserID{Tenant: tenant, ID: id}
	u.DisplayName = id
	u.Type = meta.IdentityRGW
	return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), Tenant: tenant, OpMask: op.OpTypeAll, AccessKey: id}
}

func accountIdentity(acct, id string, typ meta.IdentityType) op.Identity {
	i := userIdentity("", id)
	i.User.AccountID = acct
	i.User.Type = typ
	i.Account = &meta.AccountInfo{ID: acct, Name: "acct-" + acct[len(acct)-1:]}
	i.Owner = meta.AccountOwner(acct)
	return i
}

func adminOf(i op.Identity) op.Identity {
	i.Admin = true
	return i
}

func withUserPolicy(i op.Identity, text string) op.Identity {
	i.Attrs = map[string][]byte{attrUserPolicy: encodeUserPolicies(map[string]string{"p": text})}
	return i
}

func evalConfig(r denc.Release) authz.Config {
	return authz.Config{
		Release: r, EnforceSwiftACLs: true, RejectInvalidPrincipals: true,
		RemoteAddrParam: "REMOTE_ADDR", ACLGrantsMaxNum: 100,
	}
}

// fixture is a store seeded with buckets and objects, and an evaluator for
// its release.
type fixture struct {
	ctx   context.Context
	store *memstore.Store
	env   *op.Env
	eval  *authz.Evaluator
}

func newFixture(r denc.Release) *fixture {
	store := memstore.New(memstore.Config{Release: r})
	eval := authz.New(evalConfig(r))
	f := &fixture{
		ctx:   context.Background(),
		store: store,
		eval:  eval,
		env:   &op.Env{Zone: store, Buckets: store, Objects: store, Authz: eval},
	}
	alice := userOwner("", "alice")
	f.bucket("pub", alice, aclAttr(publicRead(alice)))
	f.object("pub", "x", aclAttr(publicRead(alice)), nil)
	f.bucket("priv", alice, aclAttr(ownerACL(alice)))
	f.object("priv", "o", aclAttr(ownerACL(alice)), nil)
	f.object("priv", "shared", aclAttr(withGrant(ownerACL(alice), userGrant("bob", acl.PermRead))), nil)
	f.object("priv", "tagged", aclAttr(ownerACL(alice)), encodeTags("env", "prod"))
	f.object("priv", "acctshared", aclAttr(withGrant(ownerACL(alice), userGrant(ownAcct, acl.PermRead))), nil)
	f.bucket("payer", alice, aclAttr(publicRead(alice)))
	f.object("payer", "o", aclAttr(publicRead(alice)), nil)
	f.update("", "payer", func(i *meta.BucketInfo) { i.RequesterPays = true })
	f.bucket("susp", alice, aclAttr(ownerACL(alice)))
	f.object("susp", "o", aclAttr(ownerACL(alice)), nil)
	f.update("", "susp", func(i *meta.BucketInfo) { i.Flags |= meta.BucketSuspended })
	f.bucket("blocked", alice, aclAttr(publicRead(alice)))
	f.setAttr("", "blocked", attrPublicAccess, pabAttr(acl.PublicAccessBlock{IgnorePublicACLs: true}))
	f.bucket("polbkt", alice, aclAttr(ownerACL(alice)))
	f.object("polbkt", "o", aclAttr(ownerACL(alice)), nil)
	f.object("polbkt", "secret1", aclAttr(ownerACL(alice)), nil)
	f.bucket("bobbkt", userOwner("", "bob"), aclAttr(ownerACL(userOwner("", "bob"))))
	acct := meta.AccountOwner(ownAcct)
	f.bucket("acctbkt", acct, aclAttr(ownerACL(acct)))
	f.object("acctbkt", "o", aclAttr(ownerACL(acct)), nil)
	carol := userOwner("t2", "carol")
	f.tenantBucket("t2", "cb", carol, aclAttr(ownerACL(carol)))
	f.tenantObject("t2", "cb", "o", aclAttr(withGrant(ownerACL(carol), userGrant("bob", acl.PermRead))), nil)
	return f
}

func (f *fixture) bucket(name string, owner meta.Owner, aclBytes []byte) {
	f.tenantBucket("", name, owner, aclBytes)
}

func (f *fixture) tenantBucket(tenant, name string, owner meta.Owner, aclBytes []byte) {
	GinkgoHelper()
	attrs := map[string][]byte{}
	if aclBytes != nil {
		attrs[meta.AttrACL] = aclBytes
	}
	_, err := f.store.CreateBucket(f.ctx, op.CreateBucketParams{Tenant: tenant, Name: name, Owner: owner, Attrs: attrs})
	Expect(err).NotTo(HaveOccurred())
}

func (f *fixture) object(bucket, key string, aclBytes, tagBytes []byte) {
	f.tenantObject("", bucket, key, aclBytes, tagBytes)
}

func (f *fixture) tenantObject(tenant, bucket, key string, aclBytes, tagBytes []byte) {
	GinkgoHelper()
	attrs := map[string][]byte{}
	if aclBytes != nil {
		attrs[meta.AttrACL] = aclBytes
	}
	if tagBytes != nil {
		attrs[tags.Attr] = tagBytes
	}
	rec, err := f.store.GetBucket(f.ctx, tenant, bucket)
	Expect(err).NotTo(HaveOccurred())
	_, err = f.store.PutObject(f.ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("x"), op.PutParams{Attrs: attrs, Size: 1})
	Expect(err).NotTo(HaveOccurred())
}

func (f *fixture) setAttr(tenant, bucket, name string, value []byte) {
	GinkgoHelper()
	rec, err := f.store.GetBucket(f.ctx, tenant, bucket)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.store.PutBucketAttrs(f.ctx, rec, map[string][]byte{name: value}, nil)).To(Succeed())
}

func (f *fixture) setPolicy(bucket, text string) {
	f.setAttr("", bucket, attrIAMPolicy, []byte(text))
}

func (f *fixture) update(tenant, bucket string, change func(*meta.BucketInfo)) {
	GinkgoHelper()
	rec, err := f.store.GetBucket(f.ctx, tenant, bucket)
	Expect(err).NotTo(HaveOccurred())
	change(&rec.Info)
	Expect(f.store.PutBucketInfo(f.ctx, rec)).To(Succeed())
}

func (f *fixture) record(tenant, bucket string) *op.BucketRecord {
	GinkgoHelper()
	rec, err := f.store.GetBucket(f.ctx, tenant, bucket)
	Expect(err).NotTo(HaveOccurred())
	return rec
}

func (f *fixture) state(rec *op.BucketRecord, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := f.store.StatObject(f.ctx, rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred())
	return st
}

// req is a GET from id at 10.9.9.9 for bucket, "tenant:name" naming a tenant,
// and key, loaded as an op's Init loads them: the bucket, when it exists,
// and the object's head, when a key is named. hdr is header name, value
// pairs, added in order.
func (f *fixture) req(id op.Identity, bucket, key string, hdr ...string) *op.Request {
	GinkgoHelper()
	tenant, name, ok := strings.Cut(bucket, ":")
	if !ok {
		tenant, name = id.Tenant, bucket
	}
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Add(hdr[i], hdr[i+1])
	}
	r := &op.Request{
		Method: http.MethodGet, Header: h, Query: url.Values{},
		RemoteAddr: "10.9.9.9:1", Tenant: tenant, Bucket: name,
		Object: meta.ObjKey{Name: key}, Identity: id, Env: f.env,
	}
	if name == "" {
		return r
	}
	rec, err := f.store.GetBucket(f.ctx, tenant, name)
	if err != nil {
		return r
	}
	r.BucketRec = rec
	if key != "" {
		r.ObjState = f.state(rec, key)
	}
	return r
}

func (f *fixture) getObject(id op.Identity, bucket, key string, hdr ...string) error {
	return f.eval.VerifyObject(f.ctx, f.req(id, bucket, key, hdr...), policy.S3GetObject, acl.PermRead)
}

func (f *fixture) list(id op.Identity, bucket string, hdr ...string) error {
	return f.eval.VerifyBucket(f.ctx, f.req(id, bucket, "", hdr...), policy.S3ListBucket, acl.PermRead)
}

func (f *fixture) putObject(id op.Identity, bucket, key string, hdr ...string) error {
	r := f.req(id, bucket, key, hdr...)
	r.Method = http.MethodPut
	return f.eval.VerifyBucket(f.ctx, r, policy.S3PutObject, acl.PermWrite)
}

var (
	alice = func() op.Identity { return userIdentity("", "alice") }
	bob   = func() op.Identity { return userIdentity("", "bob") }
	carol = func() op.Identity { return userIdentity("t2", "carol") }
	anon  = op.Anonymous
)

// The policies several specs give.
var (
	allowBob = stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::polbkt/*", "")
	denyPriv = doc(stmt("Deny", "", "s3:GetObject", "arn:aws:s3:::priv/*", ""))
	allowGet = doc(stmt("Allow", "", "s3:GetObject", "*", ""))
)

var _ = Describe("Evaluator", func() {
	var f *fixture

	BeforeEach(func() {
		f = newFixture(denc.Squid)
	})

	Describe("ACLs alone", func() {
		It("decides by the object and bucket ACLs", func() {
			Expect(f.getObject(alice(), "priv", "o")).To(Succeed(), "the owner reads her object")
			Expect(f.getObject(bob(), "priv", "o")).To(MatchError(op.ErrAccessDenied), "bob reads alice's private object")
			Expect(f.list(anon(), "pub")).To(Succeed(), "AllUsers READ lists the bucket")
			Expect(f.putObject(anon(), "pub", "x")).To(MatchError(op.ErrAccessDenied), "AllUsers has no WRITE")
			Expect(f.getObject(bob(), "priv", "shared")).To(Succeed(), "the object ACL grants bob READ")

			r := f.req(bob(), "priv", "shared")
			r.Method = http.MethodPut
			Expect(f.eval.VerifyObject(f.ctx, r, policy.S3PutObjectAcl, acl.PermWriteACP)).
				To(MatchError(op.ErrAccessDenied), "bob holds no WRITE_ACP")
			Expect(f.eval.VerifyObject(f.ctx, f.req(alice(), "priv", "o"), policy.S3GetObjectAcl, acl.PermReadACP)).
				To(Succeed(), "the owner holds READ_ACP")
		})

		It("ignores public grants under a stored IgnorePublicAcls", func() {
			Expect(f.list(anon(), "blocked")).To(MatchError(op.ErrAccessDenied), "anonymous")
			Expect(f.list(alice(), "blocked")).To(Succeed(), "the owner")
		})

		It("matches a referer grant against the last Referer header, as beast keeps it", func() {
			p := withGrant(ownerACL(userOwner("", "alice")),
				acl.Grant{Type: acl.GranteeReferer, URLSpec: ".example.com", Permission: acl.PermRead})
			f.bucket("ref", userOwner("", "alice"), aclAttr(p))
			Expect(f.list(anon(), "ref", "Referer", "http://www.example.com/")).To(Succeed(), "one matching referer")
			Expect(f.list(anon(), "ref", "Referer", "http://www.example.com/", "Referer", "http://evil.org/")).
				To(MatchError(op.ErrAccessDenied), "the first matches, the last does not")
			Expect(f.list(anon(), "ref", "Referer", "http://evil.org/", "Referer", "http://www.example.com/")).
				To(Succeed(), "the last matches")
		})
	})

	Describe("a subuser", func() {
		var ro op.Identity

		BeforeEach(func() {
			ro = alice()
			ro.SubUser = "ro"
			ro.User.SubUsers = map[string]meta.SubUser{"ro": {Name: "ro", Perm: uint32(acl.PermRead)}}
		})

		It("is held to its permission mask in the ACL fallback", func() {
			Expect(f.getObject(ro, "priv", "o")).To(Succeed(), "READ within the mask")
			Expect(f.putObject(ro, "priv", "x")).To(MatchError(op.ErrAccessDenied),
				"WRITE outside the mask, though the ACL grants alice FULL_CONTROL")
		})

		It("lists its buckets: S3 has no user ACL, so the mask is never checked", func() {
			Expect(f.eval.VerifyUser(f.ctx, f.req(ro, "", ""), policy.S3ListAllMyBuckets)).To(Succeed())
		})
	})

	Describe("a bucket policy", func() {
		It("grants what the ACL does not", func() {
			f.setPolicy("polbkt", doc(allowBob))
			Expect(f.getObject(bob(), "polbkt", "o")).To(Succeed(), "bob reads")
			Expect(f.list(bob(), "polbkt")).To(MatchError(op.ErrAccessDenied), "bob lists: neither grants")
			Expect(f.getObject(carol(), "polbkt", "o")).To(MatchError(op.ErrAccessDenied), "carol is not bob")
		})

		It("denies over the owner's FULL_CONTROL", func() {
			f.setPolicy("polbkt", doc(allowBob,
				stmt("Deny", `"*"`, "s3:GetObject", "arn:aws:s3:::polbkt/secret*", "")))
			Expect(f.getObject(alice(), "polbkt", "secret1")).To(MatchError(op.ErrAccessDenied), "alice reads")
			Expect(f.putObject(alice(), "polbkt", "secret1")).To(Succeed(), "alice writes")
			Expect(f.getObject(bob(), "polbkt", "secret1")).To(MatchError(op.ErrAccessDenied), "bob reads")
		})

		It("evaluates conditions on the request", func() {
			f.setPolicy("polbkt", doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::polbkt/*",
				`{"IpAddress": {"aws:SourceIp": "10.0.0.0/8"}}`)))
			r := f.req(anon(), "polbkt", "o")
			r.RemoteAddr = "10.1.1.1:1"
			Expect(f.eval.VerifyObject(f.ctx, r, policy.S3GetObject, acl.PermRead)).To(Succeed(), "from 10.1.1.1")
			r.RemoteAddr = "192.168.1.1:1"
			Expect(f.eval.VerifyObject(f.ctx, r, policy.S3GetObject, acl.PermRead)).
				To(MatchError(op.ErrAccessDenied), "from 192.168.1.1")
		})
	})

	Describe("stored identity policies", func() {
		It("deny over the object ACL and a bucket policy", func() {
			f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::priv/*", "")))
			Expect(f.getObject(withUserPolicy(bob(), denyPriv), "priv", "shared")).To(MatchError(op.ErrAccessDenied))
			Expect(f.getObject(withUserPolicy(alice(), denyPriv), "priv", "o")).
				To(MatchError(op.ErrAccessDenied), "the owner, under her own Deny")
		})

		It("allow a user outside any account without the ACL", func() {
			allowList := doc(stmt("Allow", "", "s3:ListBucket", "arn:aws:s3:::priv", ""))
			Expect(f.list(withUserPolicy(bob(), allowList), "priv")).To(Succeed())
		})

		It("include the managed policies the user attaches", func() {
			ro := bob()
			ro.Attrs = map[string][]byte{attrManagedPolicy: encodeManagedPolicies(s3ReadOnlyARN)}
			Expect(f.getObject(ro, "priv", "o")).To(Succeed(), "read-only access reads")
			Expect(f.putObject(ro, "priv", "x")).To(MatchError(op.ErrAccessDenied), "and falls back to the ACL to write")
		})

		It("refuse every request, before the op mask, when they do not decode", func() {
			bad := bob()
			bad.Attrs = map[string][]byte{attrUserPolicy: []byte("garbage")}
			for _, err := range []error{
				f.eval.VerifyUser(f.ctx, f.req(bad, "", ""), policy.S3ListAllMyBuckets),
				f.list(bad, "pub"),
				f.getObject(bad, "pub", "x"),
				f.eval.VerifyBucketIn(f.ctx, f.req(bad, "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
					f.record("", "pub"), meta.ObjKey{Name: "x"}),
				f.eval.VerifyObjectIn(f.ctx, f.req(bad, "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
					f.record("", "pub"), f.state(f.record("", "pub"), "x")),
			} {
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked: %v", err)
			}
		})
	})

	Describe("account users", func() {
		It("need an identity-policy Allow and the resource's grant across accounts", func() {
			acct := accountIdentity(ownAcct, "acct", meta.IdentityRGW)
			Expect(f.getObject(acct, "priv", "acctshared")).To(MatchError(op.ErrAccessDenied), "no identity policy")
			Expect(f.getObject(withUserPolicy(acct, allowGet), "priv", "acctshared")).
				To(Succeed(), "identity Allow and an ACL grant to the account")
			Expect(f.getObject(withUserPolicy(acct, allowGet), "priv", "o")).
				To(MatchError(op.ErrAccessDenied), "identity Allow, no grant from the resource")
		})

		It("are allowed within their account only by a policy, or as the account root", func() {
			Expect(f.getObject(accountIdentity(ownAcct, "root", meta.IdentityRoot), "acctbkt", "o")).To(Succeed(), "the root")
			Expect(f.getObject(accountIdentity(ownAcct, "acct", meta.IdentityRGW), "acctbkt", "o")).
				To(MatchError(op.ErrAccessDenied), "a user: the ACL is not consulted")
		})

		It("are refused, before the op mask, when their account is not loaded", func() {
			acct := withUserPolicy(accountIdentity(ownAcct, "acct", meta.IdentityRGW), allowGet)
			acct.Account = nil
			for _, err := range []error{
				f.getObject(acct, "priv", "acctshared"),
				f.eval.VerifyUser(f.ctx, f.req(acct, "", ""), policy.S3ListAllMyBuckets),
			} {
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked: %v", err)
			}
		})

		It("must be allowed ListAllMyBuckets by a policy", func() {
			acct := accountIdentity(ownAcct, "acct", meta.IdentityRGW)
			Expect(f.eval.VerifyUser(f.ctx, f.req(acct, "", ""), policy.S3ListAllMyBuckets)).
				To(MatchError(op.ErrAccessDenied), "no policy")
			acct = withUserPolicy(acct, doc(stmt("Allow", "", "s3:ListAllMyBuckets", "*", "")))
			Expect(f.eval.VerifyUser(f.ctx, f.req(acct, "", ""), policy.S3ListAllMyBuckets)).To(Succeed(), "allowed")
		})
	})

	Describe("tenants", func() {
		It("create buckets in the identity's own tenant only", func() {
			r := f.req(carol(), "t2:cb2", "")
			r.Method = http.MethodPut
			Expect(f.eval.VerifyUser(f.ctx, r, policy.S3CreateBucket)).To(Succeed(), "carol in t2")
			r = f.req(alice(), "t2:ab", "")
			r.Method = http.MethodPut
			Expect(f.eval.VerifyUser(f.ctx, r, policy.S3CreateBucket)).To(MatchError(op.ErrAccessDenied), "alice in t2")
		})

		It("bind a bucket policy's resources and principals as radosgw parses them", func() {
			f.setAttr("t2", "cb", attrIAMPolicy, []byte(doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"),
				"s3:ListBucket", "arn:aws:s3:::cb", ""))))
			Expect(f.list(bob(), "t2:cb")).To(Succeed(), "bob of the default tenant, the resource bound to t2")
			f.setAttr("t2", "cb", attrIAMPolicy, []byte(doc(stmt("Allow", awsPrincipal("arn:aws:iam::t2:user/bob"),
				"s3:ListBucket", "arn:aws:s3:::cb", ""))))
			Expect(f.list(bob(), "t2:cb")).To(MatchError(op.ErrAccessDenied), "t2's bob is another user")
		})
	})

	Describe("a missing object", func() {
		It("is NoSuchKey only to a requester who may list the bucket, before the op mask", func() {
			for _, c := range []struct {
				name string
				id   op.Identity
				b    string
				want error
			}{
				{"bob, who may not list", bob(), "priv", op.ErrAccessDenied},
				{"the owner", alice(), "priv", op.ErrNoSuchKey},
				{"anonymous on a public bucket", anon(), "pub", op.ErrNoSuchKey},
				{"an admin", adminOf(bob()), "priv", op.ErrNoSuchKey},
			} {
				err := f.getObject(c.id, c.b, "nope")
				Expect(err).To(MatchError(c.want), c.name)
				Expect(op.IsBeforeVerify(err)).To(BeTrue(), "%s: marked", c.name)
			}
		})

		It("evaluates ListBucket with the key as s3:prefix", func() {
			f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:ListBucket",
				"arn:aws:s3:::priv", `{"StringLike": {"s3:prefix": "public/*"}}`)))
			Expect(f.getObject(bob(), "priv", "public/x")).To(MatchError(op.ErrNoSuchKey), "under public/")
			Expect(f.getObject(bob(), "priv", "private/x")).To(MatchError(op.ErrAccessDenied), "under private/")
		})

		It("evaluates without the listing's keys", func() {
			f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:ListBucket",
				"arn:aws:s3:::priv", `{"NumericLessThanEquals": {"s3:max-keys": "1000"}}`)))
			Expect(f.list(bob(), "priv")).To(Succeed(), "a listing carries s3:max-keys")
			Expect(f.getObject(bob(), "priv", "nope")).To(MatchError(op.ErrAccessDenied), "the missing-object check does not")
		})
	})

	Describe("requester pays", func() {
		BeforeEach(func() {
			f.setPolicy("payer", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::payer/*", "")))
		})

		It("needs the requester to agree to pay", func() {
			Expect(f.getObject(bob(), "payer", "o")).To(MatchError(op.ErrAccessDenied), "no header")
			Expect(f.getObject(bob(), "payer", "o", "X-Amz-Request-Payer", "Requester")).To(Succeed(), "the header")
			Expect(f.getObject(bob(), "payer", "o", "X-Amz-Request-Payer", "other")).To(MatchError(op.ErrAccessDenied), "another value")
			Expect(f.getObject(bob(), "payer", "o", "X-Amz-Request-Payer", "requester", "X-Amz-Request-Payer", "other")).
				To(MatchError(op.ErrAccessDenied), "the last header decides")
			r := f.req(bob(), "payer", "o")
			r.Query.Set("x-amz-request-payer", "requester")
			Expect(f.eval.VerifyObject(f.ctx, r, policy.S3GetObject, acl.PermRead)).To(Succeed(), "the query parameter")
		})

		It("lets the owner pay and never an anonymous requester", func() {
			Expect(f.getObject(alice(), "payer", "o")).To(Succeed(), "the owner")
			Expect(f.getObject(anon(), "payer", "o", "X-Amz-Request-Payer", "requester")).
				To(MatchError(op.ErrAccessDenied), "anonymous, despite AllUsers READ")
		})
	})

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			BeforeEach(func() {
				f = newFixture(rel)
			})

			Describe("a suspended bucket", func() {
				It("refuses everyone but an admin, before the op mask", func() {
					err := f.getObject(alice(), "susp", "o")
					Expect(err).To(MatchError(op.ErrUserSuspended), "the owner")
					Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked")
					Expect(f.list(alice(), "susp")).To(MatchError(op.ErrUserSuspended), "listing")

					system := adminOf(alice())
					system.System = true
					Expect(f.getObject(system, "susp", "o")).To(Succeed(), "a system user")
					Expect(f.getObject(adminOf(alice()), "susp", "o")).To(Succeed(), "an admin user")
				})

				It("is refused unmarked as a copy source", func() {
					err := f.eval.VerifyBucketIn(f.ctx, f.req(alice(), "priv", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "susp"), meta.ObjKey{Name: "o"})
					Expect(err).To(MatchError(op.ErrUserSuspended))
					Expect(op.IsBeforeVerify(err)).To(BeFalse())
					src := f.record("", "susp")
					err = f.eval.VerifyObjectIn(f.ctx, f.req(alice(), "priv", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "o"))
					Expect(err).To(MatchError(op.ErrUserSuspended), "object form")
					Expect(op.IsBeforeVerify(err)).To(BeFalse(), "object form")
				})
			})

			Describe("a stored bucket policy that does not parse", func() {
				BeforeEach(func() {
					f.setPolicy("priv", "{not json")
				})

				It("refuses everyone but an admin on the bucket, before the op mask", func() {
					for _, err := range []error{f.list(alice(), "priv"), f.getObject(alice(), "priv", "o")} {
						Expect(err).To(MatchError(op.ErrAccessDenied))
						Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked: %v", err)
					}
					Expect(f.list(adminOf(alice()), "priv")).To(Succeed(), "an admin lists")
					Expect(f.getObject(adminOf(alice()), "priv", "o")).To(Succeed(), "and reads")
				})

				It("refuses ahead of the suspended check", func() {
					f.setPolicy("susp", "{not json")
					Expect(f.getObject(alice(), "susp", "o")).To(MatchError(op.ErrAccessDenied))
				})

				It("refuses every requester of a copy from it, an admin included", func() {
					src := f.record("", "priv")
					for _, err := range []error{
						f.eval.VerifyBucketIn(f.ctx, f.req(adminOf(alice()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
							src, meta.ObjKey{Name: "o"}),
						f.eval.VerifyObjectIn(f.ctx, f.req(adminOf(alice()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
							src, f.state(src, "o")),
					} {
						Expect(err).To(MatchError(op.ErrAccessDenied))
						Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked: %v", err)
					}
				})
			})

			Describe("a stored public-access block that does not decode", func() {
				BeforeEach(func() {
					f.setAttr("", "pub", attrPublicAccess, []byte{0xff})
				})

				It("refuses everyone but an admin on the bucket, before the op mask", func() {
					err := f.list(alice(), "pub")
					Expect(err).To(MatchError(op.ErrAccessDenied))
					Expect(op.IsBeforeVerify(err)).To(BeTrue(), "marked")
					Expect(f.list(adminOf(alice()), "pub")).To(Succeed(), "an admin")
				})

				It("refuses a copy from it unmarked, an admin included", func() {
					err := f.eval.VerifyBucketIn(f.ctx, f.req(adminOf(alice()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "pub"), meta.ObjKey{Name: "x"})
					Expect(err).To(MatchError(op.ErrAccessDenied))
					Expect(op.IsBeforeVerify(err)).To(BeFalse())
				})
			})

			Describe("x-amz-expected-bucket-owner", func() {
				It("refuses a request whose bucket another owner holds", func() {
					Expect(f.getObject(alice(), "priv", "o", "X-Amz-Expected-Bucket-Owner", "bob")).
						To(MatchError(op.ErrAccessDenied), "naming bob")
					Expect(f.getObject(alice(), "priv", "o", "X-Amz-Expected-Bucket-Owner", "alice")).To(Succeed(), "naming alice")
					Expect(f.list(alice(), "priv", "X-Amz-Expected-Bucket-Owner", "bob")).To(MatchError(op.ErrAccessDenied), "listing")
					Expect(f.getObject(alice(), "priv", "o", "X-Amz-Expected-Bucket-Owner", "alice", "X-Amz-Expected-Bucket-Owner", "bob")).
						To(MatchError(op.ErrAccessDenied), "the last header names bob")
					Expect(f.getObject(alice(), "priv", "o", "X-Amz-Expected-Bucket-Owner", "bob", "X-Amz-Expected-Bucket-Owner", "alice")).
						To(Succeed(), "the last header names alice")
					root := accountIdentity(ownAcct, "root", meta.IdentityRoot)
					Expect(f.getObject(root, "acctbkt", "o", "X-Amz-Expected-Bucket-Owner", ownAcct)).To(Succeed(), "an account id")
				})
			})

			Describe("RestrictPublicBuckets", func() {
				BeforeEach(func() {
					f.setAttr("", "polbkt", attrPublicAccess, pabAttr(acl.PublicAccessBlock{RestrictPublicBuckets: true}))
				})

				It("refuses all but the owner when the policy is public", func() {
					f.setPolicy("polbkt", doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::polbkt/*", "")))
					Expect(f.getObject(bob(), "polbkt", "o")).To(MatchError(op.ErrAccessDenied), "bob")
					Expect(f.getObject(alice(), "polbkt", "o")).To(Succeed(), "the owner")
				})

				It("judges the policy public as Tentacle does", func() {
					f.setPolicy("polbkt", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::polbkt/*", "")))
					Expect(f.getObject(bob(), "polbkt", "o")).To(Succeed(), "a grant to bob alone is not public")
				})
			})

			Describe("members of IAM groups", func() {
				var gina, hugo op.Identity
				var logs *bytes.Buffer

				BeforeEach(func() {
					gina, hugo = userIdentity("", "gina"), userIdentity("", "hugo")
					gina.User.GroupIDs = []string{"g1"}
					hugo.User.GroupIDs = []string{"g1"}
					f.bucket("gb", userOwner("", "gina"), aclAttr(ownerACL(userOwner("", "gina"))))
					f.object("gb", "o", aclAttr(ownerACL(userOwner("", "gina"))), nil)

					logs = &bytes.Buffer{}
					prev := slog.Default()
					slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
					DeferCleanup(func() { slog.SetDefault(prev) })
				})

				It("are refused whatever the ACLs and their own policies allow", func() {
					gb := f.record("", "gb")
					errs := []error{
						f.eval.VerifyUser(f.ctx, f.req(gina, "", ""), policy.S3ListAllMyBuckets),
						f.list(gina, "gb"),
						f.getObject(gina, "gb", "o"),
						f.getObject(gina, "pub", "x"),
						f.eval.VerifyBucketIn(f.ctx, f.req(gina, "gb", "c"), policy.S3GetObject, acl.PermRead, gb, meta.ObjKey{Name: "o"}),
						f.eval.VerifyObjectIn(f.ctx, f.req(gina, "gb", "c"), policy.S3GetObject, acl.PermRead, gb, f.state(gb, "o")),
						f.getObject(withUserPolicy(gina, doc(stmt("Allow", "", "s3:*", "*", ""))), "gb", "o"),
					}
					for i, err := range errs {
						Expect(err).To(MatchError(op.ErrAccessDenied), "check %d", i)
						Expect(op.IsBeforeVerify(err)).To(BeFalse(), "check %d: an admin's override applies", i)
					}
				})

				It("are refused a missing key's NoSuchKey, before the op mask", func() {
					err := f.getObject(gina, "gb", "nope")
					Expect(err).To(MatchError(op.ErrAccessDenied))
					Expect(op.IsBeforeVerify(err)).To(BeTrue())
					Expect(f.getObject(adminOf(gina), "gb", "nope")).To(MatchError(op.ErrNoSuchKey), "an admin")
				})

				It("meet a suspended bucket's refusal first", func() {
					f.update("", "gb", func(i *meta.BucketInfo) { i.Flags |= meta.BucketSuspended })
					Expect(f.getObject(gina, "gb", "o")).To(MatchError(op.ErrUserSuspended))
				})

				It("are logged once per user", func() {
					for range 5 {
						Expect(f.list(gina, "gb")).To(MatchError(op.ErrAccessDenied))
					}
					Expect(strings.Count(logs.String(), `"user":"gina"`)).To(Equal(1), "gina, in %s", logs)
					Expect(f.list(hugo, "gb")).To(MatchError(op.ErrAccessDenied))
					Expect(strings.Count(logs.String(), "refusing an iam group member")).To(Equal(2), "hugo too, in %s", logs)
				})

				It("leave other users alone", func() {
					Expect(f.list(alice(), "pub")).To(Succeed())
				})
			})

			Describe("explicit resources", func() {
				It("take the owner from the explicit bucket across accounts", func() {
					f.bucket("srcb", meta.AccountOwner(otherAcct), aclAttr(ownerACL(meta.AccountOwner(otherAcct))))
					src := f.record("", "srcb")
					acct := withUserPolicy(accountIdentity(ownAcct, "acct", meta.IdentityRGW),
						doc(stmt("Allow", "", "s3:GetObject", "*", "")))
					copyAs := func(id op.Identity) error {
						return f.eval.VerifyBucketIn(f.ctx, f.req(id, "acctbkt", "c"), policy.S3GetObject, acl.PermRead,
							f.record("", "srcb"), meta.ObjKey{Name: "o"})
					}
					Expect(copyAs(acct)).To(MatchError(op.ErrAccessDenied), "A's user, without B's grant")
					Expect(copyAs(accountIdentity(ownAcct, "root", meta.IdentityRoot))).
						To(MatchError(op.ErrAccessDenied), "A's root, without B's grant")
					f.setPolicy("srcb", doc(stmt("Allow", awsPrincipal("arn:aws:iam::"+ownAcct+":user/acct"),
						"s3:GetObject", "arn:aws:s3:::srcb/*", "")))
					Expect(copyAs(acct)).To(Succeed(), "with B's grant")
					Expect(src.Info.Owner.String()).To(Equal(otherAcct))
				})

				It("parse the explicit bucket's policy with its own tenant", func() {
					f.tenantBucket("t1", "src", userOwner("t1", "dave"), aclAttr(publicRead(userOwner("t1", "dave"))))
					f.setAttr("t1", "src", attrIAMPolicy, []byte(doc(stmt("Deny", `"*"`, "s3:GetObject", "arn:aws:s3:::src/private/*", ""))))
					copyKey := func(key string) error {
						return f.eval.VerifyBucketIn(f.ctx, f.req(carol(), "t2:cb", "c"), policy.S3GetObject, acl.PermRead,
							f.record("t1", "src"), meta.ObjKey{Name: key})
					}
					Expect(copyKey("private/x")).To(MatchError(op.ErrAccessDenied), "the owner's Deny")
					Expect(copyKey("public/x")).To(Succeed(), "the public grant")
				})

				It("apply either bucket's IgnorePublicAcls", func() {
					f.setAttr("", "pub", attrPublicAccess, pabAttr(acl.PublicAccessBlock{IgnorePublicACLs: true}))
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "pub"), meta.ObjKey{Name: "x"})).To(MatchError(op.ErrAccessDenied), "the source's block")

					f.bucket("pub2", userOwner("", "alice"), aclAttr(publicRead(userOwner("", "alice"))))
					f.setAttr("", "bobbkt", attrPublicAccess, pabAttr(acl.PublicAccessBlock{IgnorePublicACLs: true}))
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "pub2"), meta.ObjKey{Name: "x"})).To(MatchError(op.ErrAccessDenied), "the destination's block")
				})

				It("apply each RestrictPublicBuckets against its own bucket's owner", func() {
					f.setPolicy("polbkt", doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::polbkt/*", "")))
					copyAs := func(id op.Identity, dst string) error {
						return f.eval.VerifyBucketIn(f.ctx, f.req(id, dst, "c"), policy.S3GetObject, acl.PermRead,
							f.record("", "polbkt"), meta.ObjKey{Name: "o"})
					}
					Expect(copyAs(bob(), "bobbkt")).To(Succeed(), "no block")
					f.setAttr("", "polbkt", attrPublicAccess, pabAttr(acl.PublicAccessBlock{RestrictPublicBuckets: true}))
					Expect(copyAs(bob(), "bobbkt")).To(MatchError(op.ErrAccessDenied), "the source's block, bob not its owner")
					Expect(copyAs(alice(), "bobbkt")).To(Succeed(), "the source's owner")
				})

				It("give an object without an ACL the explicit bucket owner's default", func() {
					f.object("priv", "noacl", nil, nil)
					src := f.record("", "priv")
					Expect(f.eval.VerifyObjectIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "noacl"))).To(MatchError(op.ErrAccessDenied), "bob")
					Expect(f.eval.VerifyObjectIn(f.ctx, f.req(alice(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "noacl"))).To(Succeed(), "alice, the source's owner")
				})

				It("compare x-amz-expected-bucket-owner with the request's bucket", func() {
					copyWith := func(owner string) error {
						return f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "bobbkt", "c", "X-Amz-Expected-Bucket-Owner", owner),
							policy.S3GetObject, acl.PermRead, f.record("", "pub"), meta.ObjKey{Name: "x"})
					}
					Expect(copyWith("bob")).To(Succeed(), "naming the destination's owner")
					Expect(copyWith("alice")).To(MatchError(op.ErrAccessDenied), "naming the source's owner")
				})

				It("refuse unmarked when the request's bucket's block does not decode", func() {
					f.setAttr("", "bobbkt", attrPublicAccess, []byte{0xff})
					pub := f.record("", "pub")
					for _, err := range []error{
						f.eval.VerifyBucketIn(f.ctx, f.req(adminOf(bob()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
							pub, meta.ObjKey{Name: "x"}),
						f.eval.VerifyObjectIn(f.ctx, f.req(adminOf(bob()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
							pub, f.state(pub, "x")),
					} {
						Expect(err).To(MatchError(op.ErrAccessDenied))
						Expect(op.IsBeforeVerify(err)).To(BeFalse(), "unmarked: %v", err)
					}
				})

				It("let an admin copy from a suspended bucket", func() {
					src := f.record("", "susp")
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(adminOf(alice()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, meta.ObjKey{Name: "o"})).To(Succeed(), "bucket form")
					Expect(f.eval.VerifyObjectIn(f.ctx, f.req(adminOf(alice()), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "o"))).To(Succeed(), "object form")
				})

				It("allow a copy of a source object whose ACL grants the requester READ", func() {
					src := f.record("", "priv")
					Expect(f.eval.VerifyObjectIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "shared"))).To(Succeed(), "shared grants bob READ")
					Expect(f.eval.VerifyObjectIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						src, f.state(src, "o"))).To(MatchError(op.ErrAccessDenied), "o does not")
				})

				It("take requester pays from the request's bucket", func() {
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "payer", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "pub"), meta.ObjKey{Name: "x"})).To(MatchError(op.ErrAccessDenied), "into a bucket bob must pay for")
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
						f.record("", "payer"), meta.ObjKey{Name: "o"})).To(Succeed(), "from one")
				})

				It("check DeleteObjects' keys against the bucket's ACL", func() {
					priv := f.record("", "priv")
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(alice(), "priv", ""), policy.S3DeleteObject, acl.PermWrite,
						priv, meta.ObjKey{Name: "a"})).To(Succeed(), "alice")
					Expect(f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "priv", ""), policy.S3DeleteObject, acl.PermWrite,
						priv, meta.ObjKey{Name: "a"})).To(MatchError(op.ErrAccessDenied), "bob")
				})

				It("use the bucket ARN for an empty key", func() {
					listIn := func() error {
						return f.eval.VerifyBucketIn(f.ctx, f.req(bob(), "bobbkt", ""), policy.S3ListBucket, acl.PermRead,
							f.record("", "priv"), meta.ObjKey{})
					}
					f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:ListBucket", "arn:aws:s3:::priv/*", "")))
					Expect(listIn()).To(MatchError(op.ErrAccessDenied), "a policy on the objects")
					f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:ListBucket", "arn:aws:s3:::priv", "")))
					Expect(listIn()).To(Succeed(), "a policy on the bucket")
				})

				Describe("a missing source object", func() {
					var src *op.BucketRecord

					BeforeEach(func() {
						src = f.record("", "priv")
					})

					copyMissing := func(id op.Identity) error {
						return f.eval.VerifyObjectIn(f.ctx, f.req(id, "bobbkt", "c"), policy.S3GetObject, acl.PermRead,
							src, f.state(src, "public/nope"))
					}

					It("is NoSuchKey only to a requester who may list the source, unmarked", func() {
						f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::priv/*", "")))
						src = f.record("", "priv")
						err := copyMissing(bob())
						Expect(err).To(MatchError(op.ErrAccessDenied), "GetObject without ListBucket")
						Expect(op.IsBeforeVerify(err)).To(BeFalse())

						f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:ListBucket",
							"arn:aws:s3:::priv", `{"StringLike": {"s3:prefix": "public/*"}}`)))
						src = f.record("", "priv")
						Expect(copyMissing(bob())).To(MatchError(op.ErrNoSuchKey), "ListBucket on the prefix")
					})

					It("is NoSuchKey to an admin, unmarked", func() {
						err := copyMissing(adminOf(bob()))
						Expect(err).To(MatchError(op.ErrNoSuchKey))
						Expect(op.IsBeforeVerify(err)).To(BeFalse())
					})

					It("is UserSuspended first in a suspended bucket", func() {
						src = f.record("", "susp")
						Expect(copyMissing(alice())).To(MatchError(op.ErrUserSuspended))
					})
				})
			})
		})
	}

	Describe("tag conditions", func() {
		bobOn := func(action, resource, cond string) string {
			return doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), action, resource, cond))
		}

		BeforeEach(func() {
			alice := userOwner("", "alice")
			f.bucket("tagbkt", alice, aclAttr(ownerACL(alice)))
			f.setAttr("", "tagbkt", tags.Attr, encodeTags("team", "a"))
			f.object("tagbkt", "o", aclAttr(ownerACL(alice)), encodeTags("team", "b", "env", "prod"))
			f.bucket("plain", alice, aclAttr(ownerACL(alice)))
		})

		It("read an object's tags as s3:ExistingObjectTag", func() {
			f.setPolicy("priv", bobOn("s3:GetObject", "arn:aws:s3:::priv/*", `{"StringEquals": {"s3:ExistingObjectTag/env": "prod"}}`))
			Expect(f.getObject(bob(), "priv", "tagged")).To(Succeed(), "tagged")
			Expect(f.getObject(bob(), "priv", "o")).To(MatchError(op.ErrAccessDenied), "untagged")
		})

		It("read a bucket's tags as s3:ResourceTag", func() {
			cond := `{"StringEquals": {"s3:ResourceTag/team": "a"}}`
			f.setPolicy("tagbkt", bobOn("s3:ListBucket", "arn:aws:s3:::tagbkt", cond))
			f.setPolicy("plain", bobOn("s3:ListBucket", "arn:aws:s3:::plain", cond))
			Expect(f.list(bob(), "tagbkt")).To(Succeed(), "tagged bucket")
			Expect(f.list(bob(), "plain")).To(MatchError(op.ErrAccessDenied), "untagged bucket")
		})

		It("read a PUT's tags as s3:RequestObjectTag", func() {
			f.setPolicy("priv", bobOn("s3:PutObject", "arn:aws:s3:::priv/*", `{"StringEquals": {"s3:RequestObjectTag/env": "prod"}}`))
			Expect(f.putObject(bob(), "priv", "x", "X-Amz-Tagging", "env=prod")).To(Succeed(), "with the tag")
			Expect(f.putObject(bob(), "priv", "x")).To(MatchError(op.ErrAccessDenied), "without")
		})

		It("decide PutObjectTagging's s3:ResourceTag by the bucket and s3:ExistingObjectTag by the object", func() {
			put := func() error {
				r := f.req(bob(), "tagbkt", "o")
				r.Method = http.MethodPut
				return f.eval.VerifyObject(f.ctx, r, policy.S3PutObjectTagging, acl.PermWrite)
			}
			f.setPolicy("tagbkt", bobOn("s3:PutObjectTagging", "arn:aws:s3:::tagbkt/*", `{"StringEquals": {"s3:ResourceTag/team": "a"}}`))
			Expect(put()).To(Succeed(), "the bucket's team")
			f.setPolicy("tagbkt", bobOn("s3:PutObjectTagging", "arn:aws:s3:::tagbkt/*", `{"StringEquals": {"s3:ResourceTag/team": "b"}}`))
			Expect(put()).To(MatchError(op.ErrAccessDenied), "the object's team")
			f.setPolicy("tagbkt", bobOn("s3:PutObjectTagging", "arn:aws:s3:::tagbkt/*", `{"StringEquals": {"s3:ExistingObjectTag/env": "prod"}}`))
			Expect(put()).To(Succeed(), "the object's env")
		})

		Describe("DeleteObject", func() {
			var objects *opfakes.FakeObjectStore

			BeforeEach(func() {
				objects = &opfakes.FakeObjectStore{}
				objects.StatObjectStub = f.store.StatObject
				f.env.Objects = objects
				f.object("priv", "prot", aclAttr(ownerACL(userOwner("", "alice"))), encodeTags("protected", "true"))
			})

			del := func(key string) error {
				r := f.req(alice(), "priv", key)
				r.Method, r.ObjState = http.MethodDelete, nil
				return f.eval.VerifyBucket(f.ctx, r, policy.S3DeleteObject, acl.PermWrite)
			}

			It("reads the tags of the object it deletes", func() {
				f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:DeleteObject", "arn:aws:s3:::priv/*",
					`{"StringEquals": {"s3:ExistingObjectTag/protected": "true"}}`)))
				Expect(del("prot")).To(MatchError(op.ErrAccessDenied), "a protected object")
				Expect(del("o")).To(Succeed(), "an untagged one")
				Expect(objects.StatObjectCallCount()).To(Equal(2))
			})

			It("reads no object when no policy names a tag", func() {
				Expect(del("prot")).To(Succeed())
				Expect(objects.StatObjectCallCount()).To(BeZero())
			})

			It("adds no tags in DeleteObjects' per-key check", func() {
				f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:DeleteObject", "arn:aws:s3:::priv/*",
					`{"StringEquals": {"s3:ExistingObjectTag/protected": "true"}}`)))
				Expect(f.eval.VerifyBucketIn(f.ctx, f.req(alice(), "priv", ""), policy.S3DeleteObject, acl.PermWrite,
					f.record("", "priv"), meta.ObjKey{Name: "prot"})).To(Succeed())
				Expect(objects.StatObjectCallCount()).To(BeZero())
			})
		})

		It("give PutObject the bucket's tags and never the object's", func() {
			f.setPolicy("tagbkt", bobOn("s3:PutObject", "arn:aws:s3:::tagbkt/*", `{"StringEquals": {"s3:ExistingObjectTag/env": "prod"}}`))
			Expect(f.putObject(bob(), "tagbkt", "o")).To(MatchError(op.ErrAccessDenied), "s3:ExistingObjectTag")
			f.setPolicy("tagbkt", bobOn("s3:PutObject", "arn:aws:s3:::tagbkt/*", `{"StringEquals": {"s3:ResourceTag/team": "a"}}`))
			Expect(f.putObject(bob(), "tagbkt", "o")).To(Succeed(), "s3:ResourceTag")
		})

		It("decide a copy source by the source object's tags", func() {
			f.setPolicy("priv", bobOn("s3:GetObject", "arn:aws:s3:::priv/*", `{"StringEquals": {"s3:ExistingObjectTag/env": "prod"}}`))
			copyKey := func(key string) error {
				r := f.req(bob(), "bobbkt", "c", "X-Amz-Copy-Source", "priv/"+key)
				r.Method = http.MethodPut
				return f.eval.VerifyBucketIn(f.ctx, r, policy.S3GetObject, acl.PermRead, f.record("", "priv"), meta.ObjKey{Name: key})
			}
			Expect(copyKey("tagged")).To(Succeed(), "tagged")
			Expect(copyKey("o")).To(MatchError(op.ErrAccessDenied), "untagged")
		})

		Describe("that cannot be read", func() {
			var objects *opfakes.FakeObjectStore

			BeforeEach(func() {
				objects = &opfakes.FakeObjectStore{}
				objects.StatObjectStub = f.store.StatObject
				f.env.Objects = objects
				f.object("priv", "badtags", aclAttr(ownerACL(userOwner("", "alice"))), []byte{0xff, '&'})
				f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:DeleteObject", "arn:aws:s3:::priv/*",
					`{"StringEquals": {"s3:ExistingObjectTag/protected": "true"}}`)))
			})

			del := func(key string) error {
				r := f.req(alice(), "priv", key)
				r.Method, r.ObjState = http.MethodDelete, nil
				return f.eval.VerifyBucket(f.ctx, r, policy.S3DeleteObject, acl.PermWrite)
			}

			It("refuse the request when a policy names a tag and the read fails", func() {
				objects.StatObjectReturns(nil, errors.New("rados: input/output error"))
				err := del("o")
				Expect(err).To(MatchError(op.ErrInternalError))
				Expect(op.IsBeforeVerify(err)).To(BeFalse())
			})

			It("refuse the request when a policy names a tag and the tag set does not decode", func() {
				Expect(del("badtags")).To(MatchError(op.ErrInternalError), "a delete")
				Expect(f.getObject(alice(), "priv", "badtags")).To(MatchError(op.ErrInternalError),
					"a GET: rgw_check_policy_condition judges the need over every statement")
				f.setPolicy("priv", doc(stmt("Allow", `"*"`, "s3:ListBucket", "arn:aws:s3:::priv", "")))
				Expect(f.getObject(alice(), "priv", "badtags")).To(Succeed(), "no policy names a tag")
			})

			It("never match the stat's own error", func() {
				objects.StatObjectReturns(nil, fmt.Errorf("head: %w", op.ErrAccessDenied))
				err := del("o")
				Expect(err).To(MatchError(op.ErrInternalError))
				Expect(errors.Is(err, op.ErrAccessDenied)).To(BeFalse(), "got %v", err)
			})

			It("leave the ACL ops alone when no policy names a tag", func() {
				alice := userOwner("", "alice")
				f.setAttr("", "plain", tags.Attr, []byte{0xff, '&'})
				f.object("plain", "badtags", aclAttr(ownerACL(alice)), []byte{0xff, '&'})
				r := f.req(userIdentity("", "alice"), "plain", "badtags")
				r.Method = http.MethodPut
				Expect(f.eval.VerifyObject(f.ctx, r, policy.S3PutObjectAcl, acl.PermWriteACP)).To(Succeed(), "PutObjectAcl")
				r = f.req(userIdentity("", "alice"), "plain", "")
				r.Method = http.MethodPut
				Expect(f.eval.VerifyBucket(f.ctx, r, policy.S3PutBucketAcl, acl.PermWriteACP)).To(Succeed(), "PutBucketAcl")
			})

			It("refuse the request when the bucket's tag set does not decode", func() {
				f.setAttr("", "plain", tags.Attr, []byte{0xff, '&'})
				f.setPolicy("plain", bobOn("s3:ListBucket", "arn:aws:s3:::plain", `{"StringEquals": {"s3:ResourceTag/team": "a"}}`))
				Expect(f.list(bob(), "plain")).To(MatchError(op.ErrInternalError))
			})

			It("add no keys for a missing object, whether the stat reports it or fails with NoSuchKey", func() {
				Expect(del("nope")).To(Succeed(), "a state that does not exist")
				objects.StatObjectReturns(nil, fmt.Errorf("head: %w", op.ErrNoSuchKey))
				Expect(del("nope")).To(Succeed(), "NoSuchKey")
			})

			It("read nothing when no policy names a tag", func() {
				f.setPolicy("priv", doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::priv/*", "")))
				objects.StatObjectReturns(nil, errors.New("rados: input/output error"))
				Expect(del("o")).To(Succeed())
				Expect(objects.StatObjectCallCount()).To(BeZero())
			})
		})

		It("give PutObjectRetention's governance bypass the object's tags", func() {
			f.object("priv", "held", aclAttr(ownerACL(userOwner("", "alice"))), encodeTags("legal", "hold"))
			f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:BypassGovernanceRetention", "arn:aws:s3:::priv/*",
				`{"StringEquals": {"s3:ExistingObjectTag/legal": "hold"}}`)))
			bypass := func(key string) error {
				r := f.req(alice(), "priv", key)
				r.Method = http.MethodPut
				return f.eval.VerifyObject(f.ctx, r, policy.S3BypassGovernanceRetention, acl.PermFor(policy.S3BypassGovernanceRetention))
			}
			Expect(bypass("held")).To(MatchError(op.ErrAccessDenied), "a held object")
			Expect(bypass("o")).To(Succeed(), "another")
		})
	})

	Describe("tag conditions on Tentacle", func() {
		BeforeEach(func() {
			f = newFixture(denc.Tentacle)
			alice := userOwner("", "alice")
			f.bucket("tagbkt", alice, aclAttr(ownerACL(alice)))
			f.setAttr("", "tagbkt", tags.Attr, encodeTags("team", "a"))
			f.bucket("plain", alice, aclAttr(ownerACL(alice)))
		})

		DescribeTable("give the bucket logging ops the bucket's tags",
			func(action policy.Action, name string) {
				for _, b := range []string{"tagbkt", "plain"} {
					f.setPolicy(b, doc(stmt("Deny", `"*"`, name, "arn:aws:s3:::"+b, `{"StringEquals": {"s3:ResourceTag/team": "a"}}`)))
				}
				check := func(b string) error {
					r := f.req(alice(), b, "")
					r.Method = http.MethodPut
					return f.eval.VerifyBucket(f.ctx, r, action, acl.PermFor(action))
				}
				Expect(check("tagbkt")).To(MatchError(op.ErrAccessDenied), "a tagged bucket")
				Expect(check("plain")).To(Succeed(), "an untagged one")
			},
			Entry("PutBucketLogging, rgw_rest_bucket_logging.cc:197-208", policy.S3PutBucketLogging, "s3:PutBucketLogging"),
			Entry("PostBucketLogging, rgw_rest_bucket_logging.cc:401-406", policy.S3PostBucketLogging, "s3:PostBucketLogging"),
		)

		It("give a replication GET's GetObjectVersionForReplication check the object's tags", func() {
			f.object("priv", "held", aclAttr(ownerACL(userOwner("", "alice"))), encodeTags("legal", "hold"))
			f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:GetObjectVersionForReplication", "arn:aws:s3:::priv/*",
				`{"StringEquals": {"s3:ExistingObjectTag/legal": "hold"}}`)))
			get := func(key string) error {
				return f.eval.VerifyObject(f.ctx, f.req(alice(), "priv", key), policy.S3GetObjectVersionForReplication, acl.PermRead)
			}
			Expect(get("held")).To(MatchError(op.ErrAccessDenied), "a held object")
			Expect(get("o")).To(Succeed(), "another")
		})
	})

	Describe("ListAllMyBuckets and CreateBucket", func() {
		It("let an anonymous requester list but not create", func() {
			Expect(f.eval.VerifyUser(f.ctx, f.req(anon(), "", ""), policy.S3ListAllMyBuckets)).To(Succeed(), "list")
			r := f.req(anon(), "nb", "")
			r.Method = http.MethodPut
			Expect(f.eval.VerifyUser(f.ctx, r, policy.S3CreateBucket)).To(MatchError(op.ErrAccessDenied), "create")
		})
	})

	It("refuses a bucket check on a request that names no bucket", func() {
		Expect(f.eval.VerifyBucket(f.ctx, f.req(alice(), "", ""), policy.S3ListBucket, acl.PermRead)).
			To(MatchError(op.ErrAccessDenied))
	})

	Describe("under op.Run", func() {
		var executed bool

		run := func(r *op.Request, mask uint32) error {
			executed = false
			return op.Run(f.ctx, &writeOp{mask: mask, executed: &executed}, r)
		}

		It("answers a suspended bucket ahead of the op mask", func() {
			ro := alice()
			ro.OpMask = op.OpTypeRead
			r := f.req(ro, "susp", "x")
			r.Method = http.MethodPut
			Expect(run(r, op.OpTypeWrite)).To(MatchError(op.ErrUserSuspended))
		})

		It("refuses an admin whose stored user policy does not decode", func() {
			bad := adminOf(alice())
			bad.Attrs = map[string][]byte{attrUserPolicy: []byte("garbage")}
			r := f.req(bad, "priv", "x")
			r.Method = http.MethodPut
			Expect(run(r, op.OpTypeWrite)).To(MatchError(op.ErrAccessDenied))
			Expect(executed).To(BeFalse())
		})

		It("lets an admin past an ACL's denial", func() {
			r := f.req(adminOf(bob()), "priv", "x")
			r.Method = http.MethodPut
			Expect(run(r, op.OpTypeWrite)).To(Succeed())
			Expect(executed).To(BeTrue())
		})
	})

	Describe("GetObjectAttributes", func() {
		var r *op.Request

		attrs := func(id op.Identity, key string) error {
			r = f.req(id, "priv", key)
			return (&op.GetObjectAttributes{}).VerifyPermission(f.ctx, r)
		}

		Context("on Tentacle", func() {
			BeforeEach(func() {
				f = newFixture(denc.Tentacle)
			})

			It("asks for s3:GetObjectAttributes when an explicit Deny refuses s3:GetObject", func() {
				f.setPolicy("priv", doc(
					stmt("Deny", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::priv/*", ""),
					stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObjectAttributes", "arn:aws:s3:::priv/*", "")))
				err := f.getObject(bob(), "priv", "shared")
				Expect(err).To(MatchError(op.ErrAccessDenied), "the first arm")
				Expect(op.IsBeforeVerify(err)).To(BeFalse(), "the first arm, unmarked")
				Expect(attrs(bob(), "shared")).To(Succeed(), "the second arm")
			})

			It("refuses under a Deny of s3:* or s3:Get*", func() {
				for _, action := range []string{"s3:*", "s3:Get*"} {
					f.setPolicy("priv", doc(stmt("Deny", `"*"`, action, "arn:aws:s3:::priv/*", "")))
					Expect(attrs(bob(), "shared")).To(MatchError(op.ErrAccessDenied), action)
				}
			})

			It("answers a missing object from the first arm, marked", func() {
				err := attrs(bob(), "nope")
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.IsBeforeVerify(err)).To(BeTrue())
			})

			It("evaluates existing-object-tag conditions on the second arm", func() {
				f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObjectAttributes",
					"arn:aws:s3:::priv/*", `{"StringEquals": {"s3:ExistingObjectTag/env": "prod"}}`)))
				Expect(attrs(bob(), "tagged")).To(Succeed(), "tagged")
				Expect(attrs(bob(), "o")).To(MatchError(op.ErrAccessDenied), "untagged")
			})
		})

		It("refuses on Squid under a Deny of s3:*, whatever the object ACL grants", func() {
			f.setPolicy("priv", doc(stmt("Deny", `"*"`, "s3:*", "arn:aws:s3:::priv/*", "")))
			Expect(attrs(bob(), "shared")).To(MatchError(op.ErrAccessDenied))
		})
	})
})

// writeOp is an op that checks s3:PutObject on its bucket, as RGWPutObj does.
type writeOp struct {
	mask     uint32
	executed *bool
}

func (*writeOp) Name() string                                 { return "put_obj" }
func (*writeOp) Action() policy.Action                        { return policy.S3PutObject }
func (o *writeOp) OpMask() uint32                             { return o.mask }
func (*writeOp) Init(context.Context, *op.Request) error      { return nil }
func (o *writeOp) Execute(context.Context, *op.Request) error { *o.executed = true; return nil }
func (*writeOp) Complete(context.Context, *op.Request)        {}

func (*writeOp) VerifyPermission(ctx context.Context, r *op.Request) error {
	return op.VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermWrite)
}
