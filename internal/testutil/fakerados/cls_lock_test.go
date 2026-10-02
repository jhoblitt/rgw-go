package fakerados_test

import (
	"context"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("LockClass", func() {
	const name = "RGWCompleteMultipart"
	var (
		c   *fakerados.Cluster
		p   radosclient.Pool
		now time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		c.RegisterClass("lock", fakerados.LockClass(), fakerados.LockWriteMethods...)
		now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return now })
	})
	stored := func(oid string) *fakerados.Object { return c.Object(poolName, ns, oid) }
	us := func(cookie string) lock.LockerID { return lock.LockerID{Locker: fakerados.ClientName, Cookie: cookie} }
	take := func(ctx context.Context, oid string, o lock.LockOp) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { lock.Lock(op, o, denc.Squid) })
	}
	exclusive := func(cookie string, flags lock.Flags) lock.LockOp {
		return lock.LockOp{Name: name, Type: lock.TypeExclusive, Cookie: cookie, Duration: time.Minute, Flags: flags}
	}
	info := func(ctx context.Context, oid string) (lock.Info, error) {
		op := radosclient.NewReadOp()
		res := lock.GetInfo(op, name, denc.Squid)
		if _, err := p.Read(ctx, oid, op, radosclient.OpFlagNone); err != nil {
			return lock.Info{}, err
		}
		return res.Info()
	}
	assert := func(ctx context.Context, oid string, typ lock.Type, cookie, tag string) error {
		op := radosclient.NewReadOp()
		lock.AssertLocked(op, name, typ, cookie, tag, denc.Squid)
		_, err := p.Read(ctx, oid, op, radosclient.OpFlagNone)
		return err
	}

	It("names the client by the global id the cluster reports", func() {
		Expect(fakerados.ClientName).To(Equal(lock.EntityName{Type: lock.EntityTypeClient, Num: int64(c.InstanceID())}))
	})

	It("names the emulated methods that write, so get_info and assert_locked cannot", func() {
		Expect(fakerados.LockWriteMethods).To(ConsistOf("lock", "unlock", "break_lock"), "CLS_METHOD_WR, cls_lock.cc:625-633")
	})

	It("creates a missing object and records the holder in the lock's xattr", func(ctx SpecContext) {
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeExclusive, Description: "d", Duration: time.Minute})).To(Succeed())
		Expect(stored("meta")).NotTo(BeNil())
		Expect(stored("meta").Xattrs).To(HaveKey("lock." + name))
		d := denc.NewDecoder(stored("meta").Xattrs["lock."+name])
		got := lock.DecodeInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(got.Type).To(Equal(lock.TypeExclusive))
		Expect(got.Lockers).To(HaveLen(1))
		li := got.Lockers[us("")]
		Expect(li.Expiration).To(Equal(now.Add(time.Minute)))
		Expect(li.Description).To(Equal("d"))
		Expect(li.Addr).NotTo(BeEmpty())
		Expect(li.Addr[0]).To(Equal(byte(1)), "entity_addr_t's msgr2-era marker")
	})

	It("refuses LockExisting on a missing object without creating it", func(ctx SpecContext) {
		err := writeErr(ctx, p, "meta", func(op *radosclient.WriteOp) {
			lock.LockExisting(op, name, "", "", time.Minute, denc.Squid)
		})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(stored("meta")).To(BeNil())
	})

	It("answers the same holder again with EEXIST, and renews it under MayRenew", func(ctx SpecContext) {
		c.Put(poolName, ns, "meta", []byte("d"))
		Expect(take(ctx, "meta", exclusive("", 0))).To(Succeed())
		Expect(take(ctx, "meta", exclusive("", 0))).To(MatchError(radosclient.ErrExists), "lock_obj, cls_lock.cc:190-199")
		now = now.Add(30 * time.Second)
		Expect(take(ctx, "meta", exclusive("", lock.FlagMayRenew))).To(Succeed())
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Lockers[us("")].Expiration).To(Equal(now.Add(time.Minute)), "the renewal restarts the duration")
		Expect(take(ctx, "meta", exclusive("", lock.FlagMustRenew))).To(Succeed())
	})

	It("answers another holder of an exclusive lock with EBUSY", func(ctx SpecContext) {
		Expect(take(ctx, "meta", exclusive("", 0))).To(Succeed())
		Expect(take(ctx, "meta", exclusive("other", 0))).To(haveErrno(syscall.EBUSY))
		Expect(take(ctx, "meta", exclusive("other", lock.FlagMayRenew))).To(haveErrno(syscall.EBUSY))
	})

	It("shares a shared lock under one tag and refuses another tag or type with EBUSY", func(ctx SpecContext) {
		shared := func(cookie, tag string) lock.LockOp {
			return lock.LockOp{Name: name, Type: lock.TypeShared, Cookie: cookie, Tag: tag}
		}
		Expect(take(ctx, "meta", shared("a", "t"))).To(Succeed())
		Expect(take(ctx, "meta", shared("b", "t"))).To(Succeed())
		Expect(take(ctx, "meta", shared("c", "u"))).To(haveErrno(syscall.EBUSY), "another tag, cls_lock.cc:183-186")
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeExclusive, Cookie: "c", Tag: "t"})).To(haveErrno(syscall.EBUSY))
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{got.Type, got.Tag}).To(Equal([]any{lock.TypeShared, "t"}))
		Expect(got.Lockers).To(HaveLen(2))
		Expect(got.Lockers).To(HaveKey(us("a")))
		Expect(got.Lockers).To(HaveKey(us("b")))
	})

	It("checks the tag before the holder, so even the holder's renewal under another tag is EBUSY", func(ctx SpecContext) {
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeShared, Tag: "t"})).To(Succeed())
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeShared, Tag: "u", Flags: lock.FlagMayRenew})).
			To(haveErrno(syscall.EBUSY), "cls_lock.cc:181-186")
	})

	It("answers MustRenew without a hold with ENOENT", func(ctx SpecContext) {
		Expect(take(ctx, "meta", exclusive("", lock.FlagMustRenew))).To(MatchError(radosclient.ErrNotFound))
	})

	DescribeTable("refuses a request lock_obj cannot take with EINVAL",
		func(ctx SpecContext, o lock.LockOp) {
			Expect(take(ctx, "meta", o)).To(MatchError(radosclient.ErrInvalid))
			Expect(stored("meta")).To(BeNil())
		},
		Entry("both renew flags", lock.LockOp{Name: name, Type: lock.TypeExclusive, Flags: lock.FlagMayRenew | lock.FlagMustRenew}),
		Entry("type NONE", lock.LockOp{Name: name, Type: lock.TypeNone}),
		Entry("a type the class does not define", lock.LockOp{Name: name, Type: 4}),
		Entry("no name", lock.LockOp{Type: lock.TypeExclusive}),
	)

	It("unlocks the holder once, then answers ENOENT", func(ctx SpecContext) {
		Expect(take(ctx, "meta", exclusive("", 0))).To(Succeed())
		unlock := func() error {
			return writeErr(ctx, p, "meta", func(op *radosclient.WriteOp) { lock.Unlock(op, name, "", denc.Squid) })
		}
		Expect(unlock()).To(Succeed())
		Expect(unlock()).To(MatchError(radosclient.ErrNotFound))
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Lockers).To(BeEmpty())
		Expect(stored("meta")).NotTo(BeNil(), "a lock that is not ephemeral keeps its object")
		Expect(take(ctx, "meta", exclusive("other", 0))).To(Succeed())
	})

	It("breaks the lock of the holder it names, and only that holder's", func(ctx SpecContext) {
		Expect(take(ctx, "meta", exclusive("c", 0))).To(Succeed())
		brk := func(locker lock.EntityName, cookie string) error {
			return writeErr(ctx, p, "meta", func(op *radosclient.WriteOp) { lock.BreakLock(op, name, locker, cookie, denc.Squid) })
		}
		Expect(brk(lock.EntityName{Type: lock.EntityTypeClient, Num: 1}, "c")).To(MatchError(radosclient.ErrNotFound))
		Expect(brk(fakerados.ClientName, "other")).To(MatchError(radosclient.ErrNotFound))
		Expect(brk(fakerados.ClientName, "c")).To(Succeed())
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Lockers).To(BeEmpty())
	})

	It("lists the holder, the type and the tag through get_info", func(ctx SpecContext) {
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeShared, Cookie: "c", Tag: "t", Description: "d"})).To(Succeed())
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Type).To(Equal(lock.TypeShared))
		Expect(got.Tag).To(Equal("t"))
		Expect(got.Lockers).To(HaveLen(1))
		Expect(got.Lockers[us("c")].Expiration.IsZero()).To(BeTrue(), "a zero duration never expires")
		Expect(got.Lockers[us("c")].Description).To(Equal("d"))
	})

	It("answers get_info on a missing object with ENOENT and on an unlocked one with an empty lock", func(ctx SpecContext) {
		_, err := info(ctx, "meta")
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		c.Put(poolName, ns, "meta", []byte("d"))
		Expect(info(ctx, "meta")).To(Equal(lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}, Type: lock.TypeNone}))
	})

	It("drops an expired holder on the next read, so another can take the lock", func(ctx SpecContext) {
		Expect(take(ctx, "meta", exclusive("", 0))).To(Succeed())
		now = now.Add(time.Minute)
		Expect(take(ctx, "meta", exclusive("other", 0))).To(haveErrno(syscall.EBUSY), "not yet past the expiration")
		now = now.Add(time.Nanosecond)
		got, err := info(ctx, "meta")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Lockers).To(BeEmpty())
		Expect(got.Type).To(Equal(lock.TypeExclusive), "the trimmed lock keeps its type")
		Expect(take(ctx, "meta", exclusive("other", 0))).To(Succeed())
	})

	It("asserts the holder's lock and answers anything else with EBUSY", func(ctx SpecContext) {
		c.Put(poolName, ns, "meta", []byte("d"))
		Expect(assert(ctx, "meta", lock.TypeExclusive, "", "")).To(haveErrno(syscall.EBUSY), "no holder")
		Expect(take(ctx, "meta", lock.LockOp{Name: name, Type: lock.TypeExclusive, Cookie: "c", Tag: "t"})).To(Succeed())
		Expect(assert(ctx, "meta", lock.TypeExclusive, "c", "t")).To(Succeed())
		Expect(assert(ctx, "meta", lock.TypeExclusive, "other", "t")).To(haveErrno(syscall.EBUSY), "the wrong cookie")
		Expect(assert(ctx, "meta", lock.TypeShared, "c", "t")).To(haveErrno(syscall.EBUSY), "the wrong type")
		Expect(assert(ctx, "meta", lock.TypeExclusive, "c", "u")).To(haveErrno(syscall.EBUSY), "the wrong tag")
		Expect(assert(ctx, "meta", lock.TypeNone, "c", "t")).To(MatchError(radosclient.ErrInvalid))
	})

	Describe("an ephemeral lock", func() {
		ephemeral := func() lock.LockOp {
			return lock.LockOp{Name: name, Type: lock.TypeExclusiveEphemeral, Duration: time.Minute}
		}
		BeforeEach(func(ctx SpecContext) {
			c.Put(poolName, ns, "meta", []byte("d"))
			Expect(take(ctx, "meta", ephemeral())).To(Succeed())
		})

		It("removes the object with its last holder", func(ctx SpecContext) {
			Expect(writeErr(ctx, p, "meta", func(op *radosclient.WriteOp) { lock.Unlock(op, name, "", denc.Squid) })).To(Succeed())
			Expect(stored("meta")).To(BeNil(), "remove_lock, cls_lock.cc:307-309")
		})

		It("fails get_info and assert_locked with EIO once every holder expired, and keeps the object (tracker #80993)", func(ctx SpecContext) {
			now = now.Add(2 * time.Minute)
			_, err := info(ctx, "meta")
			Expect(err).To(haveErrno(syscall.EIO))
			Expect(assert(ctx, "meta", lock.TypeExclusiveEphemeral, "", "")).To(haveErrno(syscall.EIO))
			Expect(stored("meta").Data).To(Equal([]byte("d")), "the OSD drops the removal with the failed read")
		})

		It("recreates the object, holding only the lock, when it is taken again after expiring", func(ctx SpecContext) {
			now = now.Add(2 * time.Minute)
			Expect(take(ctx, "meta", ephemeral())).To(Succeed())
			Expect(stored("meta").Data).To(BeEmpty(), "read_lock removed it before write_lock set the xattr")
			Expect(stored("meta").Xattrs).To(HaveKey("lock." + name))
		})
	})

	It("fails a request that does not decode with EINVAL", func(ctx SpecContext) {
		for _, method := range []string{"lock", "unlock", "break_lock"} {
			err := writeErr(ctx, p, "meta", func(op *radosclient.WriteOp) { op.Exec("lock", method, []byte{1}) })
			Expect(err).To(MatchError(radosclient.ErrInvalid), method)
		}
		c.Put(poolName, ns, "meta", []byte("d"))
		for _, method := range []string{"get_info", "assert_locked"} {
			op := radosclient.NewReadOp()
			op.Exec("lock", method, []byte{1})
			_, err := p.Read(ctx, "meta", op, radosclient.OpFlagNone)
			Expect(err).To(MatchError(radosclient.ErrInvalid), method)
		}
	})

	It("fails with EIO on a lock xattr that does not decode", func(ctx SpecContext) {
		c.Put(poolName, ns, "meta", []byte("d"))
		stored("meta").Xattrs["lock."+name] = []byte{1}
		_, err := info(ctx, "meta")
		Expect(err).To(haveErrno(syscall.EIO))
		Expect(take(ctx, "meta", exclusive("", 0))).To(haveErrno(syscall.EIO))
	})
})
