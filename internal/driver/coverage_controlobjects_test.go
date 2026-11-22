package driver_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// This pins docs/differences.md, "Metadata cache invalidation, both
// directions": rgw-go creates, watches and sends on the control objects
// whatever rgw_cache_enabled says, where radosgw with the option false sends
// no notify and, on Tentacle, neither creates nor watches the objects.
var _ = Describe("the control objects of a gateway whose metadata cache is disabled", func() {
	It("are created, watched and notified on a metadata write", func(ctx SpecContext) {
		const control = "ceph-objectstore.rgw.control"
		c := newIndexCluster()
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		for i := range 8 {
			Expect(c.Object(control, "", fmt.Sprintf("notify.%d", i))).NotTo(BeNil(), "notify.%d created at Open", i)
		}

		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive())
		})
		Eventually(func(g Gomega) {
			for i := range 8 {
				g.Expect(c.Watches(control, "", fmt.Sprintf("notify.%d", i))).To(Equal(1), "notify.%d watched", i)
			}
		}).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Succeed())

		_, err = s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "plain", Owner: aliceOwner, Zonegroup: "zg-ceph-objectstore",
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: {1}}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		var sent int
		for i := range 8 {
			sent += len(c.Notifies(control, "", fmt.Sprintf("notify.%d", i)))
		}
		Expect(sent).To(BeNumerically(">", 0), "the bucket's metadata writes are announced")
	})
})
