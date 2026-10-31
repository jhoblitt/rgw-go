package citecheck_test

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

var _ = Describe("--form-only and a link at a branch", func() {
	var stdout *bytes.Buffer

	BeforeEach(func() {
		stdout = &bytes.Buffer{}
	})

	formOnly := func(ctx SpecContext, file string) error {
		return citecheck.Run(ctx, []string{"--form-only", file}, nil, stdout, &bytes.Buffer{})
	}

	DescribeTable("fails a markdown link whose ref is a branch, which moves",
		func(ctx SpecContext, ref string) {
			link := "https://github.com/ceph/ceph/blob/" + ref + "/src/rgw/rgw_op.cc#L10"
			md := markdown("plan.md", "radosgw still sends it ("+link+").\n")
			Expect(citecheck.ExitStatus(formOnly(ctx, md))).To(Equal(1))
			Expect(stdout.String()).To(Equal(md + ":1: " + link + ": ref " + ref + " names a branch, which moves: " +
				"cite a tag or a commit id\ncitations checked: 1, failed: 1\n"))
		},
		Entry("main", "main"),
		Entry("master", "master"),
		Entry("HEAD", "HEAD"),
		Entry("a release branch by its codename", "squid"),
	)

	It("fails a Go comment's link whose ref is a branch", func(ctx SpecContext) {
		link := "https://github.com/ceph/ceph/blob/main/src/rgw/rgw_op.cc#L10"
		file := filepath.Join(GinkgoT().TempDir(), "op.go")
		Expect(os.WriteFile(file, []byte("package op\n\n// Run answers as radosgw does ("+link+").\nfunc Run() {}\n"),
			0o600)).To(Succeed())
		Expect(citecheck.ExitStatus(formOnly(ctx, file))).To(Equal(1))
		Expect(stdout.String()).To(ContainSubstring(":3: " + link + ": ref main names a branch, which moves: " +
			"cite a tag or a commit id\n"))
	})

	DescribeTable("passes a link whose ref is a tag or a commit id",
		func(ctx SpecContext, ref string) {
			link := "https://github.com/ceph/ceph/blob/" + ref + "/src/rgw/rgw_op.cc#L10"
			Expect(formOnly(ctx, markdown("plan.md", "radosgw sends it ("+link+").\n"))).To(Succeed())
			Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0\n"))
		},
		Entry("a tag", "v20.2.4"),
		Entry("a commit id", "8aa24c51928768192775ab5220ce0621f5e60355"),
	)
})
