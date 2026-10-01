package memstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("accounts", func() {
	const id = "RGW00000000000000001"
	var store *memstore.Store
	BeforeEach(func() { store, _ = newStore() })

	It("adds an account with a fresh version", func(ctx SpecContext) {
		added := store.AddAccount(meta.AccountInfo{ID: id, Tenant: "t1", Name: "acme", MaxBuckets: 7})
		Expect(added.Version.Ver).To(BeEquivalentTo(1), "version")
		Expect(added.Version.Tag).To(MatchRegexp(tagPattern), "tag")
		Expect(added.Mtime).To(Equal(start), "mtime")
		got, err := store.GetAccount(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(added))
		Expect(got.Info.MaxBuckets).To(BeEquivalentTo(7))
	})
	It("hands out copies", func(ctx SpecContext) {
		added := store.AddAccount(meta.AccountInfo{ID: id, Name: "acme"})
		added.Info.Name = "mutated"
		added.Attrs["user.rgw.x"] = []byte("v")
		got, err := store.GetAccount(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		got.Info.Name = "mutated"
		got.Attrs["user.rgw.y"] = []byte("v")
		again, err := store.GetAccount(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Info.Name).To(Equal("acme"), "name")
		Expect(again.Attrs).To(BeEmpty(), "attrs")
	})
	It("names an account by its id, as an ACL naming the account resolves it", func(ctx SpecContext) {
		store.AddAccount(meta.AccountInfo{ID: id, Name: "acme"})
		name, err := store.AccountName(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("acme"))
	})
	It("answers NoSuchEntity for an id it does not hold", func(ctx SpecContext) {
		store.AddAccount(meta.AccountInfo{ID: id, Name: "acme"})
		_, err := store.GetAccount(ctx, "RGW00000000000000002")
		Expect(err).To(MatchError(op.ErrNoSuchEntity), "GetAccount")
		_, err = store.AccountName(ctx, "RGW00000000000000002")
		Expect(err).To(MatchError(op.ErrNoSuchEntity), "AccountName")
	})
})
