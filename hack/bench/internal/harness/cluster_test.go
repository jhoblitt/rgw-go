package harness_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness/harnessfakes"
)

// cephDF is ceph df -f json's shape, trimmed to what the run reads and a
// neighbor it must skip.
const cephDF = `{
  "stats": {"total_bytes": 10737418240, "total_avail_bytes": 9663676416},
  "pools": [
    {"name": ".mgr", "id": 1, "stats": {"stored": 459280, "max_avail": 9286684000}},
    {"name": "rgw-go-test", "id": 7, "stats": {"stored": 0, "objects": 0, "max_avail": 9286684672}}
  ]
}`

var _ = Describe("DecodePoolAvail", func() {
	It("reads the pool's max_avail", func() {
		Expect(harness.DecodePoolAvail([]byte(cephDF), "rgw-go-test")).To(BeEquivalentTo(9286684672))
	})

	It("refuses output that lists no such pool", func() {
		_, err := harness.DecodePoolAvail([]byte(cephDF), "rgw-go-missing")
		Expect(err).To(MatchError("ceph df lists no pool rgw-go-missing"))
	})

	It("refuses a pool with no max_avail rather than reading it as zero", func() {
		_, err := harness.DecodePoolAvail([]byte(`{"pools":[{"name":"rgw-go-test","stats":{}}]}`), "rgw-go-test")
		Expect(err).To(MatchError("ceph df gives pool rgw-go-test no max_avail"))
	})

	It("refuses output that is not JSON", func() {
		_, err := harness.DecodePoolAvail([]byte("Error EACCES: access denied"), "rgw-go-test")
		Expect(err).To(MatchError(ContainSubstring("decoding ceph df")))
	})
})

var _ = Describe("Cluster", func() {
	var (
		fake *harnessfakes.FakeExecer
		cl   harness.Cluster
	)

	BeforeEach(func() {
		fake = &harnessfakes.FakeExecer{}
		fake.OutputReturns([]byte("10.89.0.34"), nil)
		cl = harness.Cluster{Exec: fake, Rooket: "/opt/bin/rooket", Release: "squid", CallTimeout: time.Minute}
	})

	It("runs rooket's kubectl naming the cluster, within its call timeout", func(ctx SpecContext) {
		out, err := cl.Kubectl(ctx, "-n", "rook-ceph", "get", "pods", "-l", "app=rook-ceph-rgw")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("10.89.0.34"))
		callCtx, c := fake.OutputArgsForCall(0)
		Expect(c).To(Equal(harness.Cmd{
			Name: "/opt/bin/rooket",
			Args: []string{"kubectl", "-n", "rook-ceph", "get", "pods", "-l", "app=rook-ceph-rgw"},
			Env:  []string{"ROOKET_NAME=rgw-go-squid"},
		}))
		deadline, ok := callCtx.Deadline()
		Expect(ok).To(BeTrue(), "the call has a deadline")
		Expect(time.Until(deadline)).To(BeNumerically("<=", time.Minute))
	})

	It("names the cluster of every rooket call through ROOKET_NAME", func() {
		Expect(cl.Env()).To(Equal([]string{"ROOKET_NAME=rgw-go-squid"}))
	})
})
