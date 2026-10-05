package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("admin ops", func() {
	const fsid = "75d1938b-2949-4933-8386-fb2d1449ff03"
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}})
		env = &op.Env{Zone: store, Metrics: op.NopMetrics{}, ClusterID: fsid, Usage: store}
	})
	withCaps := func(caps meta.Caps) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}
	}
	It("checks the info cap and returns the fsid", func(ctx SpecContext) {
		o := op.NewGetInfo()
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ClusterID).To(Equal(fsid))
	})
	It("denies a missing cap and an anonymous identity", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapWrite})})).To(MatchError(op.ErrAccessDenied))
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: op.Anonymous()})).To(MatchError(op.ErrAccessDenied))
	})
	It("lets an admin identity through without caps, as rgw_process_authenticated does", func(ctx SpecContext) {
		id := withCaps(nil)
		id.Admin = true
		o := &op.GetInfo{}
		Expect(op.Run(ctx, o, &op.Request{Env: env, Identity: id})).To(Succeed())
		Expect(o.ClusterID).To(Equal(fsid))
	})
	It("passes the op-mask check with a zero mask", func(ctx SpecContext) {
		id := withCaps(meta.Caps{"info": meta.CapRead})
		id.OpMask = 0
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: id})).To(Succeed())
	})
	It("reads the zone params under zone=read", func(ctx SpecContext) {
		o := op.NewGetZoneConfig()
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"zone": meta.CapAll})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.Params.Name).To(Equal("z1"))
		Expect(op.Run(ctx, op.NewGetZoneConfig(), &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead})})).To(MatchError(op.ErrAccessDenied))
	})
	It("names itself and asks for no action and no op-mask bit", func() {
		Expect((&op.GetInfo{}).Name()).To(Equal("get_info"))
		Expect((&op.GetZoneConfig{}).Name()).To(Equal("get_zone_config"))
		Expect((&op.GetInfo{}).Action()).To(Equal(policy.ActionNone))
		Expect((&op.GetInfo{}).OpMask()).To(BeZero())
	})
	It("logs no usage: radosgw registers the admin API without set_logging", func(ctx SpecContext) {
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead, "zone": meta.CapRead})}
		Expect(op.Run(ctx, &op.GetInfo{}, r)).To(Succeed())
		Expect(op.Run(ctx, &op.GetZoneConfig{}, r)).To(Succeed())
		Expect(store.Usage()).To(BeEmpty())
	})
})
