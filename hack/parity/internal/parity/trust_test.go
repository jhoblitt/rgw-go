package parity_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// forkCommit returns a commit of a fork of ceph: a clone that commits on
// top of ceph's branch and pushes nowhere.
func forkCommit(ceph *gitRepo) string {
	fork := cloneGitRepo(ceph.dir)
	fork.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
	return fork.commit("a fork's test")
}

// branchCommit returns a commit of ceph on a branch other than
// rgw-go-s3tests, leaving rgw-go-s3tests checked out.
func branchCommit(ceph *gitRepo) string {
	ceph.git("checkout", "-q", "-b", "other")
	ceph.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
	c := ceph.commit("another branch's test")
	ceph.git("checkout", "-q", parity.TrustedBranch)
	return c
}

// danglingCommit returns a commit of ceph that no ref holds.
func danglingCommit(ceph *gitRepo, parent string) string {
	return ceph.git("commit-tree", parent+"^{tree}", "-p", parent, "-m", "dangling")
}

const (
	notServed   = "does not serve it: a commit of a fork, or of no branch at all"
	notAncestor = "it is not an ancestor of the branch's tip "
)

var _ = Describe("an s3-tests pin check --base takes in", func() {
	var f *baseFixture
	var trusted string

	BeforeEach(func() {
		f = newBaseFixture()
		trusted = "the history of file://" + f.ceph.dir + "'s " + parity.TrustedBranch + ", where an s3-tests pin must be: "
	})

	DescribeTable("is refused, naming why, unless it is on rgw-go-s3tests",
		func(ctx SpecContext, pick func() string, why string) {
			src := basePinAt(f, pick(), `,"reason":"moved"`)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(squidLedger + ": pin 2 (" + src + ", base): not on " + trusted))
			Expect(stdout).To(ContainSubstring(why))
		},
		Entry("a fork's commit", func() string { return forkCommit(f.ceph) }, notServed),
		Entry("a commit of another branch", func() string { return branchCommit(f.ceph) }, notAncestor),
		Entry("a dangling commit", func() string { return danglingCommit(f.ceph, f.pin) }, notAncestor),
	)

	It("is accepted when an ancestor of the branch's tip", func(ctx SpecContext) {
		f.ceph.put("CHANGES", []byte("one\n"))
		ancestor := f.ceph.commit("one")
		f.ceph.put("CHANGES", []byte("two\n"))
		f.ceph.commit("two")
		basePinAt(f, ancestor, `,"reason":"moved"`)
		status, stdout, stderr, _ := f.check(ctx)
		Expect(status).To(Equal(0), stdout+stderr)
	})

	It("is refused as the PIN file's pin when PIN moves to a fork's commit", func(ctx SpecContext) {
		fc := forkCommit(f.ceph)
		f.repo.put("hack/s3tests/PIN", pinText(fc))
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("hack/s3tests/PIN: the pin " + fc + ": not on " + trusted))
		Expect(stdout).To(ContainSubstring(notServed))
	})

	It("asks the repository, not --clone, which holds the fork's commit", func(ctx SpecContext) {
		fork := cloneGitRepo(f.ceph.dir)
		fork.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
		fc := fork.commit("a fork's test")
		Expect(fork.git("rev-parse", parity.TrustedBranch)).To(Equal(fc), "the fork's own rgw-go-s3tests holds the commit")
		basePinAt(f, fc, `,"reason":"moved"`)
		status, stdout, _, _ := f.check(ctx, "--clone", fork.dir, "test/s3tests/baseline")
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(notServed))
	})
})

var _ = Describe("parity pin's new commit", func() {
	var repo *gitRepo
	var old, dir string

	BeforeEach(func() {
		repo = newGitRepo()
		old = repo.seed()
		repo.serveTrusted()
		dir = GinkgoT().TempDir()
	})

	DescribeTable("is refused, naming why and writing nothing, unless it is on rgw-go-s3tests",
		func(ctx SpecContext, pick func() (next, clone string), why string) {
			next, clone := pick()
			opts := pinFiles(dir, repo.dir, old, next)
			opts.Clone = clone
			before := snapshot(opts)
			_, err := parity.Repin(ctx, opts)
			Expect(err).To(MatchError(And(
				ContainSubstring(next+": not on the history of file://"+repo.dir+"'s "+parity.TrustedBranch+", where an s3-tests pin must be: "),
				ContainSubstring(why))))
			Expect(snapshot(opts)).To(Equal(before))
		},
		Entry("a fork's commit", func() (string, string) {
			fork := cloneGitRepo(repo.dir)
			fork.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
			return fork.commit("a fork's test"), fork.dir
		}, notServed),
		Entry("a commit of another branch", func() (string, string) { return branchCommit(repo), repo.dir }, notAncestor),
		Entry("a dangling commit", func() (string, string) { return danglingCommit(repo, old), repo.dir }, notAncestor),
	)

	It("is pinned when an ancestor of the branch's tip", func(ctx SpecContext) {
		repo.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
		ancestor := repo.commit("add a test")
		repo.write("README.rst", "s3-tests\nlater\n")
		repo.commit("a later change")
		opts := pinFiles(dir, repo.dir, old, ancestor)
		res, err := parity.Repin(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Admitted).To(Equal(parity.AdmitAdditive))
		Expect(readLedger(filepath.Join(dir, "squid.json")).Pins[1].Source).To(Equal(ancestor))
	})
})
