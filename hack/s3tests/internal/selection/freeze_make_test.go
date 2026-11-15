package selection_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the Makefile's s3-tests ledger records", func() {
	DescribeTable("carry the digest of the run's .freeze file as the meta pytest_freeze",
		func(ctx SpecContext, args []string, freeze string) {
			got := records(makeDryRun(ctx, nil, args...))
			Expect(got).To(HaveLen(1))
			Expect(got[0]).To(ContainSubstring("sha256sum hack/s3tests/out/" + freeze + " | cut -c1-12"))
			Expect(got[0]).To(MatchRegexp(` --meta pytest_freeze=(\$freeze|\$\(sha256sum hack/s3tests/out/` + freeze + ` \| cut -c1-12\)) `))
		},
		Entry("make s3tests-record's ledger entries, from the second run",
			[]string{"s3tests-record", "RELEASE=squid"}, "squid-radosgw-base2.freeze"),
		Entry("make s3tests-record-tests's ledger entries, from the second run",
			[]string{"s3tests-record-tests", "RELEASE=tentacle", "TESTS=t.txt"}, "tentacle-radosgw-tests2.freeze"),
	)
})

var _ = Describe("the Makefile's s3-tests candidate", func() {
	// diffs returns the commands of make -n that run bin/parity diff.
	diffs := func(commands []string) []string {
		var out []string
		for _, c := range commands {
			if strings.Contains(c, "bin/parity diff ") {
				out = append(out, c)
			}
		}
		return out
	}

	It("is recorded from the run's .freeze file, which gives record the venv's digest, its package lines' and its Python's minor version", func(ctx SpecContext) {
		got := records(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=squid"))
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(ContainSubstring(" --freeze hack/s3tests/out/squid-rgw-go-parity.freeze "))
		Expect(got[0]).NotTo(ContainSubstring("pytest_freeze"), "record sets it from the file")
		Expect(got[0]).NotTo(ContainSubstring("sha256sum"))
	})

	It("is compared under the constraints the venv was built under, so that another patch release of its Python passes", func(ctx SpecContext) {
		for _, set := range []string{"pr", "gate"} {
			got := diffs(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=tentacle", "SET="+set))
			Expect(got).To(HaveLen(1), set)
			Expect(got[0]).To(ContainSubstring(" --constraints hack/s3tests/constraints.txt "), set)
		}
	})

	DescribeTable("leave a ledger's entries without the keys a candidate's record alone carries",
		func(ctx SpecContext, args ...string) {
			got := records(makeDryRun(ctx, nil, args...))
			Expect(got).NotTo(BeEmpty())
			for _, c := range got {
				Expect(c).NotTo(ContainSubstring("--freeze"))
				Expect(c).NotTo(ContainSubstring("pytest_packages"))
				Expect(c).NotTo(ContainSubstring("pytest_python"))
			}
		},
		Entry("make s3tests-record", "s3tests-record", "RELEASE=squid"),
		Entry("make s3tests-record-tests", "s3tests-record-tests", "RELEASE=tentacle", "TESTS=t.txt"),
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
