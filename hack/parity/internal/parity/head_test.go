package parity_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("check --base with --head", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	// headCheck commits the working tree as the head, puts the base's
	// ledgers back in the working tree, and runs check --base --head.
	headCheck := func(ctx context.Context, extra ...string) (status int, stdout, stderr, summary string) {
		head := f.repo.commit("pull request")
		f.repo.git("checkout", "-q", f.base, "--", "test")
		file := filepath.Join(GinkgoT().TempDir(), "summary.md")
		args := append([]string{"check", "--base", f.base, "--head", head, "--repo", f.repo.dir, "--summary", file}, extra...)
		status, stdout, stderr = run(ctx, args...)
		if data, err := os.ReadFile(file); err == nil {
			summary = string(data)
		}
		return status, stdout, stderr, summary
	}

	It("judges the head's ledgers, not the working tree's", func(ctx SpecContext) {
		f.appendEntry("t1", reasoned(entry(f.pin, "failed"), "fixed upstream"))
		head := f.repo.commit("pull request")
		f.repo.put(squidLedger, []byte("not a ledger"))
		file := filepath.Join(GinkgoT().TempDir(), "summary.md")
		status, stdout, stderr := run(ctx, "check", "--base", f.base, "--head", head, "--repo", f.repo.dir, "--summary", file)
		Expect(status).To(Equal(0), stdout+stderr)
		data, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("t1: passed -> failed: fixed upstream"))
	})

	It("finds an edit the head makes and the working tree does not show", func(ctx SpecContext) {
		l := f.repo.readLedgerAt(squidLedger)
		l.Tests["t1"][0].Outcome = "failed"
		f.repo.putLedger(squidLedger, l)
		status, stdout, _, _ := headCheck(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(squidLedger + ": t1 entry 1: changed or moved; an entry is never edited"))
	})

	It("refuses a ledger the head removes", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(f.repo.dir, squidLedger))).To(Succeed())
		status, stdout, _, _ := headCheck(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(squidLedger + ": removed; a ledger is never removed"))
	})

	It("refuses a ledger the head replaces with a symbolic link", func(ctx SpecContext) {
		data, err := os.ReadFile(filepath.Join(f.repo.dir, squidLedger))
		Expect(err).NotTo(HaveOccurred())
		f.repo.put("hack/elsewhere.json", data)
		Expect(os.Remove(filepath.Join(f.repo.dir, squidLedger))).To(Succeed())
		Expect(os.Symlink("../../../hack/elsewhere.json", filepath.Join(f.repo.dir, squidLedger))).To(Succeed())
		status, stdout, _, _ := headCheck(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(squidLedger + ": not a regular file"))
	})

	It("refuses a ledger the head adds outside the paths it checks", func(ctx SpecContext) {
		f.repo.putLedger("hack/s3tests/squid-v2.json", ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}}))
		head := f.repo.commit("pull request")
		Expect(os.Remove(filepath.Join(f.repo.dir, "hack/s3tests/squid-v2.json"))).To(Succeed())
		status, stdout, _ := run(ctx, "check", "--base", f.base, "--head", head, "--repo", f.repo.dir)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("hack/s3tests/squid-v2.json: a ledger outside the paths the check covers"))
	})

	It("passes a head that changes no ledger, writing no summary", func(ctx SpecContext) {
		f.repo.put("README.md", []byte("a change elsewhere\n"))
		status, stdout, stderr, summary := headCheck(ctx)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(summary).To(BeEmpty())
	})

	It("refuses a head that reads as an option", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "check", "--base", f.base, "--head", "--output=x", "--repo", f.repo.dir)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring(`the head "--output=x" is not a commit`))
	})

	It("is only for --base", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "check", "--old", "a.json", "--new", "b.json", "--head", f.base)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("--head is for --base"))
	})
})
