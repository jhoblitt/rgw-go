package driver

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("calcPerShard", func() {
	It("is calc_ordered_bucket_list_per_shard (rgw_rados.cc:9578-9611 at v19.2.6, :10499-10532 at v20.2.4)", func() {
		Expect(calcPerShard(0, 0)).To(BeZero(), "0 shards would divide by zero")
		Expect(calcPerShard(1000, 1)).To(BeEquivalentTo(1001), "log(1) is 0")
		Expect(calcPerShard(8, 64)).To(BeEquivalentTo(8), "min_read")
		Expect(calcPerShard(1001, 11)).To(BeEquivalentTo(112), "1 + (91 + 20.89), the quotient 1001/11 an integer division")
	})
})

var _ = Describe("indexHashSource", func() {
	It("is parse_index_hash_source (rgw_rados.cc:9951-9962 at v19.2.6): everything before the second-to-last dot", func() {
		src, ok := indexHashSource("obj.2~upload.meta")
		Expect(ok).To(BeTrue())
		Expect(src).To(Equal("obj"))
		_, ok = indexHashSource("meta")
		Expect(ok).To(BeFalse())
		_, ok = indexHashSource(".a.meta")
		Expect(ok).To(BeFalse(), "a dot at position 0 is refused")
	})
})
