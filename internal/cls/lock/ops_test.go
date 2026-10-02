package lock_test

import (
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// execOf asserts step is an exec of lock.method and returns it.
func execOf(step radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	ex, ok := step.(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", step)
	Expect([]string{ex.Class, ex.Method}).To(Equal([]string{"lock", method}))
	return ex
}

var _ = Describe("lock ops", func() {
	It("composes LockExisting as assert_exists then lock.lock in one write op", func() {
		op := radosclient.NewWriteOp()
		lock.LockExisting(op, "RGWCompleteMultipart", "", "", 600*time.Second, denc.Squid)
		steps := op.Steps()
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		d := denc.NewDecoder(execOf(steps[1], "lock").In)
		got := lock.DecodeLockOp(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second}))
	})

	It("passes LockExisting's cookie and description, and takes an exclusive lock without flags", func() {
		op := radosclient.NewWriteOp()
		lock.LockExisting(op, "n", "c", "d", time.Minute, denc.Squid)
		in := execOf(op.Steps()[1], "lock").In
		Expect(in).To(Equal(encode(lock.LockOp{Name: "n", Type: lock.TypeExclusive, Cookie: "c", Description: "d", Duration: time.Minute})))
	})

	It("sends Lock's request as given", func() {
		p := lock.LockOp{Name: "n", Type: lock.TypeShared, Cookie: "c", Tag: "t", Description: "d", Duration: time.Second, Flags: lock.FlagMayRenew}
		op := radosclient.NewWriteOp()
		lock.Lock(op, p, denc.Squid)
		Expect(op.Steps()).To(HaveLen(1))
		Expect(execOf(op.Steps()[0], "lock").In).To(Equal(encode(p)))
	})

	It("names the class methods Squid's class registers and sends their requests", func() {
		op := radosclient.NewWriteOp()
		holder := lock.EntityName{Type: lock.EntityTypeClient, Num: 1}
		lock.Unlock(op, "n", "c", denc.Squid)
		lock.BreakLock(op, "n", holder, "c", denc.Squid)
		lock.AssertLocked(op, "n", lock.TypeExclusive, "c", "t", denc.Squid)
		steps := op.Steps()
		Expect(steps).To(HaveLen(3))
		Expect(execOf(steps[0], "unlock").In).To(Equal(encode(lock.UnlockOp{Name: "n", Cookie: "c"})))
		Expect(execOf(steps[1], "break_lock").In).To(Equal(encode(lock.BreakOp{Name: "n", Locker: holder, Cookie: "c"})))
		Expect(execOf(steps[2], "assert_locked").In).To(Equal(encode(lock.AssertOp{Name: "n", Type: lock.TypeExclusive, Cookie: "c", Tag: "t"})))
		Expect(lock.Class).To(Equal("lock"))
	})

	Describe("GetInfo", func() {
		var (
			rop *radosclient.ReadOp
			res *lock.InfoResult
		)
		BeforeEach(func() {
			rop = radosclient.NewReadOp()
			res = lock.GetInfo(rop, "n", denc.Squid)
		})
		step := func() *radosclient.ExecStep {
			GinkgoHelper()
			Expect(rop.Steps()).To(HaveLen(1))
			return execOf(rop.Steps()[0], "get_info")
		}

		It("sends cls_lock_get_info_op and decodes the reply", func() {
			Expect(step().In).To(Equal(encode(lock.GetInfoOp{Name: "n"})))
			step().Result.Set([]byte{1, 1, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0) // no lockers, type NONE, tag ""
			info, err := res.Info()
			Expect(err).NotTo(HaveOccurred())
			Expect(info).To(Equal(lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}, Type: lock.TypeNone}))
		})

		It("surfaces the op's error from Info", func() {
			step().Result.Set(nil, -int32(syscall.ENOENT))
			_, err := res.Info()
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})

		It("names the class and method when the reply does not decode", func() {
			step().Result.Set([]byte{1, 1, 4, 0, 0, 0}, 0)
			_, err := res.Info()
			Expect(err).To(MatchError(denc.ErrShortBuffer))
			Expect(err).To(MatchError(HavePrefix("lock: decoding get_info reply: ")))
		})

		It("reports a reply read before the op ran", func() {
			_, err := res.Info()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
		})
	})
})
