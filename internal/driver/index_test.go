package driver

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("shardOIDs", func() {
	It("names the shards of every layout generation", func() {
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Name: "a", Marker: "m-a", ID: "m-a"}
		gen := meta.NewIndexLayoutGen()
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a.0"}), "the initializers' single shard")
		gen.Layout.Normal.NumShards = 0
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a"}), "unsharded")
		gen.Layout.Normal.NumShards = 2
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a.0", ".dir.m-a.1"}))
		gen.Gen = 3
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a.3.0", ".dir.m-a.3.1"}), "svc_bi_rados.cc:119-128")
	})
})
