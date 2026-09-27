//go:build integration

package refcount_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// manifest is the part of populate.sh's manifest.json these specs read.
type manifest struct {
	Pools   struct{ Data string }
	Buckets []struct{ Name, Marker string }
}

var _ = Describe("refcount against a cluster", Label("integration"), func() {
	var (
		cluster radosclient.Cluster
		m       manifest
	)

	BeforeEach(func(ctx SpecContext) {
		conf := cephtest.Conf()
		cephtest.ReadManifest(conf, &m)
		var err error
		cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
	})

	// A plain PUT writes its tail objects with no refcount xattr; radosgw
	// takes refs, NUL-terminated write tags, only when a copy shares a tail.
	It("reads large.bin's tail objects as held only by the implicit wildcard ref", func(ctx SpecContext) {
		var marker string
		for _, b := range m.Buckets {
			if b.Name == "plain" {
				marker = b.Marker
			}
		}
		Expect(marker).NotTo(BeEmpty())
		pool, err := cluster.Pool(ctx, m.Pools.Data, "")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })

		// large.bin is the only upload in plain with a head plus stripes and no
		// parts, so the __shadow_ objects with the atomic "." prefix are its tail.
		var tails []string
		Expect(pool.ListObjects(ctx, func(oid, _ string) error {
			if strings.HasPrefix(oid, marker+"__shadow_.") {
				tails = append(tails, oid)
			}
			return nil
		})).To(Succeed())
		Expect(tails).NotTo(BeEmpty())

		for _, oid := range tails {
			op := radosclient.NewReadOp()
			explicit := refcount.Read(op, false, denc.Squid)
			implicit := refcount.Read(op, true, denc.Squid)
			xattrs := op.GetXattrs()
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), oid)
			refs, err := explicit.Refs()
			Expect(err).NotTo(HaveOccurred())
			Expect(refs).To(BeEmpty(), oid)
			refs, err = implicit.Refs()
			Expect(err).NotTo(HaveOccurred())
			Expect(refs).To(Equal([]string{""}), oid)
			Expect(xattrs.Xattrs).NotTo(HaveKey(refcount.XattrName), oid)
		}
	})

	It("gets, puts and sets NUL-terminated tags until the last put removes the object", func(ctx SpecContext) {
		pool, err := cluster.Pool(ctx, cephtest.TestPool, "")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
		oid := fmt.Sprintf("cls-refcount-%d", time.Now().UnixNano())
		write := func(build func(*radosclient.WriteOp)) {
			GinkgoHelper()
			w := radosclient.NewWriteOp()
			build(w)
			_, werr := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
			Expect(werr).NotTo(HaveOccurred())
		}
		stored := func() (refcount.Refcount, []string) {
			GinkgoHelper()
			op := radosclient.NewReadOp()
			res := refcount.Read(op, false, denc.Squid)
			xattrs := op.GetXattrs()
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			refs, rerr := res.Refs()
			Expect(rerr).NotTo(HaveOccurred())
			d := denc.NewDecoder(xattrs.Xattrs[refcount.XattrName])
			rc := refcount.DecodeRefcount(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(d.Remaining()).To(BeZero())
			return rc, refs
		}

		const tag = "write-tag\x00"
		// read_refcount tolerates only ENODATA, so a get fails on an object
		// that does not exist yet, even in the op that creates it.
		w := radosclient.NewWriteOp()
		w.WriteFull([]byte("tail"))
		refcount.Get(w, tag, true, denc.Squid)
		_, err = pool.Write(ctx, oid, w, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))

		write(func(w *radosclient.WriteOp) { w.WriteFull([]byte("tail")) })
		DeferCleanup(func(ctx SpecContext) {
			w := radosclient.NewWriteOp()
			w.Remove()
			_, werr := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
			if werr != nil {
				Expect(werr).To(MatchError(radosclient.ErrNotFound))
			}
		})
		write(func(w *radosclient.WriteOp) { refcount.Get(w, tag, true, denc.Squid) })
		rc, refs := stored()
		Expect(refs).To(Equal([]string{"", tag}))
		Expect(rc).To(Equal(refcount.Refcount{Refs: map[string]bool{"": true, tag: true}}))

		write(func(w *radosclient.WriteOp) { refcount.Put(w, tag, true, denc.Squid) })
		write(func(w *radosclient.WriteOp) { refcount.Put(w, tag, true, denc.Squid) })
		rc, refs = stored()
		Expect(refs).To(Equal([]string{""}))
		Expect(rc).To(Equal(refcount.Refcount{Refs: map[string]bool{"": true}, RetiredRefs: []string{tag}}))

		write(func(w *radosclient.WriteOp) { refcount.Set(w, []string{"b\x00", "a\x00"}, denc.Squid) })
		rc, refs = stored()
		Expect(refs).To(Equal([]string{"a\x00", "b\x00"}))
		Expect(rc).To(Equal(refcount.Refcount{Refs: map[string]bool{"a\x00": true, "b\x00": true}}))

		write(func(w *radosclient.WriteOp) { refcount.Put(w, "a\x00", false, denc.Squid) })
		write(func(w *radosclient.WriteOp) { refcount.Put(w, "b\x00", false, denc.Squid) })
		op := radosclient.NewReadOp()
		op.Stat()
		Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrNotFound))
	})
})
