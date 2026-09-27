//go:build integration

package user_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// manifest is the part of populate.sh's manifest.json these specs read.
type manifest struct {
	Pools   struct{ Meta string }
	Buckets []struct{ Name, Owner, ID, Marker string }
	Objects []struct {
		Bucket string
		Size   uint64
	}
}

// roundedSize is rgw_rounded_objsize: the size rounded up to 4 KiB.
func roundedSize(size uint64) uint64 { return (size + 4095) &^ 4095 }

var _ = Describe("user against a cluster", Label("integration"), func() {
	var (
		pool radosclient.Pool
		m    manifest
	)
	const oid = "alice.buckets"

	BeforeEach(func(ctx SpecContext) {
		conf := cephtest.Conf()
		cephtest.ReadManifest(conf, &m)
		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		pool, err = cluster.Pool(ctx, m.Pools.Meta, "users.uid")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
	})

	listAll := func(ctx SpecContext) []user.BucketEntry {
		GinkgoHelper()
		op := radosclient.NewReadOp()
		res := user.ListBuckets(op, "", "", 1000, denc.Squid)
		Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
		entries, marker, truncated, err := res.Entries()
		Expect(err).NotTo(HaveOccurred())
		Expect(marker).To(BeEmpty())
		Expect(truncated).To(BeFalse())
		return entries
	}
	header := func(ctx SpecContext) user.Header {
		GinkgoHelper()
		op := radosclient.NewReadOp()
		res := user.GetHeader(op, denc.Squid)
		Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
		h, err := res.Header()
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	It("lists plain in alice.buckets, re-encoding radosgw's entry byte for byte", func(ctx SpecContext) {
		op := radosclient.NewReadOp()
		res := user.ListBuckets(op, "", "", 1000, denc.Squid)
		omap := op.OmapGetVals("", "", 1000)
		Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
		entries, _, truncated, err := res.Entries()
		Expect(err).NotTo(HaveOccurred())
		Expect(truncated).To(BeFalse())

		var want []string
		var id, marker string
		for _, b := range m.Buckets {
			if b.Owner == "alice" {
				want = append(want, b.Name)
			}
			if b.Name == "plain" {
				id, marker = b.ID, b.Marker
			}
		}
		Expect(entries).To(HaveLen(len(want)))
		var plain user.BucketEntry
		for i, e := range entries {
			Expect(e.Bucket.Name).To(Equal(want[i]))
			if e.Bucket.Name == "plain" {
				plain = e
			}
		}
		Expect(plain.Bucket.Name).To(Equal("plain"))
		Expect(plain.Bucket.BucketID).To(Equal(id))
		Expect(plain.Bucket.Marker).To(Equal(marker))
		Expect(plain.Bucket.PlacementID).To(BeEmpty(), "radosgw writes the (7,3) form")
		Expect(plain.CreationTime).NotTo(BeZero())

		Expect(omap.Values).To(HaveKey("plain"))
		e := denc.NewEncoder()
		plain.Encode(e, denc.Squid)
		Expect(e.Bytes()).To(Equal(omap.Values["plain"]))

		for _, bounds := range [][2]string{{"plain", ""}, {"", "plain"}} {
			op := radosclient.NewReadOp()
			res := user.ListBuckets(op, bounds[0], bounds[1], 1000, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			entries, _, _, err := res.Entries()
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "marker %q, end marker %q", bounds[0], bounds[1])
		}
	})

	It("reads the stats radosgw synced, recounts them and completes a sync", func(ctx SpecContext) {
		// populate.sh runs radosgw-admin user stats --sync-stats, which has
		// radosgw write each bucket's stats into its entry and their sum into
		// the header; the bucket sync thread writes the same within
		// rgw_user_quota_bucket_sync_interval.
		perBucket := map[string]user.Stats{}
		for _, b := range m.Buckets {
			if b.Owner == "alice" {
				perBucket[b.Name] = user.Stats{}
			}
		}
		for _, o := range m.Objects {
			if s, ok := perBucket[o.Bucket]; ok {
				s.TotalEntries++
				s.TotalBytes += o.Size
				s.TotalBytesRounded += roundedSize(o.Size)
				perBucket[o.Bucket] = s
			}
		}
		var want user.Stats
		for _, s := range perBucket {
			want.TotalEntries += s.TotalEntries
			want.TotalBytes += s.TotalBytes
			want.TotalBytesRounded += s.TotalBytesRounded
		}
		Expect(want.TotalEntries).To(BeNumerically(">", 0))

		entries := listAll(ctx)
		Expect(entries).To(HaveLen(len(perBucket)))
		for _, e := range entries {
			Expect(perBucket).To(HaveKey(e.Bucket.Name))
			s := perBucket[e.Bucket.Name]
			Expect(e.UserStatsSync).To(BeTrue(), e.Bucket.Name)
			Expect(user.Stats{TotalEntries: e.Count, TotalBytes: e.Size, TotalBytesRounded: e.SizeRounded}).
				To(Equal(s), e.Bucket.Name)
		}
		h := header(ctx)
		Expect(h.Stats).To(Equal(want))
		Expect(h.LastStatsSync).NotTo(BeZero())

		reset := time.Now().UTC()
		var ret user.ResetStats2Ret
		for {
			op := radosclient.NewReadOp()
			res := user.ResetStats2(op, reset, ret.Marker, ret.AccStats, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagReturnVec)).Error().To(Succeed())
			var err error
			ret, err = res.Result()
			Expect(err).NotTo(HaveOccurred())
			if !ret.Truncated {
				break
			}
		}
		Expect(ret.AccStats).To(Equal(want))
		h = header(ctx)
		Expect(h.Stats).To(Equal(want))
		Expect(h.LastStatsUpdate).To(BeTemporally(">=", reset))
		// The last page writes a fresh header, dropping the last sync time.
		Expect(h.LastStatsSync).To(BeZero())

		completed := time.Now().UTC()
		w := radosclient.NewWriteOp()
		user.CompleteStatsSync(w, completed, denc.Squid)
		_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(header(ctx).LastStatsSync).To(BeTemporally("==", completed))
	})
})
