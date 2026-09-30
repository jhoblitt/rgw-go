package driver

import (
	"bytes"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("floorShards", func() {
	It("keeps a positive count and logs nothing", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		Expect(floorShards("rgw_usage_max_shards", 7)).To(Equal(uint32(7)))
		Expect(buf.String()).To(BeEmpty())
	})

	It("uses one for a count that is not positive, logging an error that names the option and the tracker issue", func() {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		Expect(floorShards("rgw_lc_max_objs", -4)).To(Equal(uint32(1)))
		Expect(logRecords(&buf)).To(ConsistOf(And(
			HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("msg", ContainSubstring("80991")),
			HaveKeyWithValue("option", "rgw_lc_max_objs"), HaveKeyWithValue("value", BeNumerically("==", -4)),
		)))
	})

	It("uses 2^32-1 for a count above it", func() {
		Expect(floorShards("rgw_usage_max_shards", math.MaxInt64)).To(Equal(uint32(math.MaxUint32)))
	})
})

var _ = Describe("shardMod", func() {
	It("reduces by the count and never divides by zero", func() {
		Expect(shardMod(17138, 8)).To(Equal(uint32(2)))
		Expect(shardMod(17138, 0)).To(BeZero())
	})
})
