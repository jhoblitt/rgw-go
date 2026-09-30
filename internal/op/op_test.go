package op_test

import (
	"context"
	"errors"
	"fmt"
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
	It("denies a user whose op mask lacks the op's type once verify has run", func(ctx SpecContext) {
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
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
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
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
	DescribeTable("lets an admin identity through an access denial",
		func(ctx SpecContext, permErr error) {
			rec.permErr = permErr
			req.Identity.Admin = true
			Expect(op.Run(ctx, rec, req)).To(Succeed())
			Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
		},
		Entry("AccessDenied from EACCES", op.ErrAccessDenied),
		Entry("AccessDenied from EPERM", &op.Error{Code: "AccessDenied", Status: 403, Errno: 1}),
		Entry("AuthorizationError from ERR_AUTHORIZATION", op.ErrAuthorization),
		Entry("a wrapped AccessDenied carrying a message", fmt.Errorf("evaluating: %w", op.ErrAccessDenied.WithMessage("denied"))),
	)
	DescribeTable("returns every other verify error to an admin identity",
		func(ctx SpecContext, permErr error) {
			rec.permErr = permErr
			req.Identity.Admin = true
			Expect(op.Run(ctx, rec, req)).To(MatchError(permErr))
			Expect(rec.calls).To(Equal([]string{"init", "verify"}))
		},
		Entry("QuotaExceeded", op.ErrQuotaExceeded),
		Entry("NoSuchKey", op.ErrNoSuchKey),
		Entry("InvalidArgument", op.ErrInvalidArgument),
		Entry("ERR_MFA_REQUIRED, although it shares AccessDenied/403 with EACCES", op.ErrMFARequired),
		Entry("an error carrying no *Error", errors.New("boom")),
	)
	It("lets the op mask decide over an unmarked verify error", func(ctx SpecContext) {
		rec.permErr = op.ErrNoSuchKey
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
	})
	It("returns a BeforeVerify refusal to an admin identity", func(ctx SpecContext) {
		rec.permErr = op.BeforeVerify(op.ErrUserSuspended)
		req.Identity.Admin = true
		err := op.Run(ctx, rec, req)
		Expect(err).To(MatchError(op.ErrUserSuspended))
		Expect(op.IsBeforeVerify(err)).To(BeTrue(), "the mark on %v", err)
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
	})
	It("returns a BeforeVerify refusal ahead of a failing op mask", func(ctx SpecContext) {
		rec.permErr = op.BeforeVerify(op.ErrUserSuspended)
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrUserSuspended))
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
	})
	It("never lets an admin identity through a BeforeVerify access denial", func(ctx SpecContext) {
		rec.permErr = op.BeforeVerify(op.ErrAccessDenied)
		req.Identity.Admin = true
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
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

var _ = Describe("BeforeVerify", func() {
	It("leaves nil unmarked", func() {
		Expect(op.BeforeVerify(nil)).To(Succeed())
		Expect(op.IsBeforeVerify(nil)).To(BeFalse())
	})
	It("keeps the refusal it marks, so the refusal renders unchanged", func() {
		refusal := op.ErrUserSuspended.WithMessage("suspended")
		marked := op.BeforeVerify(refusal)
		Expect(marked).To(MatchError(op.ErrUserSuspended))
		Expect(marked).To(MatchError(refusal.Error()))
		Expect(op.AsError(marked)).To(BeIdenticalTo(refusal))
	})
	It("is found through further wrapping", func() {
		err := fmt.Errorf("verifying: %w", op.BeforeVerify(op.ErrAccessDenied))
		Expect(op.IsBeforeVerify(err)).To(BeTrue(), "the mark on %v", err)
	})
	It("is absent from an unmarked refusal", func() {
		Expect(op.IsBeforeVerify(op.ErrUserSuspended)).To(BeFalse(), "bare")
		Expect(op.IsBeforeVerify(fmt.Errorf("verifying: %w", op.ErrUserSuspended))).To(BeFalse(), "wrapped")
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
