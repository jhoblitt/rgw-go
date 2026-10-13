package seam_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

// golden reads a file of testdata.
func golden(name string) string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

var _ = Describe("Settings.Check", func() {
	var repo string

	BeforeEach(func() {
		repo = GinkgoT().TempDir()
	})

	// limitedSettings is the settings of a limiter re-run of squid in repo.
	limitedSettings := func() seam.Settings {
		return seam.Settings{Release: "squid", Limiter: "1", Repo: repo}
	}
	// sweepSettings is the settings of a sweep of squid in repo.
	sweepSettings := func() seam.Settings {
		return seam.Settings{Release: "squid", Repo: repo}
	}

	It("defaults the limiter re-run to 100 MiB, 640 objects, a derived limit and 5 GiB", func() {
		cfg, err := limitedSettings().Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(seam.Config{
			Release: "squid", Repo: repo, Rooket: "rooket", GoTags: "ceph_preview", Limiter: true,
			InflightBytes: 104857600, OverObjects: 640, MinAvailGiB: 5,
		}))
	})

	It("defaults the sweep to its documented settings", func() {
		cfg, err := sweepSettings().Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(seam.Config{
			Release: "squid", Repo: repo, Rooket: "rooket", GoTags: "ceph_preview",
			OverObjects: 640, MinAvailGiB: 5,
			BenchTime: 20 * time.Second, FloorSeconds: 20, Conc: []int{1, 16, 64, 256, 512}, LargeConc: []int{1, 4, 16},
			Write4mObjects: 1000, Engine: "podman",
		}))
	})

	It("takes BENCH_QUICK=1's settings where the rest are unset", func() {
		s := sweepSettings()
		s.Quick = "1"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(seam.Config{
			Release: "squid", Repo: repo, Rooket: "rooket", GoTags: "ceph_preview",
			OverObjects: 128, MinAvailGiB: 5,
			BenchTime: 5 * time.Second, FloorSeconds: 5, Conc: []int{16, 256}, LargeConc: []int{16},
			Write4mObjects: 100, Engine: "podman",
		}))
	})

	It("runs 128 objects in the limiter's quick mode", func() {
		s := limitedSettings()
		s.Quick = "1"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.OverObjects).To(BeEquivalentTo(128))
	})

	It("lets each setting win over quick mode", func() {
		empty := ""
		s := sweepSettings()
		s.Quick, s.BenchTime, s.FloorSeconds, s.Conc, s.LargeConc = "1", "1m", "30", "64", &empty
		s.Write4mObjects, s.OverObjects = "200", "300"
		cfg, err := s.Check()
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{cfg.BenchTime, cfg.FloorSeconds, cfg.Conc, cfg.LargeConc, cfg.Write4mObjects, cfg.OverObjects}).
			To(Equal([]any{time.Minute, int64(30), []int{64}, []int(nil), int64(200), int64(300)}))
	})

	DescribeTable("accepts",
		func(base func() seam.Settings, edit func(*seam.Settings), check func(seam.Config)) {
			s := base()
			edit(&s)
			cfg, err := s.Check()
			Expect(err).NotTo(HaveOccurred())
			check(cfg)
		},
		Entry("a plan of the limiter re-run", limitedSettings, func(s *seam.Settings) { s.Plan = "1" },
			func(c seam.Config) { Expect([]bool{c.Plan, c.Limiter}).To(Equal([]bool{true, true})) }),
		Entry("a plan of the sweep", sweepSettings, func(s *seam.Settings) { s.Plan = "1" },
			func(c seam.Config) { Expect([]bool{c.Plan, c.Limiter}).To(Equal([]bool{true, false})) }),
		Entry("an 18-digit byte bound", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "999999999999999999" },
			func(c seam.Config) { Expect(c.InflightBytes).To(BeEquivalentTo(999999999999999999)) }),
		Entry("a 10-digit count", limitedSettings, func(s *seam.Settings) { s.OverObjects = "9999999999" },
			func(c seam.Config) { Expect(c.OverObjects).To(BeEquivalentTo(9999999999)) }),
		Entry("a timeout in plain seconds", limitedSettings, func(s *seam.Settings) { s.Timeout = "600" },
			func(c seam.Config) { Expect(c.Timeout).To(Equal(600 * time.Second)) }),
		Entry("a timeout in hours and minutes", sweepSettings, func(s *seam.Settings) { s.Timeout = "1h30m" },
			func(c seam.Config) { Expect(c.Timeout).To(Equal(90 * time.Minute)) }),
		Entry("the longest timeout", limitedSettings, func(s *seam.Settings) { s.Timeout = "99999999s" },
			func(c seam.Config) { Expect(c.Timeout).To(Equal(99999999 * time.Second)) }),
		Entry("no free space to wait for", sweepSettings, func(s *seam.Settings) { s.MinAvailGiB = "0" },
			func(c seam.Config) { Expect(c.MinAvailGiB).To(BeZero()) }),
		Entry("a benchtime in plain seconds, which go test would refuse as given", sweepSettings, func(s *seam.Settings) { s.BenchTime = "90" },
			func(c seam.Config) { Expect(c.BenchTime).To(Equal(90 * time.Second)) }),
		Entry("a benchtime in minutes and seconds", sweepSettings, func(s *seam.Settings) { s.BenchTime = "1m30s" },
			func(c seam.Config) { Expect(c.BenchTime).To(Equal(90 * time.Second)) }),
		Entry("floor seconds", sweepSettings, func(s *seam.Settings) { s.FloorSeconds = "45" },
			func(c seam.Config) { Expect(c.FloorSeconds).To(BeEquivalentTo(45)) }),
		Entry("concurrencies in any order", sweepSettings, func(s *seam.Settings) { s.Conc = "256,1,64" },
			func(c seam.Config) { Expect(c.Conc).To(Equal([]int{256, 1, 64})) }),
		Entry("4 MiB concurrencies", sweepSettings, func(s *seam.Settings) { large := "16,1"; s.LargeConc = &large },
			func(c seam.Config) { Expect(c.LargeConc).To(Equal([]int{16, 1})) }),
		Entry("an empty BENCH_LARGE_CONC, which skips the 4 MiB cells", sweepSettings, func(s *seam.Settings) { large := ""; s.LargeConc = &large },
			func(c seam.Config) { Expect(c.LargeConc).To(BeEmpty()) }),
		Entry("write4m objects", sweepSettings, func(s *seam.Settings) { s.Write4mObjects = "500" },
			func(c seam.Config) { Expect(c.Write4mObjects).To(BeEquivalentTo(500)) }),
		Entry("a floor timeout", sweepSettings, func(s *seam.Settings) { s.FloorTimeout = "15m" },
			func(c seam.Config) { Expect(c.FloorTimeout).To(Equal(15 * time.Minute)) }),
		Entry("a floor image and an engine", sweepSettings, func(s *seam.Settings) { s.FloorImage, s.Engine = "registry.local/ceph:v19.2.6", "docker" },
			func(c seam.Config) {
				Expect([]string{c.FloorImage, c.Engine}).To(Equal([]string{"registry.local/ceph:v19.2.6", "docker"}))
			}),
		Entry("an engine in the limiter re-run, which rooket reads too", limitedSettings, func(s *seam.Settings) { s.Engine = "docker" },
			func(c seam.Config) { Expect(c.Limiter).To(BeTrue()) }),
		Entry("the release's own ceph.conf", sweepSettings, func(s *seam.Settings) {
			s.CephConf = filepath.Join(repo, "hack", "rooket", "out", "squid", "..", "squid", "ceph.conf")
		}, func(c seam.Config) {
			Expect(c.CephConf()).To(Equal(filepath.Join(repo, "hack/rooket/out/squid/ceph.conf")))
		}),
		Entry("the rooket binary and build tags given", limitedSettings, func(s *seam.Settings) { s.Rooket, s.GoTags = "/opt/rooket", "ceph_preview,x" },
			func(c seam.Config) {
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
		func(base func() seam.Settings, edit func(*seam.Settings), want string) {
			s := base()
			edit(&s)
			_, err := s.Check()
			Expect(err).To(MatchError(want))
		},
		Entry("another release", sweepSettings, func(s *seam.Settings) { s.Release = "reef" }, `release "reef" is not squid or tentacle`),
		Entry("a limiter other than 1", limitedSettings, func(s *seam.Settings) { s.Limiter = "yes" }, "BENCH_LIMITER is yes: set it to 1 or leave it unset"),
		Entry("a plan other than 1, which would start the run it asked to plan", sweepSettings, func(s *seam.Settings) { s.Plan = "yes" },
			"BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("a plan of true", limitedSettings, func(s *seam.Settings) { s.Plan = "true" }, "BENCH_PLAN is true: set it to 1 or leave it unset"),
		Entry("a plan of 0", limitedSettings, func(s *seam.Settings) { s.Plan = "0" }, "BENCH_PLAN is 0: set it to 1 or leave it unset"),
		Entry("quick mode of yes in the sweep, which must not run a full sweep", sweepSettings, func(s *seam.Settings) { s.Quick = "yes" },
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("quick mode of yes in the limiter re-run, which would run 640 objects", limitedSettings, func(s *seam.Settings) { s.Quick = "yes" },
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("a byte bound without the limiter", sweepSettings, func(s *seam.Settings) { s.InflightBytes = "67108864" },
			"BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1"),
		Entry("a zero byte bound", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "0" },
			"BENCH_INFLIGHT_BYTES is 0: give a positive whole number of bytes, at most 18 digits"),
		Entry("a negative byte bound", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "-5" },
			"BENCH_INFLIGHT_BYTES is -5: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound with a leading zero, which would alias the default's directory", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "0104857600" },
			"BENCH_INFLIGHT_BYTES is 0104857600: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound with a unit", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "100Mi" },
			"BENCH_INFLIGHT_BYTES is 100Mi: give a positive whole number of bytes, at most 18 digits"),
		Entry("a byte bound in exponent notation", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "1e8" },
			"BENCH_INFLIGHT_BYTES is 1e8: give a positive whole number of bytes, at most 18 digits"),
		Entry("a 19-digit byte bound", limitedSettings, func(s *seam.Settings) { s.InflightBytes = "1000000000000000000" },
			"BENCH_INFLIGHT_BYTES is 1000000000000000000: give a positive whole number of bytes, at most 18 digits"),
		Entry("a zero count", sweepSettings, func(s *seam.Settings) { s.OverObjects = "0" },
			"BENCH_OVER_OBJECTS is 0: give a positive whole number of at most 10 digits"),
		Entry("a count with a leading zero, which shell arithmetic read as octal", limitedSettings, func(s *seam.Settings) { s.OverObjects = "0640" },
			"BENCH_OVER_OBJECTS is 0640: give a positive whole number of at most 10 digits"),
		Entry("an 11-digit count", limitedSettings, func(s *seam.Settings) { s.OverObjects = "12345678901" },
			"BENCH_OVER_OBJECTS is 12345678901: give a positive whole number of at most 10 digits"),
		Entry("free space with a unit", sweepSettings, func(s *seam.Settings) { s.MinAvailGiB = "5G" },
			"BENCH_MIN_AVAIL_GIB is 5G: give a whole number of GiB, at most 8589934591"),
		Entry("free space past an int64 of bytes", limitedSettings, func(s *seam.Settings) { s.MinAvailGiB = "8589934592" },
			"BENCH_MIN_AVAIL_GIB is 8589934592: give a whole number of GiB, at most 8589934591"),
		Entry("zero floor seconds", sweepSettings, func(s *seam.Settings) { s.FloorSeconds = "0" },
			"BENCH_FLOOR_SECONDS is 0: give a positive whole number of seconds, at most 8 digits"),
		Entry("floor seconds with a unit", sweepSettings, func(s *seam.Settings) { s.FloorSeconds = "20s" },
			"BENCH_FLOOR_SECONDS is 20s: give a positive whole number of seconds, at most 8 digits"),
		Entry("a concurrency that is no number", sweepSettings, func(s *seam.Settings) { s.Conc = "16,lots" },
			"CONC is 16,lots: give positive whole numbers, comma separated, each at most once"),
		Entry("a zero concurrency", sweepSettings, func(s *seam.Settings) { s.Conc = "0,16" },
			"CONC is 0,16: give positive whole numbers, comma separated, each at most once"),
		Entry("an empty concurrency", sweepSettings, func(s *seam.Settings) { s.Conc = "16,,256" },
			"CONC is 16,,256: give positive whole numbers, comma separated, each at most once"),
		Entry("a concurrency twice, whose judged floor runs the report would refuse", sweepSettings, func(s *seam.Settings) { s.Conc = "64,64" },
			"CONC is 64,64: give positive whole numbers, comma separated, each at most once"),
		Entry("a 4 MiB concurrency the report pairs with no floor", sweepSettings, func(s *seam.Settings) { large := "1,8"; s.LargeConc = &large },
			"BENCH_LARGE_CONC is 1,8: the 4 MiB cells run only at 1, 4 and 16, each at most once, where the report pairs them with their floor; empty skips them"),
		Entry("a 4 MiB concurrency twice", sweepSettings, func(s *seam.Settings) { large := "16,16"; s.LargeConc = &large },
			"BENCH_LARGE_CONC is 16,16: the 4 MiB cells run only at 1, 4 and 16, each at most once, where the report pairs them with their floor; empty skips them"),
		Entry("zero write4m objects", sweepSettings, func(s *seam.Settings) { s.Write4mObjects = "0" },
			"BENCH_WRITE4M_OBJECTS is 0: give a positive whole number of at most 10 digits"),
		Entry("the sweep's BENCH_TIME in the limiter re-run", limitedSettings, func(s *seam.Settings) { s.BenchTime = "20s" },
			"BENCH_TIME applies only to the sweep, not with BENCH_LIMITER=1"),
		Entry("the sweep's CONC in the limiter re-run", limitedSettings, func(s *seam.Settings) { s.Conc = "64" },
			"CONC applies only to the sweep, not with BENCH_LIMITER=1"),
		Entry("the sweep's empty BENCH_LARGE_CONC in the limiter re-run", limitedSettings, func(s *seam.Settings) { large := ""; s.LargeConc = &large },
			"BENCH_LARGE_CONC applies only to the sweep, not with BENCH_LIMITER=1"),
		Entry("the sweep's floor image in the limiter re-run", limitedSettings, func(s *seam.Settings) { s.FloorImage = "quay.io/ceph/ceph:v19.2.6" },
			"BENCH_FLOOR_IMAGE applies only to the sweep, not with BENCH_LIMITER=1"),
	)

	It("names the release's ceph.conf when it refuses another", func() {
		s := sweepSettings()
		s.CephConf = "/elsewhere/ceph.conf"
		_, err := s.Check()
		Expect(err).To(MatchError("RGW_GO_TEST_CEPH_CONF is /elsewhere/ceph.conf, but the squid cluster is reached through " +
			filepath.Join(repo, "hack/rooket/out/squid/ceph.conf") + ": unset it"))
	})

	DescribeTable("refuses a duration",
		func(name, value string) {
			s := sweepSettings()
			switch name {
			case "BENCH_TIMEOUT":
				s.Timeout = value
			case "BENCH_TIME":
				s.BenchTime = value
			case "BENCH_FLOOR_TIMEOUT":
				s.FloorTimeout = value
			}
			_, err := s.Check()
			Expect(err).To(MatchError(name + " " + value + " is not a duration in h, m and s from 1 s to 99999999 s"))
		},
		Entry("of zero seconds, which would stop every process at once", "BENCH_TIMEOUT", "0"),
		Entry("of zero in units", "BENCH_TIMEOUT", "0s"),
		Entry("of zero in every unit", "BENCH_TIMEOUT", "0h0m0s"),
		Entry("that is no duration", "BENCH_TIMEOUT", "bogus"),
		Entry("in fractions", "BENCH_TIMEOUT", "1.5s"),
		Entry("in milliseconds", "BENCH_TIMEOUT", "500ms"),
		Entry("written as a count", "BENCH_TIMEOUT", "100x"),
		Entry("over 99999999 s", "BENCH_TIMEOUT", "27778h"),
		Entry("of 100000000 plain seconds", "BENCH_TIMEOUT", "100000000"),
		Entry("with a 10-digit part", "BENCH_TIMEOUT", "1000000000s"),
		Entry("that would overflow the arithmetic", "BENCH_TIMEOUT", "99999999999999999999"),
		Entry("in units out of order", "BENCH_TIMEOUT", "5s1m"),
		Entry("as a benchtime in fractions", "BENCH_TIME", "1.5s"),
		Entry("as a benchtime in milliseconds", "BENCH_TIME", "500ms"),
		Entry("as a benchtime written as a count", "BENCH_TIME", "100x"),
		Entry("as a benchtime of zero, which go test refuses", "BENCH_TIME", "0s"),
		Entry("as a floor timeout of no duration", "BENCH_FLOOR_TIMEOUT", "5 minutes"),
		Entry("as a floor timeout of zero", "BENCH_FLOOR_TIMEOUT", "0"),
	)
})

// runStart is the time the specs' runs start.
var runStart = time.Date(2026, 10, 12, 21, 5, 9, 0, time.UTC)

var _ = Describe("Config", func() {
	limiter := func(edit func(*seam.Config)) seam.Config {
		c := seam.Config{Release: "tentacle", Repo: "/repo", Limiter: true, InflightBytes: 104857600, OverObjects: 640, MinAvailGiB: 5}
		edit(&c)
		return c
	}
	sweep := func(edit func(*seam.Config)) seam.Config {
		c := seam.Config{
			Release: "squid", Repo: "/repo", OverObjects: 640, MinAvailGiB: 5,
			BenchTime: 20 * time.Second, FloorSeconds: 20, Conc: []int{1, 16, 64, 256, 512}, LargeConc: []int{1, 4, 16},
			Write4mObjects: 1000, Engine: "podman",
		}
		edit(&c)
		return c
	}

	DescribeTable("Dir",
		func(c seam.Config, want string) {
			Expect(c.Dir(runStart)).To(Equal(want))
		},
		Entry("of the sweep", sweep(func(*seam.Config) {}), "/repo/hack/bench/out/squid/seam-20261012T210509Z"),
		Entry("of the limiter re-run at the default bound", limiter(func(*seam.Config) {}), "/repo/hack/bench/out/tentacle/seam-20261012T210509Z-limited"),
		Entry("of the limiter re-run at another bound", limiter(func(c *seam.Config) { c.InflightBytes = 67108864 }),
			"/repo/hack/bench/out/tentacle/seam-20261012T210509Z-limited-67108864"),
	)

	It("names the directory by UTC time", func() {
		local := runStart.In(time.FixedZone("UTC-7", -7*3600))
		Expect(limiter(func(*seam.Config) {}).Dir(local)).To(HaveSuffix("seam-20261012T210509Z-limited"))
	})

	DescribeTable("Limit",
		func(c seam.Config, cell []string, want time.Duration) {
			Expect(c.Limit(cell)).To(Equal(want))
		},
		Entry("a tenth of a second an object, plus five minutes", limiter(func(*seam.Config) {}), limiter(func(*seam.Config) {}).CellArgs(), 364*time.Second),
		Entry("in quick mode", limiter(func(c *seam.Config) { c.OverObjects = 128 }), []string{"-test.benchtime=128x"}, 312*time.Second),
		Entry("rounding a tenth down", sweep(func(*seam.Config) {}), []string{"-shapes=write4m", "-test.benchtime=649x"}, 364*time.Second),
		Entry("ten times BENCH_TIME for a cell bounded by time, plus five minutes", sweep(func(*seam.Config) {}), []string{"-shapes=read4k", "-conc=16"}, 500*time.Second),
		Entry("BENCH_TIMEOUT when set", sweep(func(c *seam.Config) { c.Timeout = 10 * time.Minute }), []string{"-test.benchtime=640x"}, 600*time.Second),
	)

	It("passes the limiter's process the cell, its mode, its results and a -test.timeout past the watchdog", func() {
		c := limiter(func(*seam.Config) {})
		Expect(c.BenchArgs("pipe", "/out/overbudget-pipe.jsonl", c.CellArgs())).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "454s",
			"-mode=pipe", "-results=/out/overbudget-pipe.jsonl",
			"-shapes=write4m", "-conc=64", "-budget-bytes=0", "-inflight-bytes=104857600", "-test.benchtime=640x",
		}))
	})

	It("passes the sweep's process BENCH_TIME in whole seconds before the cell, which a count-bounded cell overrides", func() {
		c := sweep(func(c *seam.Config) { c.BenchTime = 90 * time.Second })
		Expect(c.BenchArgs("sync", "/out/seam-sync.jsonl", []string{"-shapes=write4m", "-conc=4", "-test.benchtime=1000x"})).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "490s", "-test.benchtime", "90s",
			"-mode=sync", "-results=/out/seam-sync.jsonl", "-shapes=write4m", "-conc=4", "-test.benchtime=1000x",
		}))
	})

	It("passes a profile's process no results file", func() {
		c := sweep(func(*seam.Config) {})
		Expect(c.BenchArgs("pipe", "", []string{"-shapes=read4k", "-conc=256", "-test.cpuprofile=/out/cpu-pipe-read4k-256.pprof"})).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "590s", "-test.benchtime", "20s",
			"-mode=pipe", "-shapes=read4k", "-conc=256", "-test.cpuprofile=/out/cpu-pipe-read4k-256.pprof",
		}))
	})

	DescribeTable("WritePlan",
		func(c seam.Config, want func() string) {
			var b bytes.Buffer
			c.WritePlan(&b)
			Expect(b.String()).To(Equal(want()))
		},
		Entry("of the sweep at the defaults", sweep(func(*seam.Config) {}),
			func() string { return golden("plan-sweep.txt") }),
		Entry("of the sweep in quick mode", sweep(func(c *seam.Config) {
			c.BenchTime, c.FloorSeconds, c.Conc, c.LargeConc, c.Write4mObjects, c.OverObjects = 5*time.Second, 5, []int{16, 256}, []int{16}, 100, 128
		}), func() string { return golden("plan-sweep-quick.txt") }),
		Entry("of the limiter re-run at the defaults", limiter(func(*seam.Config) {}),
			func() string { return plan("tentacle", "104857600", "-limited", "640", "364") }),
		Entry("of the limiter re-run at another bound", limiter(func(c *seam.Config) { c.InflightBytes = 67108864 }),
			func() string { return plan("tentacle", "67108864", "-limited-67108864", "640", "364") }),
		Entry("of the limiter re-run in quick mode", limiter(func(c *seam.Config) { c.OverObjects = 128 }),
			func() string { return plan("tentacle", "104857600", "-limited", "128", "312") }),
		Entry("of the limiter re-run under BENCH_TIMEOUT", limiter(func(c *seam.Config) { c.Timeout = 90 * time.Minute }),
			func() string { return plan("tentacle", "104857600", "-limited", "640", "5400") }),
	)

	It("plans no 4 MiB cell when BENCH_LARGE_CONC is empty", func() {
		var b bytes.Buffer
		sweep(func(c *seam.Config) { c.LargeConc = nil }).WritePlan(&b)
		Expect(b.String()).NotTo(SatisfyAny(ContainSubstring("read4m"), ContainSubstring("write4m at"), ContainSubstring("-shapes=write4m -conc=1 ")))
		Expect(b.String()).To(ContainSubstring("bench callback: -shapes=write4m -conc=64 -budget-bytes=0"), "the over-budget cells still run")
	})

	It("plans every floor run and limit under BENCH_FLOOR_TIMEOUT and BENCH_TIMEOUT", func() {
		var b bytes.Buffer
		sweep(func(c *seam.Config) { c.FloorTimeout, c.Timeout = 25*time.Minute, time.Hour }).WritePlan(&b)
		lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")[1:]
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "floor "):
				Expect(line).To(HaveSuffix("(limit 1500 s)"))
			case strings.HasPrefix(line, "bench "):
				Expect(line).To(HaveSuffix("(limit 3600 s)"))
			}
		}
	})
})

// plan is the limiter re-run's plan for a release, byte bound, directory
// suffix, object count and limit.
func plan(release, bytes, suffix, objects, limit string) string {
	var b strings.Builder
	b.WriteString("seam: the " + release + " limiter re-run at " + bytes + " B, into hack/bench/out/" + release + "/seam-<UTC time>" + suffix + "/\n")
	for _, mode := range []string{"sync", "callback", "pipe"} {
		b.WriteString("wait for 5 GiB available in rgw-go-test\n")
		b.WriteString("bench " + mode + ": -shapes=write4m -conc=64 -budget-bytes=0 -inflight-bytes=" + bytes +
			" -test.benchtime=" + objects + "x -> overbudget-" + mode + ".jsonl (limit " + limit + " s)\n")
	}
	return b.String()
}
