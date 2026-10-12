package limited_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/limited/internal/limited"
)

var _ = Describe("Settings.Check", func() {
	var repo string

	BeforeEach(func() {
		repo = GinkgoT().TempDir()
	})

	// limited is the settings of a limiter re-run of squid in repo.
	limitedSettings := func() limited.Settings {
		return limited.Settings{Release: "squid", Limiter: "1", Repo: repo}
	}

	It("defaults to 100 MiB, 640 objects, a derived limit and 5 GiB", func() {
		cfg, err := limitedSettings().Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(limited.Config{
			Release: "squid", Repo: repo, Rooket: "rooket", GoTags: "ceph_preview",
			InflightBytes: 104857600, OverObjects: 640, MinAvailGiB: 5,
		}))
	})

	It("runs 128 objects in quick mode", func() {
		s := limitedSettings()
		s.Quick = "1"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.OverObjects).To(BeEquivalentTo(128))
	})

	It("lets BENCH_OVER_OBJECTS win over quick mode", func() {
		s := limitedSettings()
		s.Quick, s.OverObjects = "1", "300"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.OverObjects).To(BeEquivalentTo(300))
	})

	DescribeTable("accepts",
		func(edit func(*limited.Settings), check func(limited.Config)) {
			s := limitedSettings()
			edit(&s)
			cfg, err := s.Check()
			Expect(err).NotTo(HaveOccurred())
			check(cfg)
		},
		Entry("a plan", func(s *limited.Settings) { s.Plan = "1" },
			func(c limited.Config) { Expect(c.Plan).To(BeTrue(), "Config.Plan for BENCH_PLAN=1") }),
		Entry("an 18-digit byte bound", func(s *limited.Settings) { s.InflightBytes = "999999999999999999" },
			func(c limited.Config) { Expect(c.InflightBytes).To(BeEquivalentTo(999999999999999999)) }),
		Entry("a 10-digit count", func(s *limited.Settings) { s.OverObjects = "9999999999" },
			func(c limited.Config) { Expect(c.OverObjects).To(BeEquivalentTo(9999999999)) }),
		Entry("a timeout in plain seconds", func(s *limited.Settings) { s.Timeout = "600" },
			func(c limited.Config) { Expect(c.Timeout).To(Equal(600 * time.Second)) }),
		Entry("a timeout in hours and minutes", func(s *limited.Settings) { s.Timeout = "1h30m" },
			func(c limited.Config) { Expect(c.Timeout).To(Equal(90 * time.Minute)) }),
		Entry("the longest timeout", func(s *limited.Settings) { s.Timeout = "99999999s" },
			func(c limited.Config) { Expect(c.Timeout).To(Equal(99999999 * time.Second)) }),
		Entry("no free space to wait for", func(s *limited.Settings) { s.MinAvailGiB = "0" },
			func(c limited.Config) { Expect(c.MinAvailGiB).To(BeZero()) }),
		Entry("the release's own ceph.conf", func(s *limited.Settings) {
			s.CephConf = filepath.Join(repo, "hack", "rooket", "out", "squid", "..", "squid", "ceph.conf")
		}, func(c limited.Config) {
			Expect(c.CephConf()).To(Equal(filepath.Join(repo, "hack/rooket/out/squid/ceph.conf")))
		}),
		Entry("the rooket binary and build tags given", func(s *limited.Settings) { s.Rooket, s.GoTags = "/opt/rooket", "ceph_preview,x" },
			func(c limited.Config) {
				Expect([]string{c.Rooket, c.GoTags}).To(Equal([]string{"/opt/rooket", "ceph_preview,x"}))
			}),
	)

	It("accepts the release's ceph.conf through a symbolic link", func() {
		out := filepath.Join(repo, "hack", "rooket", "out", "squid")
		Expect(os.MkdirAll(out, 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(out, "ceph.conf"), nil, 0o600)).To(Succeed())
		link := filepath.Join(GinkgoT().TempDir(), "ceph.conf")
		Expect(os.Symlink(filepath.Join(out, "ceph.conf"), link)).To(Succeed())
		s := limitedSettings()
		s.CephConf = link
		_, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("refuses",
		func(edit func(*limited.Settings), want string) {
			s := limitedSettings()
			edit(&s)
			_, err := s.Check()
			Expect(err).To(MatchError(want))
		},
		Entry("another release", func(s *limited.Settings) { s.Release = "reef" }, `release "reef" is not squid or tentacle`),
		Entry("a limiter other than 1", func(s *limited.Settings) { s.Limiter = "yes" }, "BENCH_LIMITER is yes: set it to 1 or leave it unset"),
		Entry("a plan other than 1, which would start the run it asked to plan", func(s *limited.Settings) { s.Plan = "yes" },
			"BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("a plan of true", func(s *limited.Settings) { s.Plan = "true" }, "BENCH_PLAN is true: set it to 1 or leave it unset"),
		Entry("a plan of 0", func(s *limited.Settings) { s.Plan = "0" }, "BENCH_PLAN is 0: set it to 1 or leave it unset"),
		Entry("quick mode other than 1, which would run 640 objects", func(s *limited.Settings) { s.Quick = "yes" },
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("a plan other than 1 without the limiter", func(s *limited.Settings) { s.Limiter, s.Plan = "", "yes" },
			"BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("a byte bound without the limiter", func(s *limited.Settings) { s.Limiter, s.InflightBytes = "", "67108864" },
			"BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1"),
		Entry("a plan without the limiter", func(s *limited.Settings) { s.Limiter, s.Plan = "", "1" },
			"BENCH_PLAN=1 prints the limiter re-run only: set BENCH_LIMITER=1"),
		Entry("a run without the limiter", func(s *limited.Settings) { s.Limiter = "" }, "the limiter re-run needs BENCH_LIMITER=1"),
		Entry("a zero byte bound", func(s *limited.Settings) { s.InflightBytes = "0" },
			"BENCH_INFLIGHT_BYTES is 0: give a positive whole number of bytes, at most 18 digits"),
		Entry("a negative byte bound", func(s *limited.Settings) { s.InflightBytes = "-5" },
			"BENCH_INFLIGHT_BYTES is -5: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound with a leading zero, which would alias the default's directory", func(s *limited.Settings) { s.InflightBytes = "0104857600" },
			"BENCH_INFLIGHT_BYTES is 0104857600: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound with a unit", func(s *limited.Settings) { s.InflightBytes = "100Mi" },
			"BENCH_INFLIGHT_BYTES is 100Mi: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound in exponent notation", func(s *limited.Settings) { s.InflightBytes = "1e8" },
			"BENCH_INFLIGHT_BYTES is 1e8: give a positive whole number of bytes, at most 18 digits"),
		Entry("a 19-digit byte bound", func(s *limited.Settings) { s.InflightBytes = "1000000000000000000" },
			"BENCH_INFLIGHT_BYTES is 1000000000000000000: give a positive whole number of bytes, at most 18 digits"),
		Entry("a zero count", func(s *limited.Settings) { s.OverObjects = "0" },
			"BENCH_OVER_OBJECTS is 0: give a positive whole number of at most 10 digits"),
		Entry("a count with a leading zero, which shell arithmetic read as octal", func(s *limited.Settings) { s.OverObjects = "0640" },
			"BENCH_OVER_OBJECTS is 0640: give a positive whole number of at most 10 digits"),
		Entry("an 11-digit count", func(s *limited.Settings) { s.OverObjects = "12345678901" },
			"BENCH_OVER_OBJECTS is 12345678901: give a positive whole number of at most 10 digits"),
		Entry("free space with a unit", func(s *limited.Settings) { s.MinAvailGiB = "5G" },
			"BENCH_MIN_AVAIL_GIB is 5G: give a whole number of GiB, at most 8589934591"),
		Entry("free space past an int64 of bytes", func(s *limited.Settings) { s.MinAvailGiB = "8589934592" },
			"BENCH_MIN_AVAIL_GIB is 8589934592: give a whole number of GiB, at most 8589934591"),
	)

	It("names the release's ceph.conf when it refuses another", func() {
		s := limitedSettings()
		s.CephConf = "/elsewhere/ceph.conf"
		_, err := s.Check()
		Expect(err).To(MatchError("RGW_GO_TEST_CEPH_CONF is /elsewhere/ceph.conf, but the squid cluster is reached through " +
			filepath.Join(repo, "hack/rooket/out/squid/ceph.conf") + ": unset it"))
	})

	DescribeTable("refuses a timeout",
		func(timeout string) {
			s := limitedSettings()
			s.Timeout = timeout
			_, err := s.Check()
			Expect(err).To(MatchError("BENCH_TIMEOUT " + timeout + " is not a duration in h, m and s from 1 s to 99999999 s"))
		},
		Entry("of zero seconds, which would stop every process at once", "0"),
		Entry("of zero in units", "0s"),
		Entry("of zero in every unit", "0h0m0s"),
		Entry("that is no duration", "bogus"),
		Entry("in fractions", "1.5s"),
		Entry("in milliseconds", "500ms"),
		Entry("written as a count", "100x"),
		Entry("over 99999999 s", "27778h"),
		Entry("of 100000000 plain seconds", "100000000"),
		Entry("with a 10-digit part", "1000000000s"),
		Entry("that would overflow the arithmetic", "99999999999999999999"),
		Entry("in units out of order", "5s1m"),
	)
})

// runStart is the time the specs' runs start.
var runStart = time.Date(2026, 10, 12, 21, 5, 9, 0, time.UTC)

var _ = Describe("Config", func() {
	cfg := func(edit func(*limited.Config)) limited.Config {
		c := limited.Config{Release: "tentacle", Repo: "/repo", InflightBytes: 104857600, OverObjects: 640, MinAvailGiB: 5}
		edit(&c)
		return c
	}

	DescribeTable("Dir",
		func(c limited.Config, want string) {
			Expect(c.Dir(runStart)).To(Equal(want))
		},
		Entry("at the default bound", cfg(func(*limited.Config) {}), "/repo/hack/bench/out/tentacle/seam-20261012T210509Z-limited"),
		Entry("at another bound", cfg(func(c *limited.Config) { c.InflightBytes = 67108864 }),
			"/repo/hack/bench/out/tentacle/seam-20261012T210509Z-limited-67108864"),
	)

	It("names the directory by UTC time", func() {
		local := runStart.In(time.FixedZone("UTC-7", -7*3600))
		Expect(cfg(func(*limited.Config) {}).Dir(local)).To(HaveSuffix("seam-20261012T210509Z-limited"))
	})

	DescribeTable("Limit",
		func(c limited.Config, want time.Duration) {
			Expect(c.Limit()).To(Equal(want))
		},
		Entry("a tenth of a second an object, plus five minutes", cfg(func(*limited.Config) {}), 364*time.Second),
		Entry("in quick mode", cfg(func(c *limited.Config) { c.OverObjects = 128 }), 312*time.Second),
		Entry("rounding a tenth down", cfg(func(c *limited.Config) { c.OverObjects = 649 }), 364*time.Second),
		Entry("BENCH_TIMEOUT when set", cfg(func(c *limited.Config) { c.Timeout = 10 * time.Minute }), 600*time.Second),
	)

	It("passes the benchmark the cell, its mode, its results and a -test.timeout past the watchdog", func() {
		Expect(cfg(func(*limited.Config) {}).BenchArgs("pipe", "/out/overbudget-pipe.jsonl")).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "454s",
			"-mode=pipe", "-results=/out/overbudget-pipe.jsonl",
			"-shapes=write4m", "-conc=64", "-budget-bytes=0", "-inflight-bytes=104857600", "-test.benchtime=640x",
		}))
	})

	DescribeTable("WritePlan",
		func(c limited.Config, want string) {
			var b bytes.Buffer
			c.WritePlan(&b)
			Expect(b.String()).To(Equal(want))
		},
		Entry("at the defaults", cfg(func(*limited.Config) {}), plan("tentacle", "104857600", "-limited", "640", "364")),
		Entry("at another bound", cfg(func(c *limited.Config) { c.InflightBytes = 67108864 }),
			plan("tentacle", "67108864", "-limited-67108864", "640", "364")),
		Entry("in quick mode", cfg(func(c *limited.Config) { c.OverObjects = 128 }), plan("tentacle", "104857600", "-limited", "128", "312")),
		Entry("under BENCH_TIMEOUT", cfg(func(c *limited.Config) { c.Timeout = 90 * time.Minute }),
			plan("tentacle", "104857600", "-limited", "640", "5400")),
	)
})

// plan is WritePlan's output for a release, byte bound, directory suffix,
// object count and limit.
func plan(release, bytes, suffix, objects, limit string) string {
	var b strings.Builder
	b.WriteString("limited: the " + release + " limiter re-run at " + bytes + " B, into hack/bench/out/" + release + "/seam-<UTC time>" + suffix + "/\n")
	for _, mode := range []string{"sync", "callback", "pipe"} {
		b.WriteString("bench " + mode + ": -shapes=write4m -conc=64 -budget-bytes=0 -inflight-bytes=" + bytes +
			" -test.benchtime=" + objects + "x -> overbudget-" + mode + ".jsonl (limit " + limit + " s)\n")
	}
	return b.String()
}
