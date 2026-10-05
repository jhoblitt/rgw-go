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

	It("uses a zero rgw_max_copy_obj_concurrent_io as 1, logging the option, and keeps a negative one", func(ctx SpecContext) {
		var buf bytes.Buffer
		DeferCleanup(driver.CaptureLog(&buf))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_max_copy_obj_concurrent_io": "0"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().CopyConcurrentIO).To(Equal(1))
		Expect(buf.String()).To(ContainSubstring("rgw_max_copy_obj_concurrent_io"))
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_max_copy_obj_concurrent_io": "-1"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().CopyConcurrentIO).To(Equal(-1), "make_throttle's unsigned window: no limit, as radosgw")
	})

	DescribeTable("uses the default for a negative rgw_gc_obj_min_wait, logging the option and the value",
		func(ctx SpecContext, value string) {
			var buf bytes.Buffer
			DeferCleanup(driver.CaptureLog(&buf))
			s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_obj_min_wait": value}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.WriteOptionsForTest().GCObjMinWait).To(BeEquivalentTo(7200), "not a due time that wraps into the past")
			Expect(buf.String()).To(MatchRegexp(`"level":"ERROR".*"option":"rgw_gc_obj_min_wait","value":` + value))
		},
		Entry("-1", "-1"),
		Entry("the most negative int64", "-9223372036854775808"),
	)

	It("keeps a zero rgw_gc_obj_min_wait", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_obj_min_wait": "0"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCObjMinWait).To(BeZero())
	})

	DescribeTable("narrows the gc processor's time options to the int radosgw reads them into",
		func(ctx SpecContext, value string, want time.Duration) {
			s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_processor_max_time": value, "rgw_gc_processor_period": value}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			o := s.WriteOptionsForTest()
			Expect([]time.Duration{o.GCProcessorMaxTime, o.GCProcessorPeriod}).To(Equal([]time.Duration{want, want}), "rgw_gc.cc:731 and :797 at v19.2.6")
		},
		Entry("2^32+1", "4294967297", time.Second),
		Entry("past a Duration's seconds", "10000000001", 1410065409*time.Second),
		Entry("2^31", "2147483648", -2147483648*time.Second),
	)

	It("keeps an rgw_gc_obj_min_wait reaching past 2106, logging it, for each enqueue to saturate", func(ctx SpecContext) {
		var buf bytes.Buffer
		DeferCleanup(driver.CaptureLog(&buf))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_obj_min_wait": "4294967297"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCObjMinWait).To(BeEquivalentTo(4294967297), "not narrowed to 1 s, as radosgw's uint32_t would")
		Expect(buf.String()).To(MatchRegexp(`"level":"ERROR".*"option":"rgw_gc_obj_min_wait","value":4294967297`))
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
