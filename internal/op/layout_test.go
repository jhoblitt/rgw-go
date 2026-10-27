package op_test

import (
	"context"
	"fmt"
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
)

// currentVersionHeads is an object store whose StatObject resolves a key
// named without an instance to its current version, v2, as the driver's
// follows a versioned key's OLH.
type currentVersionHeads struct{ op.ObjectStore }

func (c currentVersionHeads) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	if key.Instance == "" {
		key.Instance = "v2"
	}
	return c.ObjectStore.StatObject(ctx, rec, key)
}

var _ = Describe("GetObjectLayout", func() {
	var (
		store  *memstore.Store
		env    *op.Env
		layout *opfakes.FakeLayoutStore
		doc    *opfakes.FakeLayoutDocument
		// alice owns plain and holds no cap; carol holds admin=read and
		// neither owns plain nor holds a grant on it.
		alice, carol op.Identity
	)
	identityOf := func(rec *op.UserRecord) op.Identity {
		return op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask, Caps: rec.Info.Caps}
	}
	withCaps := func(caps meta.Caps) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "dave"}
		u.Caps = caps
		return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}
	}
	admin := func() op.Identity {
		id := withCaps(nil)
		id.Admin = true
		return id
	}
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid})
		alice = identityOf(store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll}))
		carol = identityOf(store.AddUser(meta.UserInfo{
			UserID: meta.UserID{ID: "carol"}, DisplayName: "Carol", OpMask: op.OpTypeAll, Caps: meta.Caps{"admin": meta.CapRead},
		}))
		doc = &opfakes.FakeLayoutDocument{}
		layout = &opfakes.FakeLayoutStore{}
		layout.ObjectLayoutReturns(doc, nil)
		env = &op.Env{
			Zone: store, Users: store, Buckets: store, Objects: store, Layout: layout,
			Authz: authz.New(authz.DefaultConfig(denc.Squid)), Metrics: op.NopMetrics{},
		}
		aliceACL := encodedPolicy(acl.DefaultPolicy(alice.Owner, "Alice"))
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "plain", Owner: alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: aliceACL},
		})
		Expect(err).NotTo(HaveOccurred())
		for _, key := range []meta.ObjKey{{Name: "k"}, {Name: "v", Instance: "v1"}, {Name: "v", Instance: "v2"}} {
			_, err := store.PutObject(ctx, rec, key, strings.NewReader(key.Instance), op.PutParams{
				Size: int64(len(key.Instance)), Attrs: map[string][]byte{meta.AttrACL: aliceACL},
			})
			Expect(err).NotTo(HaveOccurred())
		}
	})
	request := func(id op.Identity, key meta.ObjKey, hdr ...string) *op.Request {
		r := &op.Request{
			Method: http.MethodGet, Bucket: "plain", Object: key, Identity: id, Env: env,
			Header: http.Header{}, Query: url.Values{"layout": {""}}, ContentLength: -1,
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return r
	}
	run := func(ctx context.Context, r *op.Request) (*op.GetObjectLayout, error) {
		o := &op.GetObjectLayout{}
		return o, op.Run(ctx, o, r)
	}
	setBucket := func(ctx context.Context, f func(*op.BucketRecord)) {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		f(rec)
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	laidOut := func() *op.ObjectState {
		GinkgoHelper()
		Expect(layout.ObjectLayoutCallCount()).To(Equal(1), "ObjectLayout calls")
		_, st := layout.ObjectLayoutArgsForCall(0)
		return st
	}

	It("is get_obj_layout, needs READ in the op mask and names no action", func() {
		o := &op.GetObjectLayout{}
		Expect(o.Name()).To(Equal("get_obj_layout"))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		Expect(o.Action()).To(Equal(policy.ActionNone))
	})

	It("lays out the head Init read for a holder of admin=read who neither owns the bucket nor holds a grant", func(ctx SpecContext) {
		r := request(carol, meta.ObjKey{Name: "k"})
		o, err := run(ctx, r)
		Expect(err).NotTo(HaveOccurred())
		st := laidOut()
		Expect(st).To(BeIdenticalTo(r.ObjState), "the state Init read")
		Expect(st.Key).To(Equal(meta.ObjKey{Name: "k"}))
		Expect(st.Exists).To(BeTrue())
		Expect(o.Document).To(BeIdenticalTo(doc))
	})

	DescribeTable("refuses AccessDenied without admin=read, laying nothing out, as check_cap's -EPERM",
		func(ctx SpecContext, id func() op.Identity) {
			_, err := run(ctx, request(id(), meta.ObjKey{Name: "k"}))
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(op.IsBeforeVerify(err)).To(BeFalse(), "verify_permission's refusal, which an admin's override passes")
			Expect(layout.ObjectLayoutCallCount()).To(BeZero())
		},
		Entry("the bucket's owner", func() op.Identity { return alice }),
		Entry("admin=write alone", func() op.Identity { return withCaps(meta.Caps{"admin": meta.CapWrite}) }),
		Entry("every other cap type at *", func() op.Identity {
			return withCaps(meta.Caps{
				"users": meta.CapAll, "buckets": meta.CapAll, "metadata": meta.CapAll, "usage": meta.CapAll,
				"zone": meta.CapAll, "info": meta.CapAll, "user-info-without-keys": meta.CapAll,
			})
		}),
		Entry("an anonymous requester", op.Anonymous),
	)

	It("lets an admin identity through without the cap, as rgw_process_authenticated overrides verify_permission", func(ctx SpecContext) {
		_, err := run(ctx, request(admin(), meta.ObjKey{Name: "k"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(laidOut().Key).To(Equal(meta.ObjKey{Name: "k"}))
	})

	It("refuses an admin without READ in its op mask, which verify_op_mask decides before any override", func(ctx SpecContext) {
		id := admin()
		id.Caps = meta.Caps{"admin": meta.CapRead}
		id.OpMask = op.OpTypeWrite
		_, err := run(ctx, request(id, meta.ObjKey{Name: "k"}))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(layout.ObjectLayoutCallCount()).To(BeZero())
	})

	DescribeTable("answers a missing object by read_obj_policy's rule, before the cap check",
		func(ctx SpecContext, id func() op.Identity, want error) {
			_, err := run(ctx, request(id(), meta.ObjKey{Name: "nope"}))
			Expect(err).To(MatchError(want))
			Expect(op.IsBeforeVerify(err)).To(BeTrue(), "read_permissions' refusal, which no override passes")
			Expect(layout.ObjectLayoutCallCount()).To(BeZero())
		},
		Entry("NoSuchKey to the bucket's owner, who may list it and holds no cap", func() op.Identity { return alice }, op.ErrNoSuchKey),
		Entry("AccessDenied to a holder of the cap who may not list the bucket", func() op.Identity { return carol }, op.ErrAccessDenied),
		Entry("NoSuchKey to an admin", admin, op.ErrNoSuchKey),
	)

	// Each refusal below radosgw makes before verify_permission, so neither
	// the cap nor an admin's override passes it.
	DescribeTable("makes init_permissions' and read_permissions' refusals before the cap check",
		func(ctx SpecContext, change func(context.Context) op.Identity, want error) {
			id := change(ctx)
			_, err := run(ctx, request(id, meta.ObjKey{Name: "k"}))
			Expect(err).To(MatchError(want))
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			Expect(layout.ObjectLayoutCallCount()).To(BeZero())
		},
		Entry("a suspended bucket", func(ctx context.Context) op.Identity {
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Info.Flags |= meta.BucketSuspended })
			return carol
		}, op.ErrUserSuspended),
		Entry("a bucket policy that does not parse", func(ctx context.Context) op.Identity {
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Attrs[op.AttrIAMPolicy] = []byte("{") })
			return carol
		}, op.ErrAccessDenied),
		Entry("a public-access block that does not decode", func(ctx context.Context) op.Identity {
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Attrs[op.AttrPublicAccess] = []byte{0xff} })
			return carol
		}, op.ErrAccessDenied),
		Entry("a bucket ACL that does not decode", func(ctx context.Context) op.Identity {
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Attrs[meta.AttrACL] = []byte{0xff} })
			return carol
		}, op.ErrUnknown),
		Entry("an object ACL that does not decode", func(ctx context.Context) op.Identity {
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader(""), op.PutParams{
				Attrs: map[string][]byte{meta.AttrACL: {0xff}},
			})
			Expect(err).NotTo(HaveOccurred())
			return carol
		}, op.ErrUnknown),
		Entry("an identity policy that does not decode", func(context.Context) op.Identity {
			id := carol
			id.Attrs = map[string][]byte{meta.AttrPrefix + "user-policy": []byte("garbage")}
			return id
		}, op.ErrAccessDenied),
	)

	It("serves a holder of the cap who belongs to an IAM group, and logs no refusal of the group member", func(ctx SpecContext) {
		logs := captureLogs()
		u := *carol.User
		u.GroupIDs = []string{"g1"}
		member := carol
		member.User = &u
		_, err := run(ctx, request(member, meta.ObjKey{Name: "k"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(laidOut().Key).To(Equal(meta.ObjKey{Name: "k"}))
		Expect(logs.String()).NotTo(ContainSubstring("refusing an iam group member"))
	})

	// Each refusal below is one verify_object_permission would make of carol
	// with a READ grant on k, which radosgw's check_caps never consults.
	DescribeTable("evaluates no ACL or policy, so a check verify_object_permission makes refuses no holder of the cap",
		func(ctx SpecContext, change func(context.Context), hdr ...string) {
			p := acl.DefaultPolicy(alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "carol", Name: "Carol", Permission: acl.PermRead})
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader(""), op.PutParams{
				Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(p)},
			})
			Expect(err).NotTo(HaveOccurred())
			change(ctx)
			_, err = run(ctx, request(carol, meta.ObjKey{Name: "k"}, hdr...))
			Expect(err).NotTo(HaveOccurred())
			Expect(laidOut().Key).To(Equal(meta.ObjKey{Name: "k"}))
		},
		Entry("a bucket policy denying the holder s3:*", func(ctx context.Context) {
			const deny = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/carol"]},` +
				`"Action":"s3:*","Resource":["arn:aws:s3:::plain","arn:aws:s3:::plain/*"]}]}`
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Attrs[op.AttrIAMPolicy] = []byte(deny) })
		}),
		Entry("an x-amz-expected-bucket-owner naming another owner", func(context.Context) {}, "X-Amz-Expected-Bucket-Owner", "someone"),
		Entry("an object ACL with no grant to the holder", func(ctx context.Context) {
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader(""), op.PutParams{
				Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(alice.Owner, "Alice"))},
			})
			Expect(err).NotTo(HaveOccurred())
		}),
		Entry("a requester-pays bucket and no x-amz-request-payer", func(ctx context.Context) {
			setBucket(ctx, func(rec *op.BucketRecord) { rec.Info.RequesterPays = true })
		}),
	)

	DescribeTable("lays out the version StatObject resolves",
		func(ctx SpecContext, named, want meta.ObjKey) {
			env.Objects = currentVersionHeads{store}
			_, err := run(ctx, request(carol, named))
			Expect(err).NotTo(HaveOccurred())
			Expect(laidOut().Key).To(Equal(want))
		},
		Entry("the current one for a key named without a versionId", meta.ObjKey{Name: "v"}, meta.ObjKey{Name: "v", Instance: "v2"}),
		Entry("the one a versionId names", meta.ObjKey{Name: "v", Instance: "v1"}, meta.ObjKey{Name: "v", Instance: "v1"}),
	)

	It("answers the layout's error as the request's, with no document", func(ctx SpecContext) {
		layout.ObjectLayoutReturns(nil, fmt.Errorf("%w: plain/k has no manifest to lay out", op.ErrInternalError))
		o, err := run(ctx, request(carol, meta.ObjKey{Name: "k"}))
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(o.Document).To(BeNil())
	})
})
