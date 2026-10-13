package gateway_test

import (
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
)

var _ = Describe("Settings.Check", func() {
	check := func(s gateway.Settings) (gateway.Config, error) {
		if s.Release == "" {
			s.Release = "tentacle"
		}
		if s.Gateway == "" {
			s.Gateway = "radosgw"
		}
		return s.Check()
	}

	It("takes the comparison's defaults", func() {
		cfg, err := check(gateway.Settings{Repo: "/repo"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(gateway.Config{
			Release: "tentacle", Gateway: "radosgw", Profiles: gateway.Profiles,
			Seconds: 60, Conc: []int64{1, 8, 32, 64, 128, 256}, PutObjects: 3000, GetObjects: 400,
			LargeThreads: 32, LargeObjects: 1, ListThreads: 64, ListObjects: 1600,
			GoTags: "ceph_preview", Rooket: "rooket", Repo: "/repo",
		}))
	})

	It("takes a smoke run's settings for BENCH_SMOKE=1, where unset", func() {
		cfg, err := check(gateway.Settings{Smoke: "1", Repo: "/repo"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(gateway.Config{
			Release: "tentacle", Gateway: "radosgw", Profiles: gateway.Profiles, Smoke: true,
			Seconds: 5, Conc: []int64{8}, PutObjects: 3000, GetObjects: 50,
			LargeThreads: 4, LargeObjects: 1, ListThreads: 64, ListObjects: 16,
			GoTags: "ceph_preview", Rooket: "rooket", Repo: "/repo",
		}))
	})

	It("lets an explicit setting win over BENCH_SMOKE=1", func() {
		cfg, err := check(gateway.Settings{Smoke: "1", Seconds: "30", Conc: "8 64", GetObjects: "100", LargeThreads: "8", ListObjects: "32"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]int64{cfg.Seconds, cfg.GetObjects, cfg.LargeThreads, cfg.ListObjects}).To(Equal([]int64{30, 100, 8, 32}))
		Expect(cfg.Conc).To(Equal([]int64{8, 64}))
	})

	It("takes the profiles in the order given", func() {
		cfg, err := check(gateway.Settings{Profiles: []string{"noop", "small-get"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Profiles).To(Equal([]string{"noop", "small-get"}))
	})

	It("splits RGW_GO_FLAGS into rgw-go serve's words, keeping it as given", func() {
		cfg, err := check(gateway.Settings{Gateway: "rgw-go", RgwGoFlags: " --rados-completions=pipe  --log-level=debug "})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.RgwGoArgs).To(Equal([]string{"--rados-completions=pipe", "--log-level=debug"}))
		Expect(cfg.RgwGoFlags).To(Equal(" --rados-completions=pipe  --log-level=debug "))
	})

	It("reads the plan's free bytes only with BENCH_PLAN=1", func() {
		cfg, err := check(gateway.Settings{Plan: "1", IdleAvail: "8220119040"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{cfg.Plan, cfg.IdleAvail}).To(Equal([]any{true, int64(8220119040)}))
	})

	DescribeTable("refuses",
		func(s gateway.Settings, want string) {
			_, err := check(s)
			Expect(err).To(MatchError(want))
		},
		Entry("a release other than squid or tentacle", gateway.Settings{Release: "reef"}, `release "reef" is not squid or tentacle`),
		Entry("a gateway other than radosgw or rgw-go", gateway.Settings{Gateway: "minio"}, `gateway "minio" is not radosgw or rgw-go`),
		Entry("an unknown profile", gateway.Settings{Profiles: []string{"small-put", "huge-put"}},
			"no profile huge-put; the profiles are small-put small-get noop large-put large-get list"),
		Entry("a profile given twice, whose second run would append to the first's results",
			gateway.Settings{Profiles: []string{"noop", "list", "noop"}}, "profile noop is given twice"),
		Entry("BENCH_SMOKE=yes, which must not start a full run", gateway.Settings{Smoke: "yes"},
			"BENCH_SMOKE is yes: set it to 1 or leave it unset"),
		Entry("BENCH_PLAN=true, which must not start a run", gateway.Settings{Plan: "true", IdleAvail: "1"},
			"BENCH_PLAN is true: set it to 1 or leave it unset"),
		Entry("a zero count", gateway.Settings{Seconds: "0"}, "BENCH_SECONDS is '0', not a positive count of at most 9 digits"),
		Entry("a count with a unit", gateway.Settings{Seconds: "60s"}, "BENCH_SECONDS is '60s', not a positive count of at most 9 digits"),
		Entry("a count of 10 digits", gateway.Settings{PutObjects: "1000000000"},
			"BENCH_PUT_OBJECTS is '1000000000', not a positive count of at most 9 digits"),
		Entry("a count with a leading zero", gateway.Settings{GetObjects: "0400"},
			"BENCH_GET_OBJECTS is '0400', not a positive count of at most 9 digits"),
		Entry("a negative count", gateway.Settings{LargeThreads: "-4"},
			"BENCH_LARGE_THREADS is '-4', not a positive count of at most 9 digits"),
		Entry("a fractional count", gateway.Settings{LargeObjects: "1.5"},
			"BENCH_LARGE_OBJECTS is '1.5', not a positive count of at most 9 digits"),
		Entry("a list thread count that is no number", gateway.Settings{ListThreads: "many"},
			"BENCH_LIST_THREADS is 'many', not a positive count of at most 9 digits"),
		Entry("an empty-looking list key count", gateway.Settings{ListObjects: " "},
			"BENCH_LIST_OBJECTS is ' ', not a positive count of at most 9 digits"),
		Entry("concurrencies of only spaces", gateway.Settings{Conc: "  "}, "BENCH_CONC names no concurrency"),
		Entry("concurrencies separated by commas", gateway.Settings{Conc: "1,8"},
			"BENCH_CONC holds '1,8', not a positive count of at most 9 digits"),
		Entry("a zero concurrency", gateway.Settings{Conc: "0 8"}, "BENCH_CONC holds '0', not a positive count of at most 9 digits"),
		Entry("a concurrency given twice, whose phase would be profiled twice", gateway.Settings{Conc: "8 32 8"},
			"BENCH_CONC holds 8 twice"),
		Entry("a dataset whose bytes overflow", gateway.Settings{Conc: "999999999", PutObjects: "999999999"},
			"the dataset of small-put overflows: lower its threads or objects"),
		Entry("BENCH_PLAN=1 without BENCH_IDLE_AVAIL", gateway.Settings{Plan: "1"},
			"BENCH_PLAN=1 needs BENCH_IDLE_AVAIL, the data pool's free bytes"),
		Entry("free bytes that are no count", gateway.Settings{Plan: "1", IdleAvail: "8G"},
			"BENCH_IDLE_AVAIL is 8G: give the data pool's free bytes, a positive whole number of at most 18 digits"),
		Entry("free bytes of 19 digits", gateway.Settings{Plan: "1", IdleAvail: "1000000000000000000"},
			"BENCH_IDLE_AVAIL is 1000000000000000000: give the data pool's free bytes, a positive whole number of at most 18 digits"),
		Entry("BENCH_IDLE_AVAIL without BENCH_PLAN=1, where a run would start instead of a plan",
			gateway.Settings{IdleAvail: "8220119040"}, "BENCH_IDLE_AVAIL applies only with BENCH_PLAN=1"),
	)
})

var _ = Describe("Config", func() {
	config := func(s gateway.Settings) gateway.Config {
		GinkgoHelper()
		s.Release, s.Gateway = "tentacle", "radosgw"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		return cfg
	}

	DescribeTable("sizes each profile's dataset from its threads, objects per thread and object size",
		func(s gateway.Settings, profile string, want int64) {
			Expect(config(s).Dataset(profile)).To(Equal(want))
		},
		Entry("small-put at the top concurrency's keys", gateway.Settings{Conc: "8 64 1"}, "small-put", int64(64*3000*4096)),
		Entry("small-get's read set", gateway.Settings{GetObjects: "10"}, "small-get", int64(256*10*4096)),
		Entry("large-put, one write of 64 MiB objects at a time", gateway.Settings{LargeObjects: "2"}, "large-put", int64(32*2*64<<20)),
		Entry("large-get's read set", gateway.Settings{LargeThreads: "4"}, "large-get", int64(4*64<<20)),
		Entry("noop, which writes nothing", gateway.Settings{}, "noop", int64(0)),
		Entry("list, whose keys are empty", gateway.Settings{}, "list", int64(0)),
	)

	It("writes the defaults' plan as traced", func() {
		var b strings.Builder
		Expect(config(gateway.Settings{}).WritePlan(&b, 8220119040)).To(BeTrue())
		Expect(b.String()).To(Equal(golden("plan-default.txt")))
	})

	It("writes a smoke run's plan as traced", func() {
		var b strings.Builder
		Expect(config(gateway.Settings{Smoke: "1"}).WritePlan(&b, 8220119040)).To(BeTrue())
		Expect(b.String()).To(Equal(golden("plan-smoke.txt")))
	})

	It("marks each profile that needs more than the free bytes, and reports the plan does not fit", func() {
		var b strings.Builder
		Expect(config(gateway.Settings{}).WritePlan(&b, 4000000000)).To(BeFalse())
		Expect(b.String()).To(Equal(
			"small-put dataset  3145728000 B, needs  6291456000 B of  4000000000 B free: DOES NOT FIT\n" +
				"small-get dataset   419430400 B, needs   838860800 B of  4000000000 B free: fits\n" +
				"noop      dataset           0 B, needs           0 B of  4000000000 B free: fits\n" +
				"large-put dataset  2147483648 B, needs  4294967296 B of  4000000000 B free: DOES NOT FIT\n" +
				"large-get dataset  2147483648 B, needs  4294967296 B of  4000000000 B free: DOES NOT FIT\n" +
				"list      dataset           0 B, needs           0 B of  4000000000 B free: fits\n"))
	})

	It("plans only the profiles given, in their order", func() {
		var b strings.Builder
		Expect(config(gateway.Settings{Profiles: []string{"list", "large-get"}}).WritePlan(&b, 8220119040)).To(BeTrue())
		Expect(b.String()).To(Equal(
			"list      dataset           0 B, needs           0 B of  8220119040 B free: fits\n" +
				"large-get dataset  2147483648 B, needs  4294967296 B of  8220119040 B free: fits\n"))
	})

	It("names the run directory for the gateway and the run's UTC start", func() {
		cfg := config(gateway.Settings{Repo: "/repo"})
		start := time.Date(2026, 10, 13, 9, 11, 45, 0, time.FixedZone("CEST", 2*3600))
		Expect(cfg.Dir(start)).To(Equal(filepath.Join("/repo", "hack/bench/out/tentacle/gateway-radosgw-20261013T071145Z")))
	})
})

var _ = Describe("NoDelay", func() {
	DescribeTable("reads the TCP_NODELAY a gateway's frontends give from their first tcp_nodelay",
		func(gw, frontends, want string) {
			Expect(gateway.NoDelay(gw, frontends)).To(Equal(want))
		},
		Entry("radosgw's beast with tcp_nodelay=1", "radosgw", "beast port=80 tcp_nodelay=1", "on"),
		Entry("radosgw's beast without it, which leaves it off", "radosgw", "beast port=80", "off"),
		Entry("radosgw's beast with tcp_nodelay=0", "radosgw", "beast port=80 tcp_nodelay=0", "off"),
		Entry("a bare tcp_nodelay, which is no 1", "radosgw", "beast tcp_nodelay port=80", "off"),
		Entry("the first of two tcp_nodelays", "radosgw", "beast tcp_nodelay=1 tcp_nodelay=0", "on"),
		Entry("rgw-go without it, which keeps Go's on", "rgw-go", "beast endpoint=127.0.0.1:7481", "on"),
		Entry("rgw-go with tcp_nodelay=0", "rgw-go", "beast endpoint=127.0.0.1:7481 tcp_nodelay=0", "off"),
		Entry("rgw-go with tcp_nodelay=true, which is no 1", "rgw-go", "beast tcp_nodelay=true", "off"),
	)
})
