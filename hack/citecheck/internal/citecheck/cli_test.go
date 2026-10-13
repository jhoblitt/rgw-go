package citecheck_test

import (
	"bytes"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// holding cites two lines that hold at v1.0.0, one in each form.
const holding = "# Plan\n\nRead::prepare is `src/rgw/rgw_fixture.cc:6-13@v1.0.0#Store::Object::Read::prepare`;\n" +
	"Write::prepare is [here](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L15-L19).\n"

var _ = Describe("Run", func() {
	var (
		ceph           *cephFixture
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		ceph = newCephFixture()
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	run := func(ctx SpecContext, args ...string) int {
		return citecheck.ExitStatus(citecheck.Run(ctx, args, nil, stdout, stderr))
	}

	It("prints only the count and exits 0 when every citation holds", func(ctx SpecContext) {
		Expect(run(ctx, "--ceph", ceph.dir, markdown("plan.md", holding))).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 2, failed: 0\n"))
	})

	It("prints FILE:LINE, the citation and its reason for each that fails, then the count, and exits 1", func(ctx SpecContext) {
		good := markdown("good.md", holding)
		bad := markdown("bad.md", "Moved:\n\n- `src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare`\n"+
			"- see https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_nope.cc#L3.\n")
		err := citecheck.Run(ctx, []string{"--ceph", ceph.dir, good, bad}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(1))
		Expect(err).To(MatchError("2 of 4 citations failed"))
		Expect(stdout.String()).To(Equal(
			bad + ":3: src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare: " +
				"the cited lines do not hold prepare, and no block enclosing them is headed by it\n" +
				bad + ":4: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_nope.cc#L3: " +
				"path src/rgw/rgw_nope.cc does not exist at v1.0.0\n" +
				"citations checked: 4, failed: 2\n"))
	})

	It("passes a file with no citations", func(ctx SpecContext) {
		Expect(run(ctx, "--ceph", ceph.dir, markdown("prose.md", "No citations here.\n"))).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
	})

	It("reads the clone from CITECHECK_CEPH", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", ceph.dir)
		Expect(run(ctx, markdown("plan.md", holding))).To(Equal(0))
	})

	It("takes --ceph over CITECHECK_CEPH", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", GinkgoT().TempDir())
		Expect(run(ctx, "--ceph", ceph.dir, markdown("plan.md", holding))).To(Equal(0))
	})

	It("reads the clone from the config file's ceph", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", "")
		cfg := markdown("citecheck.yaml", "ceph: "+ceph.dir+"\n")
		Expect(run(ctx, "--config", cfg, markdown("plan.md", holding))).To(Equal(0))
	})

	It("reads none of the RGW_GO_ settings meant for rgw-go", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", ceph.dir)
		GinkgoT().Setenv("RGW_GO_CEPH_SRC", GinkgoT().TempDir())
		GinkgoT().Setenv("RGW_GO_CONFIG", filepath.Join(GinkgoT().TempDir(), "missing.yaml"))
		GinkgoT().Setenv("RGW_GO_LOG_FORMAT", "xml")
		Expect(run(ctx, markdown("plan.md", holding))).To(Equal(0))
	})

	It("fails a file whose only citations have no path, and exits 1", func(ctx SpecContext) {
		md := markdown("short.md", "At the other release, `:6716-6727@v20.2.4` and `:6059-6070@v19.2.6#RGWGetCORS::execute`.\n")
		Expect(run(ctx, "--ceph", ceph.dir, md)).To(Equal(1))
		Expect(stdout.String()).To(Equal(
			md + ":1: :6716-6727@v20.2.4: the citation has no path: write the file's path from ceph's repository root " +
				"before \":6716-6727@v20.2.4\"\n" +
				md + ":1: :6059-6070@v19.2.6#RGWGetCORS::execute: the citation has no path: write the file's path from " +
				"ceph's repository root before \":6059-6070@v19.2.6#RGWGetCORS::execute\"\n" +
				"citations checked: 2, failed: 2\n"))
	})

	DescribeTable("a refused command line exits 2 and checks nothing",
		func(ctx SpecContext, want string, args ...string) {
			err := citecheck.Run(ctx, args, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(stdout.String()).To(BeEmpty())
		},
		Entry("no file", "no markdown file given"),
		Entry("an unknown flag", "unknown flag: --nope", "--nope", "plan.md"),
		Entry("an unknown log format", `unknown log format "xml"`, "--log-format", "xml", "plan.md"),
	)

	It("exits 2, before checking any file, when a file cannot be read", func(ctx SpecContext) {
		missing := filepath.Join(GinkgoT().TempDir(), "missing.md")
		err := citecheck.Run(ctx, []string{"--ceph", ceph.dir, markdown("plan.md", holding), missing}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(2))
		Expect(err).To(MatchError(ContainSubstring("reading " + missing)))
		Expect(stdout.String()).To(BeEmpty())
	})

	It("exits 2 when --ceph is not a git repository", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		err := citecheck.Run(ctx, []string{"--ceph", dir, markdown("plan.md", holding)}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(2))
		Expect(err).To(MatchError(HavePrefix(dir + " is not a git repository")))
	})
})
