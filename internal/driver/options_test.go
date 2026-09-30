package driver

import (
	"bytes"
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

// squidDefaults is readOptions over conf(nil): rgw.yaml.in's defaults at
// v19.2.6.
func squidDefaults() options {
	return options{
		cacheEnabled: true, cacheLRUSize: 25000, cacheExpiry: 900 * time.Second, numControlOIDs: 8, maxNotifyRetries: 10,
		usageFlushThreshold: 1024, usageTick: 30 * time.Second, usageMaxShards: 32, usageMaxUserShards: 1, lcMaxObjs: 32,
		bucketQuotaTTL: 10 * time.Minute, bucketQuotaCacheSize: 10000, bucketSyncInterval: 3 * time.Minute,
		ownerSyncInterval: 24 * time.Hour, ownerSyncWait: 24 * time.Hour, quotaThreads: true,
		listMinReadahead: 1000, bucketIndexMaxAIO: 128, dynamicResharding: true, runSyncThread: true,
	}
}

var _ = Describe("readOptions", func() {
	It("reads every option with radosgw's defaults", func() {
		Expect(readOptions(conf(nil))).To(Equal(squidDefaults()))
	})

	DescribeTable("reads each option into its own field, in its own unit, and names it when it does not parse",
		func(option, value string, set func(*options)) {
			want := squidDefaults()
			set(&want)
			Expect(readOptions(conf(map[string]string{option: value}))).To(Equal(want))
			_, err := readOptions(conf(map[string]string{option: "lots"}))
			Expect(err).To(MatchError(ContainSubstring(option)))
		},
		func(option, value string, _ func(*options)) string { return option + "=" + value },
		Entry(nil, "rgw_cache_enabled", "false", func(o *options) { o.cacheEnabled = false }),
		Entry(nil, "rgw_cache_lru_size", "7", func(o *options) { o.cacheLRUSize = 7 }),
		Entry(nil, "rgw_cache_expiry_interval", "7", func(o *options) { o.cacheExpiry = 7 * time.Second }),
		Entry(nil, "rgw_num_control_oids", "7", func(o *options) { o.numControlOIDs = 7 }),
		Entry(nil, "rgw_max_notify_retries", "7", func(o *options) { o.maxNotifyRetries = 7 }),
		Entry(nil, "rgw_enable_usage_log", "true", func(o *options) { o.usageLogEnabled = true }),
		Entry(nil, "rgw_usage_log_flush_threshold", "7", func(o *options) { o.usageFlushThreshold = 7 }),
		Entry(nil, "rgw_usage_log_tick_interval", "7", func(o *options) { o.usageTick = 7 * time.Second }),
		Entry(nil, "rgw_usage_max_shards", "7", func(o *options) { o.usageMaxShards = 7 }),
		Entry(nil, "rgw_usage_max_user_shards", "7", func(o *options) { o.usageMaxUserShards = 7 }),
		Entry(nil, "rgw_lc_max_objs", "7", func(o *options) { o.lcMaxObjs = 7 }),
		Entry(nil, "rgw_bucket_quota_ttl", "7", func(o *options) { o.bucketQuotaTTL = 7 * time.Second }),
		Entry(nil, "rgw_bucket_quota_cache_size", "7", func(o *options) { o.bucketQuotaCacheSize = 7 }),
		Entry(nil, "rgw_user_quota_bucket_sync_interval", "7", func(o *options) { o.bucketSyncInterval = 7 * time.Second }),
		Entry(nil, "rgw_user_quota_sync_interval", "7", func(o *options) { o.ownerSyncInterval = 7 * time.Second }),
		Entry(nil, "rgw_user_quota_sync_wait_time", "7", func(o *options) { o.ownerSyncWait = 7 * time.Second }),
		Entry(nil, "rgw_user_quota_sync_idle_users", "true", func(o *options) { o.ownerSyncIdle = true }),
		Entry(nil, "rgw_enable_quota_threads", "false", func(o *options) { o.quotaThreads = false }),
		Entry(nil, "rgw_list_bucket_min_readahead", "7", func(o *options) { o.listMinReadahead = 7 }),
		Entry(nil, "rgw_override_bucket_index_max_shards", "7", func(o *options) { o.overrideIndexMaxShards = 7 }),
		Entry(nil, "rgw_bucket_index_max_aio", "7", func(o *options) { o.bucketIndexMaxAIO = 7 }),
		Entry(nil, "rgw_dynamic_resharding", "false", func(o *options) { o.dynamicResharding = false }),
		Entry(nil, "rgw_run_sync_thread", "false", func(o *options) { o.runSyncThread = false }),
	)

	It("floors a zero or negative shard count to one and says so, with the value read", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		o, err := readOptions(conf(map[string]string{"rgw_usage_max_shards": "0", "rgw_lc_max_objs": "-4", "rgw_usage_max_user_shards": "0"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.usageMaxShards).To(Equal(uint32(1)))
		Expect(o.lcMaxObjs).To(Equal(uint32(1)))
		Expect(o.usageMaxUserShards).To(Equal(uint32(1)))
		floored := func(option string, value int) any {
			return And(HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("msg", ContainSubstring("80991")),
				HaveKeyWithValue("option", option), HaveKeyWithValue("value", BeNumerically("==", value)))
		}
		Expect(logRecords(&buf)).To(ConsistOf(
			floored("rgw_usage_max_shards", 0), floored("rgw_lc_max_objs", -4), floored("rgw_usage_max_user_shards", 0),
		))
	})

	It("floors a zero bucket index AIO limit to one and says so, with the value read", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		o, err := readOptions(conf(map[string]string{"rgw_bucket_index_max_aio": "0"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.bucketIndexMaxAIO).To(Equal(1))
		Expect(logRecords(&buf)).To(ConsistOf(And(
			HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("option", "rgw_bucket_index_max_aio"),
			HaveKeyWithValue("value", BeNumerically("==", 0)),
		)))
	})

	It("caps rgw_lc_max_objs at 7877, as radosgw's lifecycle does", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		o, err := readOptions(conf(map[string]string{"rgw_lc_max_objs": "10000"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.lcMaxObjs).To(Equal(uint32(7877)))
		Expect(buf.String()).To(BeEmpty())
	})

	It("uses a value its field cannot hold as the nearest one it can", func() {
		const maxUint64 = "18446744073709551615"
		o, err := readOptions(conf(map[string]string{
			"rgw_override_bucket_index_max_shards": maxUint64, "rgw_bucket_index_max_aio": maxUint64,
			"rgw_cache_expiry_interval": maxUint64, "rgw_user_quota_sync_interval": "9223372037",
			"rgw_bucket_quota_ttl": "-9223372037",
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.overrideIndexMaxShards).To(Equal(uint32(math.MaxUint32)))
		Expect(o.bucketIndexMaxAIO).To(Equal(math.MaxInt))
		Expect(o.cacheExpiry).To(Equal(time.Duration(math.MaxInt64)))
		Expect(o.ownerSyncInterval).To(Equal(time.Duration(math.MaxInt64)))
		Expect(o.bucketQuotaTTL).To(Equal(time.Duration(math.MinInt64)))
	})

	It("fails on an option librados does not know, flooring none it did not read", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		_, err := readOptions(cephconf.NewOptions(cephconf.MapGetter{}))
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
		Expect(buf.String()).To(BeEmpty())
	})
})

var _ = Describe("options.logWorkersNotRun", func() {
	It("warns that no reshard worker runs and notes that a single zone has nothing to sync", func(ctx SpecContext) {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		squidDefaults().logWorkersNotRun(ctx)
		Expect(logRecords(&buf)).To(ConsistOf(
			And(HaveKeyWithValue("level", "WARN"), HaveKeyWithValue("msg", "dynamic resharding is enabled in config but rgw-go runs no reshard worker")),
			And(HaveKeyWithValue("level", "INFO"), HaveKeyWithValue("msg", "rgw_run_sync_thread is set; a single zone has nothing to sync")),
		))
	})

	It("logs nothing when neither is enabled", func(ctx SpecContext) {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		o := squidDefaults()
		o.dynamicResharding, o.runSyncThread = false, false
		o.logWorkersNotRun(ctx)
		Expect(buf.String()).To(BeEmpty())
	})
})
