package cli_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// radosgw throttles every request, S3 or admin, through its frontend's one
// scheduler: process_request calls schedule_request once get_op has found
// the op (rgw_process.cc:330-338 at v19.2.6, :332-340 at v20.2.4), the beast
// frontend holds a single scheduler (rgw_asio_frontend.cc:470 and :508-522
// at v19.2.6, :425 and :460-474 at v20.2.4), whose default SimpleThrottler
// answers -EAGAIN past rgw_max_concurrent_requests
// (rgw_dmclock_async_scheduler.h:195-208 at v19.2.6, :194-209 at v20.2.4),
// and process_request sends that as ERR_RATE_LIMITED, 503 SlowDown
// (rgw_common.cc:135 at v19.2.6, :136 at v20.2.4).
var _ = Describe("the gateway's request cap", func() {
	var (
		gw      *cli.Gateway
		s3h     *s3.Handler
		adminH  *admin.Handler
		release chan struct{}
		unblock func()
	)
	BeforeEach(func() {
		zone := new(opfakes.FakeZoneInfo)
		zone.ReleaseReturns(denc.Squid)
		env := &op.Env{HostID: "4107-zone-a-zonegroup", Metrics: op.NopMetrics{}, Zone: zone}
		gw = cli.NewGatewayForTest(env, s3.AnonymousOnly{}, s3.Config{MaxConcurrent: 1, TransIDSuffix: op.TransIDSuffix(4107, "zone-a")})
		s3h, adminH = gw.S3Handler(), gw.AdminHandler("admin")
		release = make(chan struct{})
		var once sync.Once
		unblock = func() { once.Do(func() { close(release) }) }
		DeferCleanup(func() { unblock() })
		// The first request either handler runs waits for unblock; a later
		// one, which the cap should have refused, answers at once.
		var ran atomic.Int32
		wait := func() {
			if ran.Add(1) == 1 {
				<-release
			}
		}
		s3h.Register("list_buckets", func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			wait()
			w.WriteHeader(http.StatusOK)
			return nil
		})
		adminH.Register("get_info", func(_ context.Context, w http.ResponseWriter, r *op.Request, _ admin.Request) error {
			wait()
			admin.WriteEmpty(w, r, http.StatusOK)
			return nil
		})
	})
	// hold serves a request through h until unblock, and returns the channel
	// its status arrives on.
	hold := func(ctx context.Context, h http.Handler, path string) <-chan int {
		done := make(chan int, 1)
		go func() { // stops once release closes, at the latest when the spec ends
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
			done <- rec.Code
		}()
		return done
	}
	serve := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	It("refuses an admin request with 503 SlowDown while S3 requests fill rgw_max_concurrent_requests", func(ctx SpecContext) {
		done := hold(ctx, s3h, "/")
		Eventually(s3h.InFlight).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(1))
		rec := serve(adminH, "/admin/info")
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"SlowDown",`))
		unblock()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(http.StatusOK)))
		Expect(s3h.InFlight()).To(BeZero())
	})

	It("refuses an S3 request with 503 SlowDown while admin requests fill rgw_max_concurrent_requests", func(ctx SpecContext) {
		done := hold(ctx, adminH, "/admin/info")
		Eventually(adminH.InFlight).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(1))
		rec := serve(s3h, "/")
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>SlowDown</Code>"))
		unblock()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(http.StatusOK)))
		Expect(adminH.InFlight()).To(BeZero())
	})
})
