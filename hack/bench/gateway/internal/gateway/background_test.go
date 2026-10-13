package gateway_test

import (
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
)

// measured reports whether a traced command is a measured phase or an
// httpload run.
func measured(event string) bool {
	return strings.Contains(event, " --jsonfile ") || strings.Contains(event, "/httpload ")
}

// placed writes each event with the background tasks running beside it, S
// for the sampler and P for a CPU profile's fetch.
func placed(events []string, states []background) string {
	var b strings.Builder
	for i, e := range events {
		flags := []byte("--")
		if states[i].sampling {
			flags[0] = 'S'
		}
		if states[i].profiling {
			flags[1] = 'P'
		}
		b.WriteString(string(flags) + " " + e + "\n")
	}
	return b.String()
}

var _ = Describe("the run's background tasks", func() {
	// The sampler starts just before a profile's first quiet and stops just
	// before the profile's bench objects are removed; for rgw-go, the CPU
	// profile's fetch starts just before a profile's last measured command and
	// is waited for just after it.
	DescribeTable("run beside each profile, the sampler from its first quiet to its removals and the CPU profile over its last measured command",
		func(ctx SpecContext, gw string) {
			w := newWorld(gw)
			w.mu.Lock()
			w.probe = true
			w.mu.Unlock()
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))).To(Succeed())
			w.mu.Lock()
			events, states, strays := slices.Clone(w.events), slices.Clone(w.states), slices.Clone(w.strays)
			w.mu.Unlock()
			Expect(strays).To(BeEmpty(), "reads of the gateway's process or requests of its CPU profile while neither task ran")

			// Where each belongs, read off the trace: the sampler from a profile's
			// first quiet up to its bucket stats, and the fetch beside the last
			// measured command of each of those windows.
			want := make([]background, len(events))
			var windows [][]int
			sampling := false
			for i, e := range events {
				switch {
				case !sampling && e == "sleep 2":
					sampling = true
					windows = append(windows, nil)
				case sampling && strings.Contains(e, "radosgw-admin bucket stats"):
					sampling = false
				}
				want[i].sampling = sampling
				if sampling {
					windows[len(windows)-1] = append(windows[len(windows)-1], i)
				}
			}
			Expect(windows).To(HaveLen(len(gateway.Profiles)), "a sampler a profile")
			if gw == "rgw-go" {
				for _, window := range windows {
					last := -1
					for _, i := range window {
						if measured(events[i]) {
							last = i
						}
					}
					Expect(last).NotTo(Equal(-1), "a measured command in each profile")
					want[last].profiling = true
				}
			}
			Expect(placed(events, states)).To(Equal(placed(events, want)))
		},
		Entry("for radosgw, the sampler only, reading its pod", "radosgw"),
		Entry("for rgw-go, the sampler reading /proc and the CPU profile's fetch", "rgw-go"),
	)
})
