package seam_test

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

// stat is a /proc/<pid>/stat line for comm with ticks of CPU time, split
// between utime and stime.
func stat(pid int, comm string, ticks int) *fstest.MapFile {
	return &fstest.MapFile{Data: fmt.Appendf(nil, "%d (%s) S 1 %d %d 0 -1 0 0 0 0 0 %d %d 0 0 20 0 1 0 1 0 0\n",
		pid, comm, pid, pid, ticks-ticks/3, ticks/3)}
}

var _ = Describe("Sample", func() {
	var (
		proc fstest.MapFS
		at   time.Time
	)

	BeforeEach(func() {
		proc = fstest.MapFS{"loadavg": {Data: []byte("0.98 0.96 1.31 2/1203 98765\n")}}
		at = runStart.In(time.FixedZone("UTC-7", -7*3600))
	})

	// across replaces the processes when the window's wait runs, as a second
	// reading of /proc would find them.
	across := func(after map[string]*fstest.MapFile) func(context.Context, time.Duration) error {
		return func(_ context.Context, d time.Duration) error {
			Expect(d).To(Equal(time.Second), "the window")
			for name := range proc {
				if strings.HasSuffix(name, "/stat") {
					delete(proc, name)
				}
			}
			maps.Copy(proc, after)
			return nil
		}
	}

	It("names the processes that used 5% of a CPU or more over the window, busiest first", func(ctx SpecContext) {
		proc["100/stat"] = stat(100, "seam.test", 10)
		proc["200/stat"] = stat(200, "kworker/0:1", 1)
		proc["300/stat"] = stat(300, "conmon", 0)
		proc["500/stat"] = stat(500, "bash", 5)
		proc["self"] = &fstest.MapFile{Data: []byte("not a pid")}
		out, err := seam.Sample(ctx, proc, at, time.Second, across(map[string]*fstest.MapFile{
			"100/stat": stat(100, "seam.test", 160), // 150 ticks in 1 s: 150% of a CPU
			"200/stat": stat(200, "kworker/0:1", 5), // 4%: under the floor
			"300/stat": stat(300, "conmon", 6),      // 6%
			"400/stat": stat(400, "rados", 90),      // started inside the window: no baseline
			"500/stat": stat(500, "podman", 95),     // the pid reused by another process
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("== 21:05:09 load 0.98 0.96 1.31\n150.0% seam.test\n6.0% conmon\n"), "the time in UTC, not UTC-7")
	})

	It("names at most 12", func(ctx SpecContext) {
		after := map[string]*fstest.MapFile{}
		for pid := range 14 {
			proc[fmt.Sprintf("%d/stat", pid+1)] = stat(pid+1, fmt.Sprintf("p%02d", pid), 0)
			after[fmt.Sprintf("%d/stat", pid+1)] = stat(pid+1, fmt.Sprintf("p%02d", pid), 10+pid)
		}
		out, err := seam.Sample(ctx, proc, at, time.Second, across(after))
		Expect(err).NotTo(HaveOccurred())
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		Expect(lines).To(HaveLen(13), "the header and 12 processes")
		Expect(lines[1]).To(Equal("23.0% p13"))
		Expect(lines[12]).To(Equal("12.0% p02"))
	})

	It("scales the ticks by the window", func(ctx SpecContext) {
		proc["100/stat"] = stat(100, "seam.test", 0)
		out, err := seam.Sample(ctx, proc, at, 2*time.Second, func(_ context.Context, _ time.Duration) error {
			proc["100/stat"] = stat(100, "seam.test", 30)
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(HaveSuffix("\n15.0% seam.test\n"))
	})
})

var _ = Describe("ParseStat", func() {
	parse := func(stat string) []any {
		comm, ticks, ok := seam.ParseStat(stat)
		return []any{comm, ticks, ok}
	}

	It("reads the name and utime plus stime", func() {
		Expect(parse("4242 (seam.test) S 1 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 9 0 1 0 0")).
			To(Equal([]any{"seam.test", uint64(300), true}))
	})

	It("counts the fields from the last parenthesis, past a name holding spaces and parentheses", func() {
		Expect(parse("7 (tmux: (server) 1) R 1 7 7 0 -1 0 0 0 0 0 12 3 0 0 20 0 1 0 1 0 0")).
			To(Equal([]any{"tmux: (server) 1", uint64(15), true}))
	})

	It("refuses a line cut short", func() {
		_, _, ok := seam.ParseStat("7 (sh) R 1 7")
		Expect(ok).To(BeFalse())
	})
})
