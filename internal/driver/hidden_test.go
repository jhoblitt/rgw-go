package driver_test

import (
	"context"
	"net/http/httptest"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The access key, email and Swift key indexes name their objects by the
// credential or the email itself, so neither the store's errors nor any log
// line their failures reach may name those objects by id.
var _ = Describe("Credential index objects", func() {
	const rookControlPool = "ceph-objectstore.rgw.control"
	var (
		c    *fakerados.Cluster
		s    *driver.Store
		logs *syncBuffer
	)
	BeforeEach(func(ctx SpecContext) {
		logs = &syncBuffer{}
		DeferCleanup(driver.CaptureLog(logs))
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
	})
	user := func(id, email string) meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: id}
		u.DisplayName, u.Email = id, email
		return u
	}
	put := func(ctx context.Context, info meta.UserInfo, exclusive bool) error {
		return s.PutUser(ctx, &op.UserRecord{Info: info}, op.PutUserOptions{Exclusive: exclusive})
	}
	canceled := func(ctx context.Context) context.Context {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		return cctx
	}
	// expectHidden renders err as the S3 and admin handlers' WriteError do,
	// which log a server error with its cause, and asserts that none of
	// secrets appears in err or in any log line.
	expectHidden := func(ctx context.Context, err error, secrets ...string) {
		GinkgoHelper()
		Expect(err).To(HaveOccurred())
		r := &op.Request{ID: "tx1", Env: &op.Env{HostID: "h"}}
		s3.WriteError(ctx, httptest.NewRecorder(), r, err)
		admin.WriteError(ctx, httptest.NewRecorder(), r, admin.Request{}, err)
		for _, secret := range secrets {
			Expect(err.Error()).NotTo(ContainSubstring(secret), "the error")
			Expect(logs.String()).NotTo(ContainSubstring(secret), "the log")
		}
	}

	DescribeTable("names a credential index object by its pool and kind when an operation on it fails",
		func(ctx SpecContext, run func(ctx context.Context) error, kind string, matches error, secrets ...string) {
			err := run(ctx)
			Expect(err).To(MatchError(matches), "the error still matches its cause")
			Expect(err.Error()).To(ContainSubstring("ceph-objectstore.rgw.meta:users."))
			Expect(err.Error()).To(ContainSubstring("(" + kind + ")"))
			expectHidden(ctx, err, secrets...)
		},
		Entry("a failed lookup by access key", func(ctx context.Context) error {
			c.FailNextRead(rookMetaPool, "users.keys", "AKHIDDEN1", syscall.EIO)
			_, err := s.GetUserByAccessKey(ctx, "AKHIDDEN1")
			return err
		}, radosclient.KindUserKeyIndex, op.ErrUnknown, "AKHIDDEN1"),
		Entry("a canceled lookup by access key", func(ctx context.Context) error {
			_, err := s.GetUserByAccessKey(ctx, "AKWARMUP") // opens the pool, so the read meets the cancellation
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			_, err = s.GetUserByAccessKey(canceled(ctx), "AKHIDDEN1")
			return err
		}, radosclient.KindUserKeyIndex, context.Canceled, "AKHIDDEN1"),
		Entry("a failed lookup by email", func(ctx context.Context) error {
			c.FailNextRead(rookMetaPool, "users.email", "hidden@example.com", syscall.EIO)
			_, err := s.GetUserByEmail(ctx, "Hidden@Example.com")
			return err
		}, radosclient.KindUserEmailIndex, op.ErrUnknown, "hidden@example.com", "Hidden@Example.com"),
		Entry("a canceled lookup by email, an account's email redirect among them", func(ctx context.Context) error {
			_, err := s.GetUserByEmail(ctx, "warmup@example.com") // opens the pool, so the read meets the cancellation
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			_, err = s.GetUserByEmail(canceled(ctx), "Hidden@Example.com")
			return err
		}, radosclient.KindUserEmailIndex, context.Canceled, "hidden@example.com", "Hidden@Example.com"),
		Entry("a failed access key index write", func(ctx context.Context) error {
			u := user("bob", "")
			u.AccessKeys = map[string]meta.AccessKey{"AKHIDDEN2": {ID: "AKHIDDEN2", Secret: "s", Active: true}}
			c.FailNextWrite(rookMetaPool, "users.keys", "AKHIDDEN2", syscall.EIO)
			return put(ctx, u, true)
		}, radosclient.KindUserKeyIndex, op.ErrUnknown, "AKHIDDEN2"),
		Entry("a failed email index write", func(ctx context.Context) error {
			c.FailNextWrite(rookMetaPool, "users.email", "carol.hidden@example.com", syscall.EIO)
			return put(ctx, user("carol", "Carol.Hidden@Example.com"), true)
		}, radosclient.KindUserEmailIndex, op.ErrUnknown, "carol.hidden@example.com", "Carol.Hidden@Example.com"),
		Entry("a failed Swift key index write", func(ctx context.Context) error {
			u := user("dave", "")
			u.SwiftKeys = map[string]meta.AccessKey{"dave:hidden": {ID: "dave:hidden", Secret: "s", Active: true}}
			c.FailNextWrite(rookMetaPool, "users.swift", "dave:hidden", syscall.EIO)
			return put(ctx, u, true)
		}, radosclient.KindUserSwiftIndex, op.ErrUnknown, "dave:hidden"),
		Entry("a failed removal of an old email's index", func(ctx context.Context) error {
			Expect(put(ctx, user("erin", "Erin.Old@Example.com"), true)).To(Succeed())
			c.FailNextWrite(rookMetaPool, "users.email", "erin.old@example.com", syscall.EIO)
			return put(ctx, user("erin", "erin.new@example.com"), false)
		}, radosclient.KindUserEmailIndex, op.ErrUnknown, "erin.old@example.com", "Erin.Old@Example.com"),
	)

	DescribeTable("names a credential index object by its kind in the errors of a lookup through it",
		func(ctx SpecContext, run func(ctx context.Context) error, matches error, secrets ...string) {
			err := run(ctx)
			Expect(err).To(MatchError(matches))
			Expect(err.Error()).To(ContainSubstring("(" + radosclient.KindUserKeyIndex + ")"))
			expectHidden(ctx, err, secrets...)
		},
		Entry("a key whose index names an account", func(ctx context.Context) error {
			c.Put(rookMetaPool, "users.keys", "AKACCOUNT", encode(meta.UID("RGW00000000000000009")))
			_, err := s.GetUserByAccessKey(ctx, "AKACCOUNT")
			return err
		}, op.ErrNoSuchUser, "AKACCOUNT"),
		Entry("a key whose index does not decode, an InternalError WriteError logs", func(ctx context.Context) error {
			c.Put(rookMetaPool, "users.keys", "AKGARBLED", []byte{1})
			_, err := s.GetUserByAccessKey(ctx, "AKGARBLED")
			return err
		}, op.ErrInternalError, "AKGARBLED"),
	)

	It("names a credential index object by its kind when the cache answers a lookup through it as missing", func(ctx SpecContext) {
		// The cache serves once every control watch is up; the watches
		// outlive this node, so they run on a context its end does not
		// cancel.
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive())
		})
		var err error
		Eventually(func(g Gomega) {
			_, err = s.GetUserByAccessKey(ctx, "AKNEGATIVE")
			g.Expect(err).To(MatchError(op.ErrNoSuchUser))
			n := c.Reads(rookMetaPool, "users.keys", "AKNEGATIVE")
			_, err = s.GetUserByAccessKey(ctx, "AKNEGATIVE")
			g.Expect(err).To(MatchError(op.ErrNoSuchUser))
			g.Expect(c.Reads(rookMetaPool, "users.keys", "AKNEGATIVE")).To(Equal(n), "the second lookup is served from the cache")
		}).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Succeed())
		Expect(err.Error()).To(ContainSubstring("(" + radosclient.KindUserKeyIndex + ")"))
		expectHidden(ctx, err, "AKNEGATIVE")
	})

	It("refuses a key another user holds without naming the key", func(ctx SpecContext) {
		holder := user("heidi", "")
		holder.AccessKeys = map[string]meta.AccessKey{"AKTAKEN": {ID: "AKTAKEN", Secret: "s", Active: true}}
		Expect(put(ctx, holder, true)).To(Succeed())
		taker := user("ivan", "")
		taker.AccessKeys = holder.AccessKeys
		err := put(ctx, taker, true)
		Expect(err).To(MatchError(op.ErrKeyExists))
		expectHidden(ctx, err, "AKTAKEN")
	})

	It("logs a credential index object's timed-out cache notify by its kind", func(ctx SpecContext) {
		for i := range 8 {
			c.FailNotify(rookControlPool, "", "notify."+strconv.Itoa(i), 100, radosclient.ErrTimedOut)
		}
		u := user("judy", "")
		u.AccessKeys = map[string]meta.AccessKey{"AKRETRIED": {ID: "AKRETRIED", Secret: "s", Active: true}}
		Expect(put(ctx, u, true)).To(Succeed(), "a failed notify does not fail the change")
		Expect(logs.String()).To(ContainSubstring(`"msg":"control notify timed out, sending an invalidation"`))
		Expect(logs.String()).To(ContainSubstring(`(user key index)`))
		Expect(logs.String()).NotTo(ContainSubstring("AKRETRIED"))
	})

	It("logs a credential index object's failed cache notify by its kind", func(ctx SpecContext) {
		for i := range 8 {
			c.FailNotify(rookControlPool, "", "notify."+strconv.Itoa(i), 100, &radosclient.Error{Errno: int32(syscall.EIO)})
		}
		u := user("frank", "")
		u.AccessKeys = map[string]meta.AccessKey{"AKHIDDEN3": {ID: "AKHIDDEN3", Secret: "s", Active: true}}
		Expect(put(ctx, u, true)).To(Succeed(), "a failed notify does not fail the change")
		Expect(logs.String()).To(ContainSubstring(`"kind":"user key index"`))
		Expect(logs.String()).NotTo(ContainSubstring("AKHIDDEN3"))
	})

	It("still names every other object by its id", func(ctx SpecContext) {
		c.FailNextRead(rookMetaPool, "users.uid", "grace", syscall.EIO)
		_, err := s.GetUser(ctx, meta.UserID{ID: "grace"})
		Expect(err).To(MatchError(ContainSubstring("ceph-objectstore.rgw.meta:users.uid/grace")))
	})
})
