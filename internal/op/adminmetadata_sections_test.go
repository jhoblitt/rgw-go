package op_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("the admin metadata section tables", func() {
	var (
		ctx   context.Context
		clock time.Time
		store *memstore.Store
		env   *op.Env
		inst  string
	)
	// open makes store a memstore of release rel holding the user leseb and
	// her bucket test, the instance's key inst, and env over it.
	open := func(rel denc.Release) {
		GinkgoHelper()
		store = memstore.New(memstore.Config{
			Release: rel,
			Now:     func() time.Time { return clock },
			Params: meta.ZoneParams{
				Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": meta.NewZonePlacementInfo()},
			},
		})
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "leseb"}
		info.DisplayName = "leseb"
		store.AddUser(info)
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "test", Owner: meta.UserOwner(info.UserID), Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
		inst = "test:" + rec.Info.Bucket.ID
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	}
	BeforeEach(func() {
		ctx = context.Background()
		clock = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
		open(denc.Squid)
	})
	run := func(c context.Context, o op.Op) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = meta.Caps{"metadata": meta.CapAll}
		return op.Run(c, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: u.Caps,
		}})
	}
	put := func(section, key string, sync op.SyncType, body []byte) (*op.PutMetadata, error) {
		o := &op.PutMetadata{Section: section, Key: key, Sync: sync, Body: body}
		return o, run(ctx, o)
	}
	// envelope is a put's document carrying ver and data.
	envelope := func(ver meta.ObjVersion, data json.RawMessage) []byte {
		GinkgoHelper()
		b, err := json.Marshal(map[string]any{"ver": ver, "data": data})
		Expect(err).NotTo(HaveOccurred())
		return b
	}

	Describe("the top-level listing", func() {
		It("lists the sections the store names, in std::set order and each once", func(sctx SpecContext) {
			fake := new(opfakes.FakeMetadataStore)
			fake.SectionsReturns([]string{"user", "otp", "bucket.instance", "bucket", "user"})
			env.Metadata = fake
			o := &op.ListMetadata{}
			Expect(run(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket", "bucket.instance", "otp", "user"}))
			Expect(o.Truncated).To(BeFalse())
			Expect(fake.ListCallCount()).To(BeZero(), "the top level asks no section for its keys")
		}, SpecTimeout(listTimeout))
		It("pages from the first section not below the marker, over the store's sections", func(sctx SpecContext) {
			fake := new(opfakes.FakeMetadataStore)
			fake.SectionsReturns([]string{"user", "otp", "bucket"})
			env.Metadata = fake
			o := &op.ListMetadata{Extended: true, MaxEntries: 1, Marker: base64.StdEncoding.EncodeToString([]byte("c"))}
			Expect(run(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"otp"}))
			Expect(o.Truncated).To(BeTrue())
			Expect(o.NextMarker).To(Equal("user"))
		}, SpecTimeout(listTimeout))
		It("lists the memstore's sections as today's three", func(sctx SpecContext) {
			Expect(store.Sections()).To(ConsistOf("bucket", "bucket.instance", "user"))
			o := &op.ListMetadata{}
			Expect(run(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket", "bucket.instance", "user"}))
		}, SpecTimeout(listTimeout))
	})

	Describe("a put's section", func() {
		It("takes a put to every section the memstore serves, reading its body", func() {
			for _, section := range store.Sections() {
				_, err := put(section, "x", op.SyncAlways, []byte(`not json`))
				Expect(err).To(MatchError(op.ErrInvalidArgument), "section %s", section)
			}
		})
		It("refuses a put to a section the store serves that no put rule names, before it reads the body", func() {
			fake := new(opfakes.FakeMetadataStore)
			fake.SectionsReturns([]string{"kittens"})
			env.Metadata = fake
			o, err := put("kittens", "x", op.SyncAlways, []byte(`not json`))
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(o.OnDisk).To(Equal(meta.ObjVersion{}))
			Expect(fake.GetCallCount()).To(BeZero())
			Expect(fake.PutCallCount()).To(BeZero())
		})
	})

	DescribeTable("reports each section's update status and the version read, on both releases",
		func(section string, key func() string, sync op.SyncType, applied bool, status string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				open(rel)
				g := &op.GetMetadata{Section: section, Key: key()}
				Expect(run(ctx, g)).To(Succeed(), "release %s", rel)
				o, err := put(section, key(), sync, envelope(g.Result.Version, g.Result.Data))
				Expect(err).NotTo(HaveOccurred(), "release %s", rel)
				Expect(o.Applied).To(Equal(applied), "release %s", rel)
				Expect(o.UpdateStatus()).To(Equal(status), "release %s", rel)
				want := meta.ObjVersion{}
				if applied {
					want = g.Result.Version
				}
				Expect(o.OnDisk).To(Equal(want), "release %s", rel)
			}
		},
		Entry("an applied user", "user", func() string { return "leseb" }, op.SyncAlways, true, "applied"),
		Entry("a user check_versions skips", "user", func() string { return "leseb" }, op.SyncByVersion, false, "skipped"),
		Entry("an applied entry point, whose handler answers 0", "bucket", func() string { return "test" }, op.SyncAlways, true, ""),
		Entry("an entry point check_versions skips", "bucket", func() string { return "test" }, op.SyncByVersion, false, "skipped"),
		Entry("an applied bucket instance", "bucket.instance", func() string { return inst }, op.SyncAlways, true, "applied"),
		Entry("a bucket instance check_versions skips", "bucket.instance", func() string { return inst }, op.SyncByVersion, false, "skipped"),
	)

	Describe("a section's put rule", func() {
		const section = "rule-test"
		var (
			fake   *opfakes.FakeMetadataStore
			ondisk meta.ObjVersion
		)
		BeforeEach(func() {
			zone := new(opfakes.FakeZoneInfo)
			zone.ReleaseReturns(denc.Tentacle)
			env.Zone = zone
			fake = new(opfakes.FakeMetadataStore)
			ondisk = meta.ObjVersion{Ver: 7, Tag: "_t"}
			fake.GetReturns(op.MetadataEntry{Version: ondisk}, nil)
			env.Metadata = fake
		})
		DescribeTable("is given whether the put applied and the zone's release",
			func(sync op.SyncType, applied bool) {
				type call struct {
					applied bool
					rel     denc.Release
				}
				var calls []call
				DeferCleanup(op.SetMetadataPutRuleForTest(section, func(applied bool, rel denc.Release) string {
					calls = append(calls, call{applied, rel})
					return "reported"
				}, false))
				o, err := put(section, "k", sync, envelope(ondisk, json.RawMessage(`{}`)))
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Applied).To(Equal(applied))
				Expect(o.UpdateStatus()).To(Equal("reported"))
				Expect(calls).To(Equal([]call{{applied, denc.Tentacle}}))
			},
			Entry("for an applied put", op.SyncAlways, true),
			Entry("for a put check_versions skips", op.SyncByVersion, false),
		)
		It("reports the zero version for a handler that reads none, and still writes under the version its get read", func() {
			DeferCleanup(op.SetMetadataPutRuleForTest(section, func(bool, denc.Release) string { return "applied" }, true))
			o, err := put(section, "k", op.SyncAlways, envelope(meta.ObjVersion{}, json.RawMessage(`{}`)))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Applied).To(BeTrue())
			Expect(o.OnDisk).To(Equal(meta.ObjVersion{}), "RGWX_UPDATE_VERSION reports ver:0,tag:")
			Expect(fake.PutCallCount()).To(Equal(1))
			_, _, _, _, opts := fake.PutArgsForCall(0)
			Expect(opts.IfVersion).To(Equal(&ondisk))
		})
	})
})
