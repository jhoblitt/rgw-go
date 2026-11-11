package op_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("GetObject's object-lock read permissions", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var (
				store  *memstore.Store
				env    *op.Env
				alice  op.Identity
				authz  *opfakes.FakeAuthorizer
				denied map[policy.Action]error
			)
			// setFlags gives plain flags.
			setFlags := func(ctx context.Context, flags uint32) {
				GinkgoHelper()
				rec, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				rec.Info.Flags = flags
				Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
			}
			BeforeEach(func(ctx SpecContext) {
				store = memstore.New(memstore.Config{Release: rel, Now: smallMtime})
				denied = map[policy.Action]error{}
				authz = &opfakes.FakeAuthorizer{}
				authz.VerifyObjectStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
					return denied[a]
				}
				env = &op.Env{
					Zone: store, Users: store, Buckets: store, Objects: store, Usage: store,
					Authz: authz, Metrics: op.NopMetrics{}, Conf: cephconf.NewOptions(cephconf.MapGetter{}),
				}
				a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
				alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
				rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"}})
				Expect(err).NotTo(HaveOccurred())
				_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, bytes.NewReader([]byte("x")), op.PutParams{Size: 1})
				Expect(err).NotTo(HaveOccurred())
				setFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
			})
			// get runs alice's GET of k, as admin when admin is set.
			get := func(ctx context.Context, admin bool) (*op.GetObject, error) {
				r := &op.Request{
					Method: http.MethodGet, Bucket: "plain", Object: meta.ObjKey{Name: "k"}, Identity: alice, Env: env,
					Header: http.Header{}, Query: url.Values{},
				}
				r.Identity.Admin = admin
				o := &op.GetObject{GetData: true, Sink: &captureSink{}}
				return o, op.Run(ctx, o, r)
			}
			// actions are the actions VerifyObject was asked, in order.
			actions := func() []policy.Action {
				var got []policy.Action
				for i := range authz.VerifyObjectCallCount() {
					_, _, a, perm := authz.VerifyObjectArgsForCall(i)
					Expect(perm).To(Equal(acl.PermFor(a)))
					got = append(got, a)
				}
				return got
			}

			It("evaluates neither on a bucket without object lock", func(ctx SpecContext) {
				setFlags(ctx, meta.BucketVersioned)
				o, err := get(ctx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
				Expect([]bool{o.RetentionAllowed, o.LegalHoldAllowed}).To(Equal([]bool{false, false}))
			})
			DescribeTable("evaluates both on the object, after the read's own, as bools that refuse nothing",
				func(ctx SpecContext, deny []policy.Action, retention, hold bool) {
					for _, a := range deny {
						denied[a] = op.ErrAccessDenied
					}
					o, err := get(ctx, false)
					Expect(err).NotTo(HaveOccurred())
					Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectRetention, policy.S3GetObjectLegalHold}))
					Expect([]bool{o.RetentionAllowed, o.LegalHoldAllowed}).To(Equal([]bool{retention, hold}))
				},
				Entry("both allowed", nil, true, true),
				Entry("s3:GetObjectRetention refused", []policy.Action{policy.S3GetObjectRetention}, false, true),
				Entry("s3:GetObjectLegalHold refused", []policy.Action{policy.S3GetObjectLegalHold}, true, false),
				Entry("both refused", []policy.Action{policy.S3GetObjectRetention, policy.S3GetObjectLegalHold}, false, false),
			)
			It("takes a check that fails with anything but an access denial for not allowed, and serves the read", func(ctx SpecContext) {
				denied[policy.S3GetObjectRetention] = op.ErrUnknown
				o, err := get(ctx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect([]bool{o.RetentionAllowed, o.LegalHoldAllowed}).To(Equal([]bool{false, true}))
			})
			It("evaluates neither for a requester the read's own check refuses", func(ctx SpecContext) {
				denied[policy.S3GetObject] = op.ErrAccessDenied
				_, err := get(ctx, false)
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
			})
			It("evaluates them for an admin the override lets through the read's refusal on Tentacle alone, which checks them first", func(ctx SpecContext) {
				denied[policy.S3GetObject] = op.ErrAccessDenied
				o, err := get(ctx, true)
				Expect(err).NotTo(HaveOccurred())
				if rel < denc.Tentacle {
					Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
					Expect([]bool{o.RetentionAllowed, o.LegalHoldAllowed}).To(Equal([]bool{false, false}))
					return
				}
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectRetention, policy.S3GetObjectLegalHold}))
				Expect([]bool{o.RetentionAllowed, o.LegalHoldAllowed}).To(Equal([]bool{true, true}))
			})
		})
	}
})
