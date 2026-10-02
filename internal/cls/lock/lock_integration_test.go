//go:build integration

package lock_test

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// haveErrno matches a *radosclient.Error carrying errno n, for the errnos
// the seam has no sentinel for.
func haveErrno(n syscall.Errno) types.GomegaMatcher {
	return WithTransform(func(err error) int32 {
		e, ok := errors.AsType[*radosclient.Error](err)
		if !ok {
			return -1
		}
		return e.Errno
	}, Equal(int32(n)))
}

var _ = Describe("lock against a cluster", Label("integration"), func() {
	const (
		name = "RGWCompleteMultipart"
		rel  = denc.Squid
	)
	var (
		cluster radosclient.Cluster
		pool    radosclient.Pool
	)

	BeforeEach(func(ctx SpecContext) {
		var err error
		cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: cephtest.Conf()})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		pool, err = cluster.Pool(ctx, cephtest.TestPool, "")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
	})

	// scratch names an object the spec removes when it ends, whether or not
	// it then exists.
	scratch := func(what string) string {
		oid := fmt.Sprintf("cls-lock-%s-%d", what, time.Now().UnixNano())
		DeferCleanup(func(ctx SpecContext) {
			w := radosclient.NewWriteOp()
			w.Remove()
			_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
			Expect(err).To(Or(Succeed(), MatchError(radosclient.ErrNotFound)))
		})
		return oid
	}
	write := func(ctx SpecContext, oid string, build func(*radosclient.WriteOp)) error {
		w := radosclient.NewWriteOp()
		build(w)
		_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
		return err
	}
	read := func(ctx SpecContext, oid string, build func(*radosclient.ReadOp)) error {
		r := radosclient.NewReadOp()
		build(r)
		_, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone)
		return err
	}

	It("takes, refuses, reports, asserts, breaks and releases the RGWCompleteMultipart lock as the class does", func(ctx SpecContext) {
		oid := scratch("mp")
		lockExisting := func(cookie string) error {
			return write(ctx, oid, func(w *radosclient.WriteOp) { lock.LockExisting(w, name, cookie, "", time.Minute, rel) })
		}

		Expect(lockExisting("")).To(MatchError(radosclient.ErrNotFound), "assert_exists on a missing object")
		Expect(read(ctx, oid, func(r *radosclient.ReadOp) { r.Stat() })).
			To(MatchError(radosclient.ErrNotFound), "the assertion kept the lock from creating the object")

		Expect(write(ctx, oid, func(w *radosclient.WriteOp) { w.WriteFull([]byte("x")) })).To(Succeed())
		Expect(lockExisting("")).To(Succeed())
		Expect(lockExisting("")).To(MatchError(radosclient.ErrExists), "the same client and cookie again: EEXIST, lock_obj")
		Expect(lockExisting("other")).To(haveErrno(syscall.EBUSY), "another cookie on an exclusive lock: EBUSY")

		Expect(read(ctx, oid, func(r *radosclient.ReadOp) { lock.AssertLocked(r, name, lock.TypeExclusive, "", "", rel) })).To(Succeed())
		Expect(read(ctx, oid, func(r *radosclient.ReadOp) { lock.AssertLocked(r, name, lock.TypeExclusive, "other", "", rel) })).
			To(haveErrno(syscall.EBUSY))
		Expect(read(ctx, oid, func(r *radosclient.ReadOp) { lock.AssertLocked(r, name, lock.TypeShared, "", "", rel) })).
			To(haveErrno(syscall.EBUSY), "the wrong type")

		lockers, err := pool.ListLockers(ctx, oid, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lockers).To(HaveLen(1))
		Expect(lockers[0].Cookie).To(BeEmpty())
		holder, ok := lock.ParseEntityName(lockers[0].Client)
		Expect(ok).To(BeTrue(), lockers[0].Client)
		Expect(holder).To(Equal(lock.EntityName{Type: lock.EntityTypeClient, Num: int64(cluster.InstanceID())}), //nolint:gosec // a global id fits an i64
			"the holder is client.<global id>")

		var res *lock.InfoResult
		var raw *radosclient.ExecResult
		Expect(read(ctx, oid, func(r *radosclient.ReadOp) {
			res = lock.GetInfo(r, name, rel)
			raw = r.Exec(lock.Class, "get_info", encode(lock.GetInfoOp{Name: name}))
		})).To(Succeed())
		info, err := res.Info()
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Type).To(Equal(lock.TypeExclusive))
		Expect(info.Tag).To(BeEmpty())
		Expect(info.Lockers).To(HaveLen(1))
		Expect(info.Lockers).To(HaveKey(lock.LockerID{Locker: holder}))
		li := info.Lockers[lock.LockerID{Locker: holder}]
		Expect(li.Expiration).To(BeTemporally("~", time.Now().Add(time.Minute), 30*time.Second))
		Expect(li.Addr).NotTo(BeEmpty())
		Expect(li.Addr[0]).To(Equal(byte(1)), "entity_addr_t's msgr2-era marker")
		b, err := raw.Bytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(encode(info)).To(Equal(b), "the reply re-encodes byte for byte")

		Expect(write(ctx, oid, func(w *radosclient.WriteOp) { lock.BreakLock(w, name, holder, "", rel) })).To(Succeed())
		Expect(write(ctx, oid, func(w *radosclient.WriteOp) { lock.Unlock(w, name, "", rel) })).
			To(MatchError(radosclient.ErrNotFound), "nothing left to unlock")
		Expect(lockExisting("")).To(Succeed(), "a broken lock can be taken again")
		Expect(write(ctx, oid, func(w *radosclient.WriteOp) { lock.Unlock(w, name, "", rel) })).To(Succeed())
	})

	It("creates a missing object when the lock is sent without assert_exists", func(ctx SpecContext) {
		oid := scratch("bare")
		Expect(write(ctx, oid, func(w *radosclient.WriteOp) {
			lock.Lock(w, lock.LockOp{Name: name, Type: lock.TypeExclusive, Duration: time.Minute}, rel)
		})).To(Succeed())
		Expect(read(ctx, oid, func(r *radosclient.ReadOp) { r.Stat() })).To(Succeed())
	})
})
