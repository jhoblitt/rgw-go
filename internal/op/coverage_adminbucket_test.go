package op_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// This spec pins docs/differences.md, "The admin bucket info reports a
// bucket named tenant/name whatever the uid's tenant".
var _ = Describe("admin bucket info of a tenant/name bucket", func() {
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{})
		for _, id := range []meta.UserID{{ID: "admin"}, {Tenant: "t2", ID: "u"}} {
			info := meta.NewUserInfo()
			info.UserID, info.DisplayName = id, "This is "+id.ID
			store.AddUser(info)
		}
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Tenant: "t1", Name: "tb", Owner: meta.UserOwner(meta.UserID{ID: "admin"}), Zonegroup: store.ZoneGroup().ID,
		})
		Expect(err).NotTo(HaveOccurred())
	})
	run := func(ctx context.Context, o op.Op) error {
		caps := meta.Caps{"buckets": meta.CapAll}
		u := meta.NewUserInfo()
		u.UserID, u.Caps = meta.UserID{ID: "admin"}, caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}

	It("reports the bucket it loaded when the uid names another tenant", func(ctx SpecContext) {
		o := op.NewBucketInfo()
		o.Bucket, o.UID, o.UIDGiven = "t1/tb", meta.UserID{Tenant: "t2", ID: "u"}, true
		Expect(run(ctx, o)).To(Succeed(), "radosgw looks t1/tb up again under t2 and answers 200 with no document")
		Expect(o.Single).NotTo(BeNil())
		Expect(o.Single.Rec.Info.Bucket.Tenant).To(Equal("t1"))
		Expect(o.Single.Rec.Info.Bucket.Name).To(Equal("tb"))
	})
})
