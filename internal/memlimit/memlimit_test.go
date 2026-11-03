package memlimit_test

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"math"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/memlimit"
)

var _ = DescribeTable("ParseRatio takes a share in [0, 1]",
	func(s string, want float64) {
		Expect(memlimit.ParseRatio(s)).To(Equal(want))
	},
	Entry("the default", "0.8", 0.8),
	Entry("0, which sets no limit", "0", 0.0),
	Entry("1, the whole limit", "1", 1.0),
)

var _ = DescribeTable("ParseRatio refuses what is not a share in [0, 1]",
	func(s, want string) {
		_, err := memlimit.ParseRatio(s)
		Expect(err).To(MatchError(want))
	},
	Entry("above 1", "1.5", `"1.5" is outside 0 to 1`),
	Entry("below 0", "-0.1", `"-0.1" is outside 0 to 1`),
	Entry("infinity", "Inf", `"Inf" is outside 0 to 1`),
	Entry("a word", "most", `"most" is not a number`),
	Entry("NaN, which no comparison with a bound refuses", "NaN", `"NaN" is not a number`),
	Entry("nothing", "", `"" is not a number`),
)

var _ = Describe("Sizer.Size", func() {
	var (
		cg    memlimit.Cgroup
		env   map[string]string
		limit int64
		sets  []int64
		logs  *bytes.Buffer
		sizer memlimit.Sizer
	)

	BeforeEach(func() {
		cg = newCgroup()
		env = map[string]string{}
		limit = math.MaxInt64
		sets = nil
		sizer = memlimit.Sizer{
			Cgroup: cg,
			LookupEnv: func(key string) (string, bool) {
				v, ok := env[key]
				return v, ok
			},
			SetLimit: func(n int64) int64 {
				prev := limit
				if n >= 0 {
					sets = append(sets, n)
					limit = n
				}
				return prev
			},
		}
		// slog.SetDefault also points the log package's output at the handler,
		// so that comes back too.
		oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
		DeferCleanup(func() {
			slog.SetDefault(oldLogger)
			log.SetOutput(oldWriter)
			log.SetFlags(oldFlags)
		})
		logs = new(bytes.Buffer)
		slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	})

	// logged is the one line Size logged, decoded, without its time.
	logged := func() map[string]any {
		GinkgoHelper()
		lines := strings.Split(strings.TrimSuffix(logs.String(), "\n"), "\n")
		Expect(lines).To(HaveLen(1), "lines logged")
		var line map[string]any
		Expect(json.Unmarshal([]byte(lines[0]), &line)).To(Succeed())
		delete(line, "time")
		return line
	}

	DescribeTable("sets the runtime's limit to ratio times the cgroup's, rounded down to a byte",
		func(ctx SpecContext, ratio float64, memoryMax string, want int64) {
			layout(cg, "0::/a\n", map[string]string{"a": memoryMax})
			r := sizer.Size(ctx, ratio)
			Expect(r.Reason).NotTo(HaveOccurred())
			Expect(sets).To(Equal([]int64{want}), "limits set")
			Expect(r.Limit).To(Equal(want))
		},
		Entry("0.8 of 2 GiB, 1717986918.4 bytes", 0.8, "2147483648\n", int64(1717986918)),
		Entry("half of 1001 bytes, 500.5 bytes", 0.5, "1001\n", int64(500)),
		Entry("all of it at 1", 1.0, "4096\n", int64(4096)),
		Entry("all of the largest byte count, held at math.MaxInt64 where float64 rounds it past", 1.0,
			"9223372036854775807\n", int64(math.MaxInt64)),
	)

	It("logs the cgroup file, its limit, the ratio and the limit it set in one line", func(ctx SpecContext) {
		layout(cg, "0::/a\n", map[string]string{"a": "2147483648\n"})
		sizer.Size(ctx, 0.8)
		Expect(logged()).To(Equal(map[string]any{
			"level":              "INFO",
			"msg":                "go memory limit set from the cgroup",
			"cgroup_file":        filepath.Join(cg.Mount, "a/memory.max"),
			"cgroup_limit_bytes": float64(2147483648),
			"ratio":              0.8,
			"memory_limit_bytes": float64(1717986918),
		}))
	})

	It("leaves an operator's GOMEMLIMIT in place, reading no cgroup, and logs that it stands", func(ctx SpecContext) {
		env["GOMEMLIMIT"] = "1GiB"
		limit = 1 << 30
		r := sizer.Size(ctx, 0.8)
		Expect(r.Reason).To(MatchError(memlimit.ErrOperatorLimit))
		Expect(sets).To(BeEmpty(), "limits set")
		Expect(r.Limit).To(Equal(int64(1 << 30)))
		Expect(logged()).To(Equal(map[string]any{
			"level":              "INFO",
			"msg":                "go memory limit not set from the cgroup",
			"ratio":              0.8,
			"memory_limit_bytes": float64(1 << 30),
			"reason":             "the operator's GOMEMLIMIT stands",
		}))
	})

	// ForProcess's seams are the fakes above, so neither the test binary's
	// environment nor its limit is read or set.
	DescribeTable("through ForProcess, reads GOMEMLIMIT from the process's environment, where an operator's limit stands",
		func(ctx SpecContext, value string, current int64, wantSets []int64, wantLimit int64, reason types.GomegaMatcher) {
			layout(cg, "0::/a\n", map[string]string{"a": "1000\n"})
			env["GOMEMLIMIT"] = value
			limit = current
			DeferCleanup(memlimit.SetProcess(sizer))
			r := memlimit.ForProcess().Size(ctx, 0.5)
			Expect(sets).To(Equal(wantSets), "limits set")
			Expect(r.Limit).To(Equal(wantLimit))
			Expect(r.Reason).To(reason)
		},
		Entry("a byte count, which stands", "1GiB", int64(1<<30), []int64(nil), int64(1<<30),
			MatchError(memlimit.ErrOperatorLimit)),
		Entry("off, which stands as no limit", "off", int64(math.MaxInt64), []int64(nil), int64(math.MaxInt64),
			MatchError(memlimit.ErrOperatorLimit)),
		Entry("the empty string, which the runtime takes as unset, so the limit is derived", "", int64(math.MaxInt64),
			[]int64{500}, int64(500), Succeed()),
	)

	It("sets no limit at a ratio of 0", func(ctx SpecContext) {
		layout(cg, "0::/a\n", map[string]string{"a": "1000\n"})
		r := sizer.Size(ctx, 0)
		Expect(r.Reason).To(MatchError(memlimit.ErrDisabled))
		Expect(sets).To(BeEmpty(), "limits set")
		Expect(logged()).To(HaveKeyWithValue("reason", "the memory limit ratio is 0"))
	})

	DescribeTable("sets no limit and logs why at info where no cgroup v2 limit covers the process",
		func(ctx SpecContext, self string, maxes map[string]string, want error) {
			layout(cg, self, maxes)
			r := sizer.Size(ctx, 0.8)
			Expect(r.Reason).To(MatchError(want))
			Expect(sets).To(BeEmpty(), "limits set")
			Expect(r.Limit).To(Equal(int64(math.MaxInt64)))
			line := logged()
			Expect(line).To(HaveKeyWithValue("level", "INFO"))
			Expect(line).To(HaveKeyWithValue("reason", r.Reason.Error()))
		},
		Entry("cgroup v1", "12:memory:/a\n", map[string]string{"a": "1000\n"}, memlimit.ErrNoCgroupV2),
		Entry("every memory.max max", "0::/a\n", map[string]string{"a": "max\n"}, memlimit.ErrNoLimit),
	)

	It("sets no limit and warns, naming the file and why, for a malformed memory.max", func(ctx SpecContext) {
		layout(cg, "0::/a\n", map[string]string{"a": "lots\n"})
		r := sizer.Size(ctx, 0.8)
		Expect(sets).To(BeEmpty(), "limits set")
		line := logged()
		Expect(line).To(HaveKeyWithValue("level", "WARN"))
		Expect(line).To(HaveKeyWithValue("reason", And(
			ContainSubstring(filepath.Join(cg.Mount, "a/memory.max")), ContainSubstring(`"lots"`))))
		Expect(r.Cgroup).To(BeZero())
	})
})
