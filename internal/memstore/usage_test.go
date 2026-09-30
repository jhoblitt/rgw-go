package memstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("usage log", func() {
	var store *memstore.Store
	BeforeEach(func() { store, _ = newStore() })

	It("keeps its own copy of a logged entry's payer and hands out copies of it", func(ctx SpecContext) {
		e := op.UsageEntry{Owner: owner("alice"), Payer: owner("carol"), Bucket: "rp", Time: start, Category: "get_obj", Ops: 1, SuccessfulOps: 1}
		want := e
		want.Owner, want.Payer = owner("alice"), owner("carol")
		store.Log(ctx, e)
		e.Payer.User.ID = "mallory"
		Expect(store.Usage()).To(Equal([]op.UsageEntry{want}), "the caller's payer changed after Log")
		store.Usage()[0].Payer.User.ID = "mallory"
		Expect(store.Usage()).To(Equal([]op.UsageEntry{want}), "a payer Usage handed out changed")
	})
})
