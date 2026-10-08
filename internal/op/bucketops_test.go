package op_test

import (
	"context"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// messageOf is the message an op error carries.
func messageOf(err error) string {
	e, ok := errors.AsType[*op.Error](err)
	if !ok {
		return ""
	}
	return e.Message
}

var _ = Describe("bucket ops", func() {
	const (
		zgID    = "zg-1"
		apiName = "ceph-objectstore"
	)
	var (
		store *memstore.Store
		env   *op.Env
		alice *op.UserRecord
		bob   *op.UserRecord
	)
	zonegroup := func() meta.ZoneGroup {
		return meta.ZoneGroup{
			ID: zgID, Name: apiName, APIName: apiName, IsMaster: true,
			DefaultPlacement: meta.PlacementRule{Name: "default-placement"},
			PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
				"default-placement": {Name: "default-placement", StorageClasses: []string{meta.StorageClassStandard}},
				"fast":              {Name: "fast", StorageClasses: []string{meta.StorageClassStandard}},
				"tagged":            {Name: "tagged", Tags: []string{"gold"}, StorageClasses: []string{meta.StorageClassStandard}},
				"nowhere":           {Name: "nowhere", StorageClasses: []string{meta.StorageClassStandard}},
			},
		}
	}
	newStore := func(withPeriod bool, others ...meta.ZoneGroup) {
		zg := zonegroup()
		cfg := memstore.Config{ZoneGroup: zg}
		pools := func(name string) meta.ZonePlacementInfo {
			pi := meta.NewZonePlacementInfo()
			pi.IndexPool = meta.Pool{Name: name + ".index"}
			pi.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {DataPool: &meta.Pool{Name: name + ".data"}}}
			return pi
		}
		cfg.Params.PlacementPools = map[string]meta.ZonePlacementInfo{
			"default-placement": pools("default"), "fast": pools("fast"), "tagged": pools("tagged"),
		}
		if withPeriod {
			p := meta.NewPeriod()
			p.ID = "period-1"
			p.PeriodMap.ZoneGroups = map[string]meta.ZoneGroup{zgID: zg}
			for _, o := range others {
				p.PeriodMap.ZoneGroups[o.ID] = o
			}
			cfg.Period = p
		}
		store = memstore.New(cfg)
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Stats: store,
			Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
		}
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll, MaxBuckets: 1})
		bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll, MaxBuckets: 1000})
	}
	BeforeEach(func() { newStore(true) })
	identity := func(rec *op.UserRecord) op.Identity {
		return op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask}
	}
	request := func(who *op.UserRecord, bucket string) *op.Request {
		return &op.Request{Env: env, Bucket: bucket, Identity: identity(who)}
	}
	policyOf := func(who *op.UserRecord) acl.Policy {
		return acl.DefaultPolicy(meta.UserOwner(who.Info.UserID), who.Info.DisplayName)
	}
	create := func(ctx context.Context, who *op.UserRecord, bucket string, edit ...func(*op.CreateBucket)) (*op.CreateBucket, *op.Request, error) {
		o := &op.CreateBucket{ACL: policyOf(who)}
		for _, e := range edit {
			e(o)
		}
		r := request(who, bucket)
		return o, r, op.Run(ctx, o, r)
	}

	Describe("CreateBucket", func() {
		It("is radosgw's create_bucket, a write authorized as s3:CreateBucket", func() {
			o := &op.CreateBucket{}
			Expect(o.Name()).To(Equal("create_bucket"))
			Expect(o.Action()).To(Equal(policy.S3CreateBucket))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
		})
		It("creates the bucket for the ACL's owner with its ACL, in this zonegroup and its default placement", func(ctx SpecContext) {
			o, r, err := create(ctx, bob, "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Existed).To(BeFalse())
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Owner).To(Equal(meta.UserOwner(bob.Info.UserID)))
			Expect(rec.Info.Zonegroup).To(Equal(zgID))
			Expect(rec.Info.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement"}))
			got, err := op.BucketACLFor(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(policyOf(bob)))
			Expect(o.Result.Info.Bucket).To(Equal(rec.Info.Bucket))
			Expect(r.BucketRec.Info.Owner).To(Equal(meta.UserOwner(bob.Info.UserID)), "the usage owner, s->bucket_owner (rgw_op.cc:3581)")
		})
		It("runs Params first in Execute, after the permission check", func(ctx SpecContext) {
			failing := func(o *op.CreateBucket) {
				o.Params = func(context.Context, *op.CreateBucket) error { return op.ErrInvalidBucketName }
			}
			_, _, err := create(ctx, bob, "plain", failing)
			Expect(err).To(MatchError(op.ErrInvalidBucketName))
			o := &op.CreateBucket{ACL: policyOf(bob)}
			failing(o)
			r := request(bob, "plain")
			r.Identity = op.Anonymous()
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied), "get_params runs in execute, rgw_op.cc:3460")
		})
		It("refuses an anonymous request", func(ctx SpecContext) {
			r := request(bob, "plain")
			r.Identity = op.Anonymous()
			r.Identity.OpMask = op.OpTypeAll
			Expect(op.Run(ctx, &op.CreateBucket{}, r)).To(MatchError(op.ErrAccessDenied), "rgw_op.cc:3220-3222")
		})
		It("refuses a bucket in another tenant", func(ctx SpecContext) {
			o := &op.CreateBucket{ACL: policyOf(bob)}
			r := request(bob, "plain")
			r.Tenant = "other"
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied), "rgw_op.cc:3232-3241")
		})
		Describe("the location constraint", func() {
			It("must name a zonegroup of the period by its api name", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.LocationConstraint = "other" })
				Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
				Expect(messageOf(err)).To(Equal("The other location constraint is not valid."), "rgw_op.cc:3473-3481")
			})
			It("must be this zonegroup's api name without a period", func(ctx SpecContext) {
				newStore(false)
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.LocationConstraint = "other" })
				Expect(err).To(MatchError(op.ErrIllegalLocationConstraint))
				Expect(messageOf(err)).To(Equal("The other location constraint is incompatible for the region specific endpoint this request was sent to."),
					"rgw_op.cc:3483-3491")
				_, _, err = create(ctx, bob, "plain", func(o *op.CreateBucket) { o.LocationConstraint = apiName })
				Expect(err).NotTo(HaveOccurred())
			})
			It("must land a user's bucket in this zonegroup", func(ctx SpecContext) {
				newStore(true, meta.ZoneGroup{ID: "zg-2", Name: "west", APIName: "west"})
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.LocationConstraint = "west" })
				Expect(err).To(MatchError(op.ErrIllegalLocationConstraint))
				Expect(messageOf(err)).To(Equal("The west location constraint is incompatible for the region specific endpoint this request was sent to."),
					"rgw_op.cc:3507-3519")
			})
			It("is not checked under rgw_relaxed_region_enforcement", func(ctx SpecContext) {
				env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_relaxed_region_enforcement": "true"})
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.LocationConstraint = "other" })
				Expect(err).NotTo(HaveOccurred(), "rgw_op.cc:3471")
			})
		})
		Describe("the placement", func() {
			DescribeTable("is the requested rule, else the user's default, else the zonegroup's",
				func(ctx SpecContext, requested, userDefault, want meta.PlacementRule) {
					bob.Info.DefaultPlacement = userDefault
					_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = requested })
					Expect(err).NotTo(HaveOccurred())
					rec, err := store.GetBucket(ctx, "", "plain")
					Expect(err).NotTo(HaveOccurred())
					Expect(rec.Info.PlacementRule).To(Equal(want))
				},
				Entry("requested", meta.PlacementRule{Name: "fast"}, meta.PlacementRule{Name: "default-placement"}, meta.PlacementRule{Name: "fast"}),
				Entry("the user's default", meta.PlacementRule{}, meta.PlacementRule{Name: "fast"}, meta.PlacementRule{Name: "fast"}),
				Entry("a requested class takes the user default's name", meta.PlacementRule{StorageClass: "STANDARD"}, meta.PlacementRule{Name: "fast", StorageClass: "COLD"},
					meta.PlacementRule{Name: "fast", StorageClass: "STANDARD"}),
				Entry("the zonegroup's", meta.PlacementRule{}, meta.PlacementRule{}, meta.PlacementRule{Name: "default-placement"}),
			)
			It("is ZonegroupDefaultPlacementMisconfiguration when no default names one", func(ctx SpecContext) {
				zg := zonegroup()
				zg.DefaultPlacement = meta.PlacementRule{}
				zone := &opfakes.FakeZoneInfo{}
				zone.ZoneGroupReturns(zg)
				env.Zone = zone
				_, _, err := create(ctx, bob, "plain")
				Expect(err).To(MatchError(op.ErrZonegroupPlacementMisconfig), "rgw_op.cc:3430-3434")
			})
			It("must be a placement target of the zonegroup", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "absent"} })
				Expect(err).To(MatchError(op.ErrInvalidLocationConstraint), "rgw_op.cc:3439-3444")
			})
			It("must carry a tag of the user's when the target has tags", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "tagged"} })
				Expect(err).To(MatchError(op.ErrAccessDenied), "user_permitted, rgw_op.cc:3446-3451")
				bob.Info.PlacementTags = []string{"silver", "gold"}
				_, _, err = create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "tagged"} })
				Expect(err).NotTo(HaveOccurred())
			})
			It("must be a placement of the zone", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "nowhere"} })
				Expect(err).To(MatchError(op.ErrInvalidLocationConstraint), "find_zone_placement, rgw_op.cc:3531-3539")
			})
		})
		Describe("the owner's bucket limit", func() {
			It("refuses a bucket past max_buckets", func(ctx SpecContext) {
				_, _, err := create(ctx, alice, "first")
				Expect(err).NotTo(HaveOccurred())
				_, _, err = create(ctx, alice, "second")
				Expect(err).To(MatchError(op.ErrTooManyBuckets), "rgw_op.cc:3206-3209")
			})
			It("refuses every bucket at a negative max_buckets", func(ctx SpecContext) {
				alice.Info.MaxBuckets = -1
				_, _, err := create(ctx, alice, "first")
				Expect(err).To(MatchError(op.ErrAccessDenied), "-EPERM, rgw_op.cc:3186-3188")
			})
			It("does not count at a zero max_buckets", func(ctx SpecContext) {
				alice.Info.MaxBuckets = 0
				for _, name := range []string{"first", "second"} {
					_, _, err := create(ctx, alice, name)
					Expect(err).NotTo(HaveOccurred(), name)
				}
			})
			It("reads the limit of an account owner from the account", func(ctx SpecContext) {
				acct := meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme", MaxBuckets: 1}
				store.AddAccount(acct)
				owner := meta.AccountOwner(acct.ID)
				_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "theirs", Owner: owner})
				Expect(err).NotTo(HaveOccurred())
				o := &op.CreateBucket{ACL: acl.DefaultPolicy(owner, "acme")}
				r := request(bob, "plain")
				r.Identity.Owner, r.Identity.Account = owner, &acct
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrTooManyBuckets), "get_account_max_buckets, rgw_op.cc:3176-3182")
			})
			It("pages the owner's buckets rgw_list_buckets_max_chunk at a time, at least the limit", func(ctx SpecContext) {
				users := &opfakes.FakeUserStore{}
				users.ListUserBucketsReturnsOnCall(0, []meta.BucketEnt{{}}, "m", true, nil)
				users.ListUserBucketsReturnsOnCall(1, nil, "", false, nil)
				env.Users = users
				env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "1"})
				bob.Info.MaxBuckets = 3
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				Expect(users.ListUserBucketsCallCount()).To(Equal(2))
				_, _, marker, n := users.ListUserBucketsArgsForCall(0)
				Expect([]any{marker, n}).To(Equal([]any{"", 3}), "max(chunk, remaining), rgw_op.cc:3198")
				_, _, marker, n = users.ListUserBucketsArgsForCall(1)
				Expect([]any{marker, n}).To(Equal([]any{"m", 2}))
			})
		})
		Describe("an existing bucket", func() {
			It("answers its owner's re-create as having existed", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				o, r, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred(), "-ERR_BUCKET_EXISTS is sent as 200, rgw_rest_s3.cc:2546-2547")
				Expect(o.Existed).To(BeTrue())
				Expect(r.BucketRec.Info.Owner).To(Equal(meta.UserOwner(bob.Info.UserID)))
			})
			It("refuses another owner by the ACL comparison", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				alice.Info.MaxBuckets = 0
				_, r, err := create(ctx, alice, "plain")
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
				Expect(messageOf(err)).To(Equal("Cannot modify existing access control policy"), "rgw_op.cc:3569-3578")
				Expect(r.BucketRec.Info.Owner.String()).To(BeEmpty(), "refused before s->bucket_owner is set")
			})
			It("refuses a changed placement", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				_, _, err = create(ctx, bob, "plain", func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "fast"} })
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
				Expect(messageOf(err)).To(Equal("Cannot modify existing bucket's placement rule"), "rgw_op.cc:3562-3567")
				_, _, err = create(ctx, bob, "plain", func(o *op.CreateBucket) {
					o.Placement = meta.PlacementRule{Name: "default-placement", StorageClass: meta.StorageClassStandard}
				})
				Expect(err).NotTo(HaveOccurred(), "an empty class is STANDARD, rgw_placement_types.h:71-74")
			})
			It("refuses a changed zonegroup", func(ctx SpecContext) {
				rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(bob.Info.UserID), Zonegroup: "zg-2"})
				Expect(err).NotTo(HaveOccurred())
				Expect(rec).NotTo(BeNil())
				_, _, err = create(ctx, bob, "plain")
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
				Expect(messageOf(err)).To(Equal("Cannot modify existing bucket's zonegroup"), "rgw_op.cc:3552-3556")
			})
			It("logs a payer-only usage entry when a requester-pays bucket refuses the create", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				rec, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				rec.Info.RequesterPays = true
				Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
				alice.Info.MaxBuckets = 0
				_, r, err := create(ctx, alice, "plain")
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
				usage := &opfakes.FakeUsageLogger{}
				env.Usage = usage
				r.Status = 409
				op.LogUsage(ctx, r, "create_bucket")
				Expect(usage.LogCallCount()).To(Equal(1))
				_, e := usage.LogArgsForCall(0)
				Expect(e.Owner.String()).To(BeEmpty(), "rgw_log.cc:207-208")
				Expect(e.Payer).To(Equal(meta.UserOwner(alice.Info.UserID)), "rgw_log.cc:215-219")
			})
			It("answers 409 when another owner's create lands first, which radosgw answers 200", func(ctx SpecContext) {
				theirs := &op.BucketRecord{Info: meta.BucketInfo{Owner: meta.UserOwner(alice.Info.UserID), Zonegroup: zgID}}
				buckets := &opfakes.FakeBucketStore{}
				buckets.GetBucketReturns(nil, op.ErrNoSuchBucket)
				buckets.CreateBucketReturns(theirs, op.ErrBucketAlreadyExists)
				env.Buckets = buckets
				o, r, err := create(ctx, bob, "plain")
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
				Expect(o.Existed).To(BeFalse())
				Expect(r.BucketRec.Info.Owner).To(Equal(meta.UserOwner(bob.Info.UserID)), "the usage owner is still the creator")
			})
		})
		Describe("x-amz-bucket-object-lock-enabled", func() {
			lock := func(v string) func(*op.CreateBucket) {
				return func(o *op.CreateBucket) { o.ObjectLock, o.HasObjectLock = v, true }
			}
			DescribeTable("answers NotImplemented for true in any case and creates nothing",
				func(ctx SpecContext, v string) {
					_, _, err := create(ctx, bob, "plain", lock(v))
					Expect(err).To(MatchError(op.ErrNotImplemented))
					_, err = store.GetBucket(ctx, "", "plain")
					Expect(err).To(MatchError(op.ErrNoSuchBucket))
				},
				Entry("true", "true"),
				Entry("TRUE", "TRUE"),
			)
			It("refuses a value other than true or false", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", lock("maybe"))
				Expect(err).To(MatchError(op.ErrInvalidArgument), "rgw_rest_s3.cc:2534-2540")
			})
			It("creates an ordinary bucket for false", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", lock("False"))
				Expect(err).NotTo(HaveOccurred())
			})
			It("is checked after the permission check", func(ctx SpecContext) {
				o := &op.CreateBucket{}
				lock("maybe")(o)
				r := request(bob, "plain")
				r.Identity = op.Anonymous()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			})
		})
		Describe("a requested bucket index", func() {
			index := func(o *op.CreateBucket) { o.BucketIndex = true }
			It("answers NotImplemented and creates nothing", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", index)
				Expect(err).To(MatchError(op.ErrNotImplemented))
				_, err = store.GetBucket(ctx, "", "plain")
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
			})
			It("comes after the object-lock header's check", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", index, func(o *op.CreateBucket) { o.ObjectLock, o.HasObjectLock = "maybe", true })
				Expect(err).To(MatchError(op.ErrInvalidArgument), "get_params checks the header after the body, rgw_rest_s3.cc:2695-2701 at v20.2.4")
			})
			It("comes after the location constraint's and placement's checks", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain", index, func(o *op.CreateBucket) { o.LocationConstraint = "other" })
				Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
				_, _, err = create(ctx, bob, "plain", index, func(o *op.CreateBucket) { o.Placement = meta.PlacementRule{Name: "absent"} })
				Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
			})
			It("leaves another owner's existing bucket to the existing-bucket checks", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				alice.Info.MaxBuckets = 0
				_, _, err = create(ctx, alice, "plain", index)
				Expect(err).To(MatchError(op.ErrBucketAlreadyExists), "radosgw's 409, rgw_op.cc:3797-3806 at v20.2.4")
			})
			It("answers the owner's re-create of an existing bucket as having existed", func(ctx SpecContext) {
				_, _, err := create(ctx, bob, "plain")
				Expect(err).NotTo(HaveOccurred())
				o, _, err := create(ctx, bob, "plain", index)
				Expect(err).NotTo(HaveOccurred(), "-ERR_BUCKET_EXISTS whatever the index, rgw_sal_rados.cc:195-201 at v20.2.4")
				Expect(o.Existed).To(BeTrue())
			})
		})
	})

	Describe("DeleteBucket", func() {
		BeforeEach(func(ctx SpecContext) {
			_, _, err := create(ctx, bob, "plain")
			Expect(err).NotTo(HaveOccurred())
		})
		It("is radosgw's delete_bucket, a delete authorized as s3:DeleteBucket", func() {
			o := &op.DeleteBucket{}
			Expect(o.Name()).To(Equal("delete_bucket"))
			Expect(o.Action()).To(Equal(policy.S3DeleteBucket))
			Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
		})
		It("deletes the bucket", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.DeleteBucket{}, request(bob, "plain"))).To(Succeed())
			_, err := store.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		It("answers NoSuchBucket for a missing bucket", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.DeleteBucket{}, request(bob, "missing"))).To(MatchError(op.ErrNoSuchBucket))
		})
		It("answers BucketNotEmpty for a bucket with objects", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader("v"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(op.Run(ctx, &op.DeleteBucket{}, request(bob, "plain"))).To(MatchError(op.ErrBucketNotEmpty))
		})
		It("refuses a requester the authorizer refuses", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.DeleteBucket{}, request(alice, "plain"))).To(MatchError(op.ErrAccessDenied))
		})
		It("answers success when the store lost an entry-point race", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketReturns(rec, nil)
			buckets.DeleteBucketReturns(op.ErrConcurrentModification)
			env.Buckets = buckets
			Expect(op.Run(ctx, &op.DeleteBucket{}, request(bob, "plain"))).To(Succeed(), "rgw_op.cc:3792-3797")
		})
	})

	Describe("StatBucket", func() {
		BeforeEach(func(ctx SpecContext) {
			_, _, err := create(ctx, bob, "plain")
			Expect(err).NotTo(HaveOccurred())
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader("abc"), op.PutParams{Size: 3})
			Expect(err).NotTo(HaveOccurred())
		})
		It("is radosgw's stat_bucket, a read authorized as s3:ListBucket", func() {
			o := &op.StatBucket{}
			Expect(o.Name()).To(Equal("stat_bucket"))
			Expect(o.Action()).To(Equal(policy.S3ListBucket), "rgw_op.cc:2989-2991")
			Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		})
		It("reads the bucket's stats when asked", func(ctx SpecContext) {
			o := &op.StatBucket{ReadStats: true}
			Expect(op.Run(ctx, o, request(bob, "plain"))).To(Succeed())
			Expect(o.Stats.NumObjects).To(BeEquivalentTo(1))
			Expect(o.Stats.Size).To(BeEquivalentTo(3))
		})
		It("reads no stats otherwise", func(ctx SpecContext) {
			stats := &opfakes.FakeStatsStore{}
			env.Stats = stats
			Expect(op.Run(ctx, &op.StatBucket{}, request(bob, "plain"))).To(Succeed())
			Expect(stats.BucketStatsCallCount()).To(BeZero(), "v20.2.4 rgw_op.cc:3262-3264")
		})
		It("answers NoSuchBucket for a missing bucket", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.StatBucket{}, request(bob, "missing"))).To(MatchError(op.ErrNoSuchBucket))
		})
	})

	Describe("GetBucketLocation", func() {
		It("is radosgw's get_bucket_location, a read authorized as s3:GetBucketLocation", func() {
			o := &op.GetBucketLocation{}
			Expect(o.Name()).To(Equal("get_bucket_location"))
			Expect(o.Action()).To(Equal(policy.S3GetBucketLocation))
			Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		})
		DescribeTable("names the bucket's zonegroup by its api name, as get_zonegroup finds it",
			func(ctx SpecContext, withPeriod bool, bucketZG, want string) {
				newStore(withPeriod, meta.ZoneGroup{ID: "zg-2", Name: "west", APIName: "west"})
				_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(bob.Info.UserID), Zonegroup: bucketZG})
				Expect(err).NotTo(HaveOccurred())
				o := &op.GetBucketLocation{}
				Expect(op.Run(ctx, o, request(bob, "plain"))).To(Succeed())
				Expect(o.APIName).To(Equal(want))
			},
			Entry("this zonegroup", true, zgID, apiName),
			Entry("another zonegroup of the period", true, "zg-2", "west"),
			Entry("a zonegroup the period lacks: its id, rgw_rest_s3.cc:2141-2145", true, "zg-9", "zg-9"),
			Entry("a zonegroup the period lacks named default: nothing", true, "default", ""),
			Entry("another zonegroup without a period: get_zonegroup's empty zonegroup, svc_zone.cc:634-643", false, "zg-9", ""),
		)
		It("answers NoSuchBucket for a missing bucket", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.GetBucketLocation{}, request(bob, "missing"))).To(MatchError(op.ErrNoSuchBucket))
		})
	})
})
