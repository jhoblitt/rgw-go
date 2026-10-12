package parity_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	aPin  = "0123456789abcdef0123456789abcdef01234567"
	aFork = "ffffffffffffffffffffffffffffffffffffffff"
)

// cr is a carriage return.
var cr = string([]byte{13})

var _ = Describe("check --base on a run.sh", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	// withRunSh puts the fixture's run.sh with edit applied in the working
	// tree.
	withRunSh := func(edit func(string) string) {
		f.repo.put("hack/s3tests/run.sh", []byte(edit(string(runShText(suiteDir, "file://"+f.ceph.dir)))))
	}

	DescribeTable("refuses one that may set S3TESTS_COMMIT other than from PIN, as bash would",
		func(ctx SpecContext, edit func(string) string, want string) {
			withRunSh(edit)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring("hack/s3tests/run.sh: "))
			Expect(stdout).To(ContainSubstring(want))
		},
		Entry("a second assignment before the line", func(s string) string {
			return strings.Replace(s, pinLine, "S3TESTS_COMMIT="+aFork+"\n"+pinLine, 1)
		}, "line 4 names S3TESTS_COMMIT other than as ${S3TESTS_COMMIT}: S3TESTS_COMMIT="+aFork),
		Entry("the line in a string and declare setting it", func(s string) string {
			return strings.Replace(s, pinLine, ": '\n"+pinLine+"\n'\ndeclare -r S3TESTS_COMMIT="+aFork, 1)
		}, "names S3TESTS_COMMIT other than as ${S3TESTS_COMMIT}: declare -r S3TESTS_COMMIT="+aFork),
		Entry("a literal default for the environment's", func(s string) string {
			return strings.Replace(s, pinLine, "readonly S3TESTS_COMMIT=${S3TESTS_COMMIT:-"+aFork+"}", 1)
		}, `0 lines, not 1, are "`+pinLine+`"`),
		Entry("the line twice", func(s string) string {
			return strings.Replace(s, pinLine, pinLine+"\n"+pinLine, 1)
		}, `2 lines, not 1, are "`+pinLine+`"`),
		Entry("an expansion that assigns", func(s string) string {
			return strings.Replace(s, pinLine, ": ${S3TESTS_COMMIT:="+aFork+"}\n"+pinLine, 1)
		}, "names S3TESTS_COMMIT other than as ${S3TESTS_COMMIT}: : ${S3TESTS_COMMIT:="+aFork+"}"),
		Entry("the name joined across a line continuation", func(s string) string {
			return strings.Replace(s, pinLine, "S3TESTS_\\\nCOMMIT="+aFork+"\n"+pinLine, 1)
		}, "a line continuation joins S3TESTS_COMMIT from pieces"),
		Entry("an export", func(s string) string {
			return s + "export S3TESTS_COMMIT\n"
		}, "names S3TESTS_COMMIT other than as ${S3TESTS_COMMIT}: export S3TESTS_COMMIT"),
		Entry("a carriage return ending the line", func(s string) string {
			return strings.Replace(s, pinLine, pinLine+cr, 1)
		}, `0 lines, not 1, are "`+pinLine+`"`),
	)

	It("passes one that names S3TESTS_COMMIT in comments and expansions", func(ctx SpecContext) {
		withRunSh(func(s string) string {
			return s + "# S3TESTS_COMMIT=anything in a comment\necho \"${S3TESTS_COMMIT}\"\n"
		})
		status, stdout, stderr, _ := f.check(ctx)
		Expect(status).To(Equal(0), stdout+stderr)
	})
})

var _ = Describe("check --base on a PIN file", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	DescribeTable("refuses one parity cannot read by the rule",
		func(ctx SpecContext, text, want string) {
			f.repo.put("hack/s3tests/PIN", []byte(text))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(want))
		},
		Entry("a carriage return", aPin+cr+"\n", "hack/s3tests/PIN is not exactly 40 lowercase hex digits and a newline"),
		Entry("upper case", strings.ToUpper(aPin)+"\n", "hack/s3tests/PIN is not exactly 40 lowercase hex digits and a newline"),
	)

	It("refuses its removal", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(f.repo.dir, "hack/s3tests/PIN"))).To(Succeed())
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("hack/s3tests/PIN: removed; the s3-tests pin lives there"))
	})

	It("refuses a symbolic link", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(f.repo.dir, "hack/s3tests/PIN"))).To(Succeed())
		f.repo.put("hack/s3tests/PIN.real", pinText(f.pin))
		Expect(os.Symlink("PIN.real", filepath.Join(f.repo.dir, "hack/s3tests/PIN"))).To(Succeed())
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("hack/s3tests/PIN: not a regular file"))
	})
})

var _ = Describe("record --pin-file", func() {
	var ledger, pinFile string

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		ledger, pinFile = filepath.Join(dir, "l.json"), filepath.Join(dir, "PIN")
		Expect(os.WriteFile(pinFile, []byte(aPin+"\n"), 0o600)).To(Succeed())
	})

	// at returns record's arguments for runs at commit.
	at := func(commit string) []string {
		a := ledgerArgs(ledger, []string{"--pin-file", pinFile}, "testdata/run1.xml", "testdata/run1.xml")
		for i, s := range a {
			if s == "s3tests_commit=aaa" {
				a[i] = "s3tests_commit=" + commit
			}
		}
		return a
	}

	It("records runs at the PIN file's commit", func(ctx SpecContext) {
		status, _, stderr := run(ctx, at(aPin)...)
		Expect(status).To(Equal(0), stderr)
	})

	It("refuses runs at another commit, as S3TESTS_COMMIT in the environment gives, writing nothing", func(ctx SpecContext) {
		other := strings.Repeat("e", 40)
		status, _, stderr := run(ctx, at(other)...)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring("the record ran s3-tests at " + other + ", not at " + pinFile + "'s pin " + aPin))
		Expect(ledger).NotTo(BeAnExistingFile())
	})
})
