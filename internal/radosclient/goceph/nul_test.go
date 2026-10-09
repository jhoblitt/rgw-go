package goceph_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("a name holding a NUL, which librados's C API would cut at it", func() {
	DescribeTable("is refused before the operation reaches librados, as an object name or a locator",
		func(ctx SpecContext, call func(ctx context.Context, p radosclient.Pool, oid string) error) {
			Expect(call(ctx, goceph.DetachedPool("", ""), "a\x00b")).To(MatchError(radosclient.ErrNULName), "the object name")
			Expect(call(ctx, goceph.DetachedPool("", "loc\x00x"), "a")).To(MatchError(radosclient.ErrNULName), "the locator")
		},
		Entry("Read", func(ctx context.Context, p radosclient.Pool, oid string) error {
			rop := radosclient.NewReadOp()
			rop.Stat()
			_, err := p.Read(ctx, oid, rop, radosclient.OpFlagNone)
			return err
		}),
		Entry("Write", func(ctx context.Context, p radosclient.Pool, oid string) error {
			wop := radosclient.NewWriteOp()
			wop.Create(false)
			_, err := p.Write(ctx, oid, wop, radosclient.OpFlagNone)
			return err
		}),
		Entry("Watch", func(ctx context.Context, p radosclient.Pool, oid string) error {
			_, err := p.Watch(ctx, oid, func(uint64, uint64, []byte) {})
			return err
		}),
		Entry("Notify", func(ctx context.Context, p radosclient.Pool, oid string) error {
			_, err := p.Notify(ctx, oid, nil, time.Second)
			return err
		}),
		Entry("LockExclusive", func(ctx context.Context, p radosclient.Pool, oid string) error {
			return p.LockExclusive(ctx, oid, "lock", "cookie", "", time.Second, 0)
		}),
		Entry("LockShared", func(ctx context.Context, p radosclient.Pool, oid string) error {
			return p.LockShared(ctx, oid, "lock", "cookie", "tag", "", time.Second, 0)
		}),
		Entry("Unlock", func(ctx context.Context, p radosclient.Pool, oid string) error {
			return p.Unlock(ctx, oid, "lock", "cookie")
		}),
		Entry("BreakLock", func(ctx context.Context, p radosclient.Pool, oid string) error {
			return p.BreakLock(ctx, oid, "lock", "client.1", "cookie")
		}),
		Entry("ListLockers", func(ctx context.Context, p radosclient.Pool, oid string) error {
			_, err := p.ListLockers(ctx, oid, "lock")
			return err
		}),
	)

	DescribeTable("is refused before a pool is opened",
		func(ctx SpecContext, pool, namespace string) {
			_, err := goceph.DetachedCluster().Pool(ctx, pool, namespace)
			Expect(err).To(MatchError(radosclient.ErrNULName))
		},
		Entry("in the pool's name", "z1.rgw.meta\x00x", ""),
		Entry("in the namespace, which SetNamespace takes", "z1.rgw.meta", "users.uid\x00x"),
	)

	It("names an object of a credential index by the index's kind alone", func(ctx SpecContext) {
		_, err := goceph.DetachedPool("users.keys", "").Read(ctx, "AKHIDDEN\x00x", radosclient.NewReadOp(), radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNULName))
		Expect(err.Error()).NotTo(ContainSubstring("AKHIDDEN"))
	})

	DescribeTable("is refused in a read step before the step is built",
		func(build func(*radosclient.ReadOp)) {
			rop := radosclient.NewReadOp()
			build(rop)
			b := &recorder{}
			Expect(goceph.TranslateRead(b, rop)).To(MatchError(radosclient.ErrNULName))
			Expect(b.calls).To(BeEmpty())
		},
		Entry("an xattr compared", func(o *radosclient.ReadOp) { o.CmpXattr("user.rgw.a\x00b", radosclient.CmpEQ, nil) }),
		Entry("an omap listing's start", func(o *radosclient.ReadOp) { o.OmapGetVals("a\x00b", "", 1) }),
		Entry("an omap listing's prefix", func(o *radosclient.ReadOp) { o.OmapGetVals("", "a\x00b", 1) }),
		Entry("an omap key listing's start", func(o *radosclient.ReadOp) { o.OmapGetKeys("a\x00b", 1) }),
	)

	DescribeTable("is refused in a write step before the step is built",
		func(build func(*radosclient.WriteOp)) {
			wop := radosclient.NewWriteOp()
			build(wop)
			b := &writeRecorder{}
			Expect(goceph.TranslateWrite(b, wop)).To(MatchError(radosclient.ErrNULName))
			Expect(b.calls).To(BeEmpty())
		},
		Entry("an xattr compared", func(o *radosclient.WriteOp) { o.CmpXattr("user.rgw.a\x00b", radosclient.CmpEQ, nil) }),
		Entry("an xattr set", func(o *radosclient.WriteOp) { o.SetXattr("user.rgw.x-amz-meta-a\x00b", []byte("v")) }),
		Entry("an xattr removed", func(o *radosclient.WriteOp) { o.RmXattr("user.rgw.a\x00b") }),
	)

	It("passes a value or an omap key holding one, which librados takes with its length", func() {
		wop := radosclient.NewWriteOp()
		wop.SetXattr("user.rgw.etag", []byte("a\x00b"))
		wop.OmapSet(map[string][]byte{"a\x00b": nil})
		wop.OmapRmKeys([]string{"a\x00b"})
		wop.OmapCmp("a\x00b", radosclient.CmpEQ, nil)
		Expect(goceph.TranslateWrite(&writeRecorder{}, wop)).To(Succeed())
		rop := radosclient.NewReadOp()
		rop.OmapGetValsByKeys([]string{"a\x00b"})
		Expect(goceph.TranslateRead(&recorder{}, rop)).To(Succeed())
	})
})
