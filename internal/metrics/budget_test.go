package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/metrics"
)

// budgetReporter is a metrics.MemoryBudgetReporter returning the stats a
// spec last set.
type budgetReporter struct {
	mu sync.Mutex
	s  driver.MemoryBudgetStats
}

func (r *budgetReporter) set(s driver.MemoryBudgetStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = s
}

func (r *budgetReporter) MemoryBudgetStats() driver.MemoryBudgetStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s
}

var _ = Describe("the memory budget's metrics", func() {
	var (
		reg *metrics.Registry
		srv *httptest.Server
	)
	BeforeEach(func() {
		reg = metrics.New()
		srv = httptest.NewServer(reg.Handler())
		DeferCleanup(srv.Close)
	})

	It("reports the budget's size, holdings, waits and slowdowns on every scrape", func(ctx SpecContext) {
		fake := &budgetReporter{}
		reg.WatchMemoryBudget(fake)
		fake.set(driver.MemoryBudgetStats{
			Size: 800, HoldCap: 400, PrefetchCap: 200,
			PieceHeld: 300, HoldHeld: 100, PrefetchHeld: 50,
			PieceWaiting: 2, HoldWaiting: 1,
			PieceWaits: 7, HoldWaits: 3,
			PieceWaited: 1500 * time.Millisecond, HoldWaited: 250 * time.Millisecond,
			PieceSlowdowns: 4, HoldSlowdowns: 2,
			PrefetchSkipped: 9,
			FreeListIdle:    48 << 20,
		})
		resp, body := scrape(ctx, srv.URL)
		Expect(resp.StatusCode).To(Equal(http.StatusOK), body)
		for _, line := range []string{
			`rgw_go_memory_budget_bytes{class="total"} 800`,
			`rgw_go_memory_budget_bytes{class="hold"} 400`,
			`rgw_go_memory_budget_bytes{class="prefetch"} 200`,
			`rgw_go_memory_budget_held_bytes{class="piece"} 300`,
			`rgw_go_memory_budget_held_bytes{class="hold"} 100`,
			`rgw_go_memory_budget_held_bytes{class="prefetch"} 50`,
			`rgw_go_memory_budget_waiting{class="piece"} 2`,
			`rgw_go_memory_budget_waiting{class="hold"} 1`,
			`rgw_go_memory_budget_waits_total{class="piece"} 7`,
			`rgw_go_memory_budget_waits_total{class="hold"} 3`,
			`rgw_go_memory_budget_wait_seconds_total{class="piece"} 1.5`,
			`rgw_go_memory_budget_wait_seconds_total{class="hold"} 0.25`,
			`rgw_go_memory_budget_slowdowns_total{class="piece"} 4`,
			`rgw_go_memory_budget_slowdowns_total{class="hold"} 2`,
			`rgw_go_memory_budget_prefetch_skipped_total 9`,
			`rgw_go_memory_budget_free_list_bytes 5.0331648e+07`,
		} {
			Expect(body).To(ContainSubstring("\n"+line+"\n"), "%s", line)
		}
		Expect(body).To(ContainSubstring("# HELP rgw_go_memory_budget_free_list_bytes Bytes of buffers of object data the free list keeps idle for reuse, at most the memory budget's total; they are live heap the GC cannot reclaim until the list's idle trim drops them.\n" +
			"# TYPE rgw_go_memory_budget_free_list_bytes gauge\n"))

		fake.set(driver.MemoryBudgetStats{Size: 800, PieceHeld: 600})
		_, body = scrape(ctx, srv.URL)
		Expect(body).To(ContainSubstring("\n" + `rgw_go_memory_budget_held_bytes{class="piece"} 600` + "\n"))
	})

	It("reports nothing of a budget before one is watched, or after a nil one is", func(ctx SpecContext) {
		_, body := scrape(ctx, srv.URL)
		Expect(body).NotTo(ContainSubstring("rgw_go_memory_budget_"))
		reg.WatchMemoryBudget(&budgetReporter{})
		_, body = scrape(ctx, srv.URL)
		Expect(body).To(ContainSubstring("rgw_go_memory_budget_bytes"))
		reg.WatchMemoryBudget(nil)
		resp, body := scrape(ctx, srv.URL)
		Expect(resp.StatusCode).To(Equal(http.StatusOK), body)
		Expect(body).NotTo(ContainSubstring("rgw_go_memory_budget_"))
	})
})
