package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var (
	_ op.UsageReader      = (*opfakes.FakeUsageReader)(nil)
	_ op.BucketAdminStore = (*opfakes.FakeBucketAdminStore)(nil)
	_ op.RealmStore       = (*opfakes.FakeRealmStore)(nil)
	_ op.AccountStore     = (*opfakes.FakeAccountStore)(nil)
)

var _ = Describe("admin store contract", func() {
	It("reaches each admin store's fake through the Env", func(ctx SpecContext) {
		accounts := new(opfakes.FakeAccountStore)
		accounts.GetAccountByNameReturns(&op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001"}}, nil)
		usage := new(opfakes.FakeUsageReader)
		usage.ReadUsageReturns([]op.UsageRecord{{User: "alice"}}, true, nil)
		buckets := new(opfakes.FakeBucketAdminStore)
		buckets.IndexStatsReturns(op.BucketIndexStats{Ver: "0#1"}, nil)
		realms := new(opfakes.FakeRealmStore)
		realms.GetRealmReturns(meta.Realm{ID: "r1"}, nil)
		env := &op.Env{Accounts: accounts, UsageReader: usage, BucketAdmin: buckets, Realms: realms}

		acct, err := env.Accounts.GetAccountByName(ctx, "t", "acme")
		Expect(err).NotTo(HaveOccurred())
		Expect(acct.Info.ID).To(Equal("RGW00000000000000001"))
		recs, truncated, err := env.UsageReader.ReadUsage(ctx, "alice", "", 0, 1, 10, &op.UsageIter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(recs).To(Equal([]op.UsageRecord{{User: "alice"}}))
		Expect(truncated).To(BeTrue())
		stats, err := env.BucketAdmin.IndexStats(ctx, &op.BucketRecord{})
		Expect(err).NotTo(HaveOccurred())
		Expect(stats.Ver).To(Equal("0#1"))
		realm, err := env.Realms.GetRealm(ctx, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(realm.ID).To(Equal("r1"))
	})
})
