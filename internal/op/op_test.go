package op_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

type recorder struct {
	calls                     []string
	mask                      uint32
	initErr, permErr, execErr error
}

func (r *recorder) Name() string          { return "recorder" }
func (r *recorder) Action() policy.Action { return policy.S3GetObject }
func (r *recorder) OpMask() uint32        { return r.mask }
func (r *recorder) Init(context.Context, *op.Request) error {
	r.calls = append(r.calls, "init")
	return r.initErr
}

func (r *recorder) VerifyPermission(context.Context, *op.Request) error {
	r.calls = append(r.calls, "verify")
	return r.permErr
}

func (r *recorder) Execute(context.Context, *op.Request) error {
	r.calls = append(r.calls, "execute")
	return r.execErr
}
func (r *recorder) Complete(context.Context, *op.Request) { r.calls = append(r.calls, "complete") }

var _ = Describe("Run", func() {
	var (
		rec  *recorder
		req  *op.Request
		zone *opfakes.FakeZoneInfo
	)
	BeforeEach(func() {
		rec = &recorder{mask: op.OpTypeRead}
		zone = &opfakes.FakeZoneInfo{}
		zone.ZoneReturns(meta.Zone{Name: "rw"})
		req = &op.Request{Identity: op.Anonymous(), Env: &op.Env{Zone: zone}}
	})
	It("runs init, verify, execute and complete in radosgw's order", func(ctx SpecContext) {
		Expect(op.Run(ctx, rec, req)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("stops at a failed init and never completes", func(ctx SpecContext) {
		rec.initErr = op.ErrNoSuchBucket
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrNoSuchBucket))
		Expect(rec.calls).To(Equal([]string{"init"}))
	})
	It("denies a user whose op mask lacks the op's type before verifying", func(ctx SpecContext) {
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init"}))
	})
	It("requires every bit of the op's mask", func(ctx SpecContext) {
		req.Identity.OpMask = op.OpTypeWrite
		rec.mask = op.OpTypeModify
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
	})
	It("denies a modifying op on a read-only zone for a non-system identity", func(ctx SpecContext) {
		zone.ZoneReturns(meta.Zone{Name: "ro", ReadOnly: true})
		rec.mask = op.OpTypeDelete
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init"}))
		req.Identity.System = true
		rec.calls = nil
		Expect(op.Run(ctx, rec, req)).To(Succeed(), "a system identity may write a read-only zone")
	})
	It("lets a read through a read-only zone", func(ctx SpecContext) {
		zone.ZoneReturns(meta.Zone{Name: "ro", ReadOnly: true})
		Expect(op.Run(ctx, rec, req)).To(Succeed())
	})
	It("lets an admin identity through a permission denial, as rgw_process_authenticated does", func(ctx SpecContext) {
		rec.permErr = op.ErrAccessDenied
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied), "plain identity")
		Expect(rec.calls).To(Equal([]string{"init", "verify"}), "plain identity")
		req.Identity.Admin = true
		rec.calls = nil
		Expect(op.Run(ctx, rec, req)).To(Succeed(), "admin identity")
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("lets an admin identity through any verify error, not only AccessDenied", func(ctx SpecContext) {
		// ErrQuotaExceeded, not ErrMFARequired: the latter shares AccessDenied/403 with
		// ErrAccessDenied and is indistinguishable from it under errors.Is.
		rec.permErr = op.ErrQuotaExceeded
		req.Identity.Admin = true
		Expect(op.Run(ctx, rec, req)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("never lets an admin identity past the op mask", func(ctx SpecContext) {
		req.Identity.Admin = true
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
	})
	It("completes after a failed execute and returns the execute error", func(ctx SpecContext) {
		rec.execErr = op.ErrQuotaExceeded
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrQuotaExceeded))
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
})

var _ = Describe("Env", func() {
	It("reads the injected clock", func() {
		at := time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
		Expect((&op.Env{Now: func() time.Time { return at }}).Clock()).To(Equal(at))
	})
	It("falls back to time.Now without one, even on a nil Env", func() {
		var env *op.Env
		Expect(env.Clock()).To(BeTemporally("~", time.Now(), time.Minute))
		Expect((&op.Env{}).Clock()).To(BeTemporally("~", time.Now(), time.Minute))
	})
})
