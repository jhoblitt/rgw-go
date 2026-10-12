package driver_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// landingCancelCluster is c with pools whose first write to oid reaches the
// OSD, landing or failing there, and then reports the context error cancel
// causes in place of its outcome, as a write does whose request context ends
// before its reply.
type landingCancelCluster struct {
	*fakerados.Cluster
	oid    string
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *landingCancelCluster) Pool(ctx context.Context, pool, ns string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, ns)
	if err != nil {
		return nil, err
	}
	return &landingCancelPool{Pool: p, c: c}, nil
}

// landingCancelPool is a pool of a landingCancelCluster.
type landingCancelPool struct {
	radosclient.Pool
	c *landingCancelCluster
}

func (p *landingCancelPool) Write(ctx context.Context, oid string, w *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if oid != p.c.oid || !p.c.fired.CompareAndSwap(false, true) {
		return p.Pool.Write(ctx, oid, w, flags)
	}
	_, _ = p.Pool.Write(context.WithoutCancel(ctx), oid, w, flags) //nolint:errcheck // the context error stands in for the outcome, which the caller never sees
	p.c.cancel()
	return 0, ctx.Err()
}

var _ = Describe("the multipart lock of a request whose client goes away", func() {
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		key    meta.ObjKey
		mtime  time.Time
		stores []*driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		stores = []*driver.Store{s}
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
	})
	AfterEach(func() {
		for _, st := range stores {
			settle(st)
		}
	})
	// upload creates an upload of k with one part and returns it with the
	// part's ETag.
	upload := func(ctx context.Context) (*op.Upload, string) {
		GinkgoHelper()
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		r, err := s.PutPart(ctx, up, 1, bytes.NewReader(bytes.Repeat([]byte{'a'}, 3<<20)),
			op.PutParams{Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice"))}, Size: 3 << 20})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		return up, r.ETag
	}
	// disconnecting opens a store over c whose lock on up's meta object lands
	// and then meets the end of the context it returns.
	disconnecting := func(ctx context.Context, up *op.Upload) (*driver.Store, context.Context) {
		GinkgoHelper()
		reqCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		lc := &landingCancelCluster{Cluster: c, oid: metaOID(rec, "k", up.ID), cancel: cancel}
		rel := denc.Squid
		st, err := driver.Open(ctx, lc, conf(nil), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(st, func() time.Time { return mtime })
		st.SetRandForTest(fixedRand(putPrefix))
		stores = append(stores, st)
		return st, reqCtx
	}
	// lockWrites is the methods of the lock class the writes to up's meta
	// object call, in order.
	lockWrites := func(up *op.Upload) []string {
		GinkgoHelper()
		var out []string
		for _, w := range c.WritesTo(extraPoolName, "", metaOID(rec, "k", up.ID)) {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Class == "lock" {
					out = append(out, x.Method)
				}
			}
		}
		return out
	}

	It("releases a completion's lock that landed after the context ended, so a retry completes", func(ctx SpecContext) {
		up, etag := upload(ctx)
		st, reqCtx := disconnecting(ctx, up)
		c.ResetCounters()
		_, err := st.Complete(reqCtx, up, []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}})
		Expect(err).To(MatchError(context.Canceled))
		Expect(lockWrites(up)).To(Equal([]string{"lock", "unlock"}), "the unlock follows the lock under the same cookie")
		Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(BeEmpty())

		res, err := s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}})
		Expect(err).NotTo(HaveOccurred(), "the retry meets no lock of its own")
		Expect(res.ETag).To(Equal(mpETag(etag)))
	})

	It("releases an abort's lock that landed after the context ended, so another abort goes through", func(ctx SpecContext) {
		up, _ := upload(ctx)
		st, reqCtx := disconnecting(ctx, up)
		c.ResetCounters()
		Expect(st.Abort(reqCtx, up)).To(MatchError(context.Canceled))
		Expect(lockWrites(up)).To(Equal([]string{"lock", "unlock"}), "the unlock follows the lock under the same cookie")
		Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(BeEmpty())

		Expect(s.Abort(ctx, up)).To(Succeed(), "the retry meets no stray lock")
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil())
	})

	It("leaves a concurrent completion's hold on this gateway when its own unlock follows a lock the context ended", func(ctx SpecContext) {
		up, etag := upload(ctx)
		mOID := metaOID(rec, "k", up.ID)
		parts := []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}}
		c.ResetCounters()
		// A, a completion through s, holds the lock and waits at its first
		// read of the meta object until the spec lets it go.
		held, release := make(chan struct{}), make(chan struct{})
		var paused, released atomic.Bool
		letGo := func() {
			if released.CompareAndSwap(false, true) {
				close(release)
			}
		}
		DeferCleanup(letGo)
		c.BeforeRead(extraPoolName, "", mOID, func() {
			if paused.CompareAndSwap(false, true) {
				close(held)
				<-release
			}
		})
		type outcome struct {
			res *op.PutResult
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			defer GinkgoRecover()
			res, err := s.Complete(ctx, up, parts)
			done <- outcome{res, err}
		}()
		Eventually(held).WithTimeout(10 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
		hold := c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")
		Expect(hold).To(HaveLen(1), "A holds the lock")

		// B, a completion of the same gateway, meets A's hold and then the
		// end of its context, and unlocks under its own cookie.
		st, reqCtx := disconnecting(ctx, up)
		_, err := st.Complete(reqCtx, up, parts)
		Expect(err).To(MatchError(context.Canceled))
		Expect(lockWrites(up)).To(Equal([]string{"lock", "lock", "unlock"}), "A's lock, B's lock, B's unlock")
		Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(Equal(hold), "A's hold stands")

		letGo()
		var a outcome
		Eventually(done).WithTimeout(10 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(&a))
		Expect(a.err).NotTo(HaveOccurred(), "A renews its own hold and completes")
		Expect(a.res.ETag).To(Equal(mpETag(etag)))
	})

	It("sends no lock, and so no unlock, once the context has ended", func(ctx SpecContext) {
		up, etag := upload(ctx)
		reqCtx, cancel := context.WithCancel(ctx)
		cancel()
		c.ResetCounters()
		_, err := s.Complete(reqCtx, up, []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}})
		Expect(err).To(MatchError(context.Canceled))
		Expect(lockWrites(up)).To(BeEmpty())
	})
})
