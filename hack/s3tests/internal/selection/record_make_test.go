package selection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The fakes recordFakes puts where make s3tests-record's recipe finds the
// commands it runs, so that the recipe runs whole, with no cluster.
const (
	// fakeGo installs the fake parity for go build -o, and does nothing for
	// anything else, such as make lc-mode's go run.
	fakeGo = `#!/bin/sh
if [ "$1" = build ] && [ "$2" = -o ]; then
	mkdir -p "$(dirname "$3")" && cp fake/parity "$3" && chmod +x "$3"
fi
`
	// fakeJq prints the Ceph version for the manifest's ceph_version.
	fakeJq = "#!/bin/sh\necho 19.2.7\n"
	// fakeRunSh prints the pin and the digest, or makes a run: its report,
	// its .freeze and the gate-only ids fake/gate-only-RUN.ids holds, or
	// else fake/gate-only.ids.
	fakeRunSh = `#!/bin/sh
case "$1" in
--print-commit) echo 0123456789abcdef0123456789abcdef01234567; exit 0 ;;
--print-deselect) echo 999e22cc41a0; exit 0 ;;
esac
name=hack/s3tests/out/$1-$2-$3
mkdir -p hack/s3tests/out
echo "$4 ${5:-}" >"$name.xml"
echo "$3" >"$name.freeze"
ids=fake/gate-only.ids
if [ -f "fake/gate-only-$3.ids" ]; then ids=fake/gate-only-$3.ids; fi
cp "$ids" "$name.gate-only.ids"
`
	// fakeParity logs its command line, fails when FAKE_FAIL names its kind
	// of record, full or subset, and otherwise appends to its ledger a line
	// naming its kind and its LC mode.
	fakeParity = `#!/bin/sh
ledger= kind=full lc= prev=
for a in "$@"; do
	case "$prev" in
	--ledger) ledger=$a ;;
	--meta) case "$a" in lc=*) lc=${a#lc=} ;; esac ;;
	esac
	if [ "$a" = --subset ]; then kind=subset; fi
	prev=$a
done
echo "$*" >>fake/calls.log
if [ "${FAKE_FAIL:-}" = "$kind" ]; then exit 1; fi
echo "$kind lc=$lc" >>"$ledger"
`
)

// recordFakes copies the repository's Makefile into a tree of its own laid
// out as the repository is for make s3tests-record on Squid: a started
// rgw-go's endpoint, a manifest, the fakes, and gateOnly as the gate-only ids
// every run's selection leaves out. It returns the tree's root.
func recordFakes(gateOnly string) string {
	root := GinkgoT().TempDir()
	files := map[string]string{
		"Makefile":                              readFile(filepath.Join(repoRoot, "Makefile")),
		"hack/s3tests/run.sh":                   fakeRunSh,
		"fake/bin/go":                           fakeGo,
		"fake/bin/jq":                           fakeJq,
		"fake/parity":                           fakeParity,
		"fake/gate-only.ids":                    gateOnly,
		"hack/rooket/out/squid/rgw-go.endpoint": "http://127.0.0.1:8000\n",
		"hack/rooket/out/squid/manifest.json":   `{"ceph_version":"19.2.7"}` + "\n",
	}
	for name, text := range files {
		path := filepath.Join(root, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o750)).To(Succeed())
		Expect(os.WriteFile(path, []byte(text), 0o700)).To(Succeed()) //nolint:gosec // the fakes are run
	}
	return root
}

// recordLedger is the OUT the specs give make s3tests-record, from the
// tree's root, and recordScratch the copy beside it the recipe appends to.
const (
	recordLedger  = "ledger/squid.json"
	recordScratch = "ledger/.squid.json.recording"
)

// makeRecord runs make s3tests-record on Squid in the tree at root, with
// OUT recordLedger and the fakes first on PATH, under an environment of
// PATH and HOME alone plus env, and returns what it printed.
func makeRecord(ctx context.Context, root string, env ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "make", "-C", root, "--no-print-directory", "s3tests-record", "RELEASE=squid",
		"OUT="+recordLedger)
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(root, "fake/bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var _ = Describe("make s3tests-record's ledger, recorded through the recipe with fakes", func() {
	const gateOnly = "s3tests/functional/test_s3.py::test_lifecycle_expiration\n"

	// withLedger returns a tree whose ledger OUT holds one line.
	withLedger := func(gateOnly string) string {
		root := recordFakes(gateOnly)
		Expect(os.MkdirAll(filepath.Join(root, "ledger"), 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, recordLedger), []byte("orig\n"), 0o600)).To(Succeed())
		return root
	}

	It("is OUT with both records appended to a copy beside it, which then replaces it", func(ctx SpecContext) {
		root := withLedger(gateOnly)
		out, err := makeRecord(ctx, root)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(fileLines(filepath.Join(root, recordLedger))).To(Equal([]string{"orig", "full lc=off", "subset lc=radosgw"}))
		calls := fileLines(filepath.Join(root, "fake/calls.log"))
		Expect(calls).To(HaveLen(2))
		Expect(calls[0]).To(ContainSubstring("--ledger " + recordScratch + " "))
		Expect(calls[0]).To(ContainSubstring("--leave hack/s3tests/out/squid-radosgw-base2.gate-only.ids "))
		Expect(calls[1]).To(ContainSubstring("--ledger " + recordScratch + " "))
		Expect(calls[1]).To(ContainSubstring(" --subset "))
		Expect(filepath.Join(root, recordScratch)).NotTo(BeAnExistingFile())
	})

	It("is started from the copy when there was none", func(ctx SpecContext) {
		root := recordFakes(gateOnly)
		out, err := makeRecord(ctx, root)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(fileLines(filepath.Join(root, recordLedger))).To(Equal([]string{"full lc=off", "subset lc=radosgw"}))
	})

	It("gets the full record alone when the selection left no gate-only test out", func(ctx SpecContext) {
		root := withLedger("")
		out, err := makeRecord(ctx, root)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(fileLines(filepath.Join(root, recordLedger))).To(Equal([]string{"orig", "full lc=off"}))
		Expect(fileLines(filepath.Join(root, "fake/calls.log"))).To(HaveLen(1))
		Expect(out).To(ContainSubstring("left no gate-only test out, so there is no gate-only record"))
	})

	DescribeTable("is left as it was, and the copy removed, when a record fails",
		func(ctx SpecContext, kind string, calls int) {
			root := withLedger(gateOnly)
			out, err := makeRecord(ctx, root, "FAKE_FAIL="+kind)
			Expect(err).To(HaveOccurred(), out)
			Expect(fileLines(filepath.Join(root, recordLedger))).To(Equal([]string{"orig"}))
			Expect(fileLines(filepath.Join(root, "fake/calls.log"))).To(HaveLen(calls))
			Expect(filepath.Join(root, recordScratch)).NotTo(BeAnExistingFile())
		},
		Entry("the first, the full record of the pr set", "full", 1),
		Entry("the second, the subset record of the gate-only tests", "subset", 2),
	)

	It("is left as it was, with nothing recorded, when the pr set's two runs left different gate-only tests out",
		func(ctx SpecContext) {
			root := withLedger(gateOnly)
			Expect(os.WriteFile(filepath.Join(root, "fake/gate-only-base1.ids"), nil, 0o600)).To(Succeed())
			out, err := makeRecord(ctx, root)
			Expect(err).To(HaveOccurred(), out)
			Expect(out).To(ContainSubstring("the pr set's two runs left different gate-only tests out; nothing is recorded"))
			Expect(fileLines(filepath.Join(root, recordLedger))).To(Equal([]string{"orig"}))
			Expect(filepath.Join(root, "fake/calls.log")).NotTo(BeAnExistingFile())
		})
})
