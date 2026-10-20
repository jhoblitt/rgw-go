package memstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("object layout", func() {
	It("answers NotImplemented, as memstore keeps no RADOS layout", func(ctx SpecContext) {
		var store op.LayoutStore = memstore.New(memstore.Config{})
		doc, err := store.ObjectLayout(ctx, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true})
		Expect(err).To(MatchError(op.ErrNotImplemented))
		Expect(doc).To(BeNil())
	})
})
