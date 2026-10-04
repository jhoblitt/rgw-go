package driver_test

import (
	"bytes"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("the write options", func() {
	var c *fakerados.Cluster
	BeforeEach(func() {
		c = fakerados.New()
		seedRookZone(c, "ceph-objectstore", true)
	})

	It("reads radosgw's defaults", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest()).To(Equal(driver.WriteOptions{
			PutWindow:          16 << 20,
			StripeSize:         4 << 20,
			ChunkSize:          4 << 20,
			MaxPutSize:         5 << 30,
			GCMaxObjs:          32,
			GCObjMinWait:       7200,
			GCProcessorMaxTime: time.Hour,
			GCProcessorPeriod:  time.Hour,
			GCMaxConcurrentIO:  10,
			GCMaxTrimChunk:     16,
			GCMaxQueueSize:     131068 << 10,
			GCMaxDeferred:      50,
			GCThreads:          true,
			MultiObjDelMaxAIO:  16,
			CopyConcurrentIO:   10,
		}), "rgw.yaml.in, identical at v19.2.6 and v20.2.4")
	})

	DescribeTable("floors an rgw_gc_max_objs below 1, logging the option and the tracker",
		func(ctx SpecContext, value string) {
			var buf bytes.Buffer
			DeferCleanup(driver.CaptureLog(&buf))
			s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_objs": value}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.WriteOptionsForTest().GCMaxObjs).To(BeEquivalentTo(1))
			Expect(buf.String()).To(ContainSubstring("rgw_gc_max_objs"))
			Expect(buf.String()).To(ContainSubstring("80991"))
		},
		Entry("zero", "0"),
		Entry("negative", "-3"),
	)

	It("caps rgw_gc_max_objs at rgw_shards_max", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_objs": "70000"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCMaxObjs).To(BeEquivalentTo(65521), "rgw_gc.cc:35 min(rgw_gc_max_objs, rgw_shards_max())")
	})

	It("floors rgw_multi_obj_del_max_aio to one, as RGWDeleteMultiObj::execute does", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_multi_obj_del_max_aio": "0"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().MultiObjDelMaxAIO).To(Equal(1), "rgw_op.cc:7011 at v19.2.6")
	})

	It("narrows rgw_gc_obj_min_wait to the u32 every gc call takes", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_obj_min_wait": "4294967297"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCObjMinWait).To(BeEquivalentTo(1), "cls_rgw_gc_set_entry's uint32_t expiration_secs")
	})

	DescribeTable("refuses write sizes that cannot write, naming the three values",
		func(ctx SpecContext, option, value string) {
			_, err := driver.Open(ctx, c, conf(map[string]string{option: value}), driver.Options{})
			Expect(err).To(MatchError(ContainSubstring("rgw_obj_stripe_size")))
			Expect(err).To(MatchError(ContainSubstring("rgw_put_obj_min_window_size")))
		},
		Entry("a zero stripe", "rgw_obj_stripe_size", "0"),
		Entry("a window below one chunk", "rgw_put_obj_min_window_size", "4194303"),
	)

	It("fails naming a write option it cannot read", func(ctx SpecContext) {
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_deferred": "lots"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("rgw_gc_max_deferred")))
	})
})

var _ = Describe("randAlnum", func() {
	It("draws from gen_rand_alphanumeric's url-safe alphabet", func() {
		for range 64 {
			s := driver.RandAlnumForTest(31)
			Expect(s).To(HaveLen(31))
			Expect(s).To(MatchRegexp(`^[A-Za-z0-9_-]+$`))
		}
	})
})
