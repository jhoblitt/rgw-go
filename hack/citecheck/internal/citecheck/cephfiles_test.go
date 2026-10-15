package citecheck_test

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// reCephTag is a release tag .goreleaser.yaml pins in its env block.
var reCephTag = regexp.MustCompile(`(?m)^\s*-\s*[A-Z]+_CEPH_TAG=(v\S+)\s*$`)

var _ = Describe("The list of ceph's files", func() {
	It("was made at every release the markers and the release pins name, by the current pattern", func() {
		refs, pattern, paths := citecheck.CephFiles()
		pins, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".goreleaser.yaml"))
		Expect(err).NotTo(HaveOccurred())
		matches := reCephTag.FindAllStringSubmatch(string(pins), -1)
		Expect(matches).NotTo(BeEmpty(), ".goreleaser.yaml names no *_CEPH_TAG")
		want := citecheck.MarkTags()
		for _, m := range matches {
			want = append(want, m[1])
		}
		for _, ref := range want {
			Expect(refs).To(ContainElement(ref),
				"the list was made at %s: add %s to CITECHECK_FILES_REFS and run make citecheck-files", strings.Join(refs, " "), ref)
		}
		Expect(pattern).To(Equal(citecheck.CitableBase()),
			"the list's names were chosen by another pattern: run make citecheck-files")
		Expect(slices.IsSorted(paths)).To(BeTrue(), "the list is not sorted: run make citecheck-files")
		Expect(slices.Compact(slices.Clone(paths))).To(HaveLen(len(paths)), "the list repeats a path")
		re := regexp.MustCompile(pattern)
		for _, p := range paths {
			Expect(re.MatchString(path.Base(p))).To(BeTrue(), "the list holds %s, which no plain citation can name", p)
		}
	})

	Describe("citecheck files", func() {
		var (
			ceph           *cephFixture
			stdout, stderr *bytes.Buffer
		)

		BeforeEach(func() {
			ceph = newCephFixture()
			ceph.write("README.md", "# not citable\n")
			ceph.write("src/rgw/rgw_new.cc", "// new\n")
			ceph.commit("v3")
			ceph.git("tag", "v3.0.0")
			stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
		})

		run := func(ctx SpecContext, args ...string) int {
			return citecheck.ExitStatus(citecheck.Run(ctx, args, nil, stdout, stderr))
		}

		const listed = "# The paths of ceph/ceph whose base name a plain citation can name, at each ref below.\n" +
			"# make citecheck-files writes it from a clone, and make citecheck-files-check finds it stale.\n" +
			"# refs: v1.0.0 v3.0.0\n"

		It("writes the citable paths at each ref, once each, sorted", func(ctx SpecContext) {
			out := filepath.Join(GinkgoT().TempDir(), "files.txt")
			Expect(run(ctx, "files", "--ceph", ceph.dir, "--out", out, "v1.0.0", "v3.0.0")).To(Equal(0))
			Expect(stdout.String()).To(Equal("listed 3 paths at v1.0.0 v3.0.0 into " + out + "\n"))
			data, err := os.ReadFile(out)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(listed + "# names: " + citecheck.CitableBase() + "\n" +
				"src/rgw/rgw_fixture.cc\nsrc/rgw/rgw_fixture.h\nsrc/rgw/rgw_new.cc\n"))
		})

		It("passes a list that is the clone's, and fails one that is stale, saying how", func(ctx SpecContext) {
			out := filepath.Join(GinkgoT().TempDir(), "files.txt")
			Expect(run(ctx, "files", "--ceph", ceph.dir, "--out", out, "v1.0.0", "v3.0.0")).To(Equal(0))
			stdout.Reset()
			Expect(run(ctx, "files", "--ceph", ceph.dir, "--check", out, "v1.0.0", "v3.0.0")).To(Equal(0))
			Expect(stdout.String()).To(Equal(out + " lists ceph's citable files at v1.0.0 v3.0.0\n"))

			stdout.Reset()
			Expect(run(ctx, "files", "--ceph", ceph.dir, "--check", out, "v1.0.0")).To(Equal(1))
			Expect(stdout.String()).To(Equal(out + " is stale: it was listed at v1.0.0 v3.0.0, not v1.0.0; it has 1 path the " +
				"clone lacks, such as src/rgw/rgw_new.cc: rewrite it with make citecheck-files\n"))
		})

		DescribeTable("--check fails a list that differs from the clone's only in",
			func(ctx SpecContext, edit func(string) string, why string) {
				out := filepath.Join(GinkgoT().TempDir(), "files.txt")
				Expect(run(ctx, "files", "--ceph", ceph.dir, "--out", out, "v1.0.0", "v3.0.0")).To(Equal(0))
				data, err := os.ReadFile(out)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(out, []byte(edit(string(data))), 0o600)).To(Succeed())
				stdout.Reset()
				Expect(run(ctx, "files", "--ceph", ceph.dir, "--check", out, "v1.0.0", "v3.0.0")).To(Equal(1))
				Expect(stdout.String()).To(Equal(out + " is stale: " + why + ": rewrite it with make citecheck-files\n"))
			},
			Entry("the pattern its names were chosen by",
				func(s string) string {
					return strings.Replace(s, "# names: "+citecheck.CitableBase(), "# names: ^x$", 1)
				},
				"its names were chosen by ^x$, not "+citecheck.CitableBase()),
			Entry("its comments", func(s string) string { return "# a comment of its own\n" + s },
				"its comments or its order differ"),
			Entry("its order", func(s string) string {
				return strings.Replace(s, "src/rgw/rgw_fixture.h\nsrc/rgw/rgw_new.cc\n", "src/rgw/rgw_new.cc\nsrc/rgw/rgw_fixture.h\n", 1)
			}, "its comments or its order differ"),
		)

		It("--check refuses a list whose header names no refs, with status 2", func(ctx SpecContext) {
			out := filepath.Join(GinkgoT().TempDir(), "files.txt")
			Expect(os.WriteFile(out, []byte("src/rgw/rgw_fixture.cc\n"), 0o600)).To(Succeed())
			err := citecheck.Run(ctx, []string{"files", "--ceph", ceph.dir, "--check", out, "v1.0.0"}, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(ContainSubstring("the list of ceph's files names no refs or no pattern in its header")))
		})

		DescribeTable("refuses a command line it cannot act on, with status 2",
			func(ctx SpecContext, want string, args ...string) {
				err := citecheck.Run(ctx, append([]string{"files", "--ceph", ceph.dir}, args...), nil, stdout, stderr)
				Expect(citecheck.ExitStatus(err)).To(Equal(2))
				Expect(err).To(MatchError(ContainSubstring(want)))
			},
			Entry("no ref", "requires at least 1 arg", "--out", "x.txt"),
			Entry("neither --out nor --check", "at least one of the flags in the group [out check] is required", "v1.0.0"),
			Entry("a branch", "ref main is refs/heads/main, a branch or symbolic ref", "--out", "x.txt", "main"),
		)
	})
})
