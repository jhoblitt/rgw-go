package limited_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/limited/internal/limited"
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
		Expect(limited.DecodePoolAvail([]byte(cephDF), "rgw-go-test")).To(BeEquivalentTo(9286684672))
	})

	It("refuses output that lists no such pool", func() {
		_, err := limited.DecodePoolAvail([]byte(cephDF), "rgw-go-missing")
		Expect(err).To(MatchError("ceph df lists no pool rgw-go-missing"))
	})

	It("refuses a pool with no max_avail rather than reading it as zero", func() {
		_, err := limited.DecodePoolAvail([]byte(`{"pools":[{"name":"rgw-go-test","stats":{}}]}`), "rgw-go-test")
		Expect(err).To(MatchError("ceph df gives pool rgw-go-test no max_avail"))
	})

	It("refuses output that is not JSON", func() {
		_, err := limited.DecodePoolAvail([]byte("Error EACCES: access denied"), "rgw-go-test")
		Expect(err).To(MatchError(ContainSubstring("decoding ceph df")))
	})
})
