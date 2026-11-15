package selection_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the Makefile's s3-tests records", func() {
	DescribeTable("carry the digest of the run's .freeze file as the meta pytest_freeze",
		func(ctx SpecContext, args []string, freeze string) {
			got := records(makeDryRun(ctx, nil, args...))
			Expect(got).To(HaveLen(1))
			Expect(got[0]).To(ContainSubstring("sha256sum hack/s3tests/out/" + freeze + " | cut -c1-12"))
			Expect(got[0]).To(MatchRegexp(` --meta pytest_freeze=(\$freeze|\$\(sha256sum hack/s3tests/out/` + freeze + ` \| cut -c1-12\)) `))
		},
		Entry("make s3tests-parity's result, the candidate the comparison holds to the ledger's venv",
			[]string{"s3tests-parity", "RELEASE=squid"}, "squid-rgw-go-parity.freeze"),
		Entry("make s3tests-record's ledger entries, from the second run",
			[]string{"s3tests-record", "RELEASE=squid"}, "squid-radosgw-base2.freeze"),
		Entry("make s3tests-record-tests's ledger entries, from the second run",
			[]string{"s3tests-record-tests", "RELEASE=tentacle", "TESTS=t.txt"}, "tentacle-radosgw-tests2.freeze"),
	)
})

var _ = Describe("the Makefile's s3-tests runs", func() {
	// baselines returns the S3TESTS_BASELINE each command of make -n names.
	baselines := func(ctx SpecContext, args ...string) []string {
		var got []string
		for _, c := range makeDryRun(ctx, nil, args...) {
			if _, rest, ok := strings.Cut(c, "S3TESTS_BASELINE="); ok {
				got = append(got, strings.Fields(rest)[0])
			}
		}
		return got
	}

	It("give run.sh the committed ledger of the release to compare a run of rgw-go with", func(ctx SpecContext) {
		Expect(baselines(ctx, "s3tests-parity", "RELEASE=tentacle")).To(Equal([]string{"test/s3tests/baseline/tentacle.json"}))
	})

	It("give run.sh the ledger BASELINE names, such as the copy a record has just appended to", func(ctx SpecContext) {
		Expect(baselines(ctx, "s3tests-parity", "RELEASE=squid", "BASELINE=/run/recorded/squid.json")).
			To(Equal([]string{"/run/recorded/squid.json"}))
	})
})
