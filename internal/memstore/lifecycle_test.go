package memstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("lifecycle index", func() {
	var store *memstore.Store
	BeforeEach(func() { store, _ = newStore() })

	It("accepts a bucket's entry and its removal, present or not", func(ctx SpecContext) {
		rec := &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Name: "photos", Marker: "m"}}}
		Expect(store.RemoveEntry(ctx, rec)).To(Succeed())
		Expect(store.SetEntry(ctx, rec)).To(Succeed())
		Expect(store.SetEntry(ctx, rec)).To(Succeed())
		Expect(store.RemoveEntry(ctx, rec)).To(Succeed())
	})
})
