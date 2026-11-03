package budgetoracle_test

import (
	"os"

	"github.com/ceph/go-ceph/rados"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/budgetoracle"
)

// setSkipEnv sets SkipEnv to v, or unsets it for "", until the spec ends.
func setSkipEnv(v string) {
	old, had := os.LookupEnv(budgetoracle.SkipEnv)
	DeferCleanup(func() {
		if had {
			Expect(os.Setenv(budgetoracle.SkipEnv, old)).To(Succeed())
		} else {
			Expect(os.Unsetenv(budgetoracle.SkipEnv)).To(Succeed())
		}
	})
	if v == "" {
		Expect(os.Unsetenv(budgetoracle.SkipEnv)).To(Succeed())
		return
	}
	Expect(os.Setenv(budgetoracle.SkipEnv, v)).To(Succeed())
}

var _ = Describe("Budget", func() {
	It("returns what calc_op_budget charges a built op: a setxattr its name and value", func() {
		wop := rados.CreateWriteOp()
		DeferCleanup(wop.Release)
		wop.SetXattr("user.a", []byte("xyz"))
		budget, err := budgetoracle.Budget(wop, 1)
		if budgetoracle.Skipped(err) {
			Skip(budgetoracle.SkipEnv + "=skip, and libceph-common does not export Objecter::calc_op_budget")
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(budget).To(Equal(len("user.a") + len("xyz")))
	})

	// A count other than the op's stands in for a layout that has moved: in
	// both, the vector's header does not describe the steps the op holds.
	DescribeTable("refuses, before calling calc_op_budget, an op whose step vector does not hold the steps it was built with",
		func(osdOps int, read bool) {
			var err error
			if read {
				rop := rados.CreateReadOp()
				DeferCleanup(rop.Release)
				rop.Stat()
				_, err = budgetoracle.Budget(rop, osdOps)
			} else {
				wop := rados.CreateWriteOp()
				DeferCleanup(wop.Release)
				wop.Remove()
				_, err = budgetoracle.Budget(wop, osdOps)
			}
			Expect(err).To(MatchError(budgetoracle.ErrLayout))
		},
		Entry("a write op of one step, read as two", 2, false),
		Entry("a write op of one step, read as none", 0, false),
		Entry("a write op, for a negative count", -1, false),
		Entry("a read op of one step, read as two", 2, true),
	)

	DescribeTable("lets a spec skip only for a missing calc_op_budget, and only when SkipEnv says skip",
		func(err error, env string, want bool) {
			setSkipEnv(env)
			Expect(budgetoracle.Skipped(err)).To(Equal(want))
		},
		Entry("a missing symbol, by default", budgetoracle.ErrNotExported, "", false),
		Entry("a missing symbol, with SkipEnv=skip", budgetoracle.ErrNotExported, "skip", true),
		Entry("a missing symbol, with another value", budgetoracle.ErrNotExported, "1", false),
		Entry("a layout mismatch, with SkipEnv=skip", budgetoracle.ErrLayout, "skip", false),
		Entry("no error, with SkipEnv=skip", nil, "skip", false),
	)
})
