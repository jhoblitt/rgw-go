package cephtest_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// binaryAllowed are the tracked files outside docs/ and Markdown that may
// name rgw-go's binary, each with its reason; a path ending in "/" covers
// every file under it. Every other start of rgw-go on the host goes through
// cephtest.StartRGWGo, or rgw-go-up.sh, so that it runs out of the service
// map.
var binaryAllowed = map[string]string{
	"internal/testutil/cephtest/rgwgo.go":               "StartRGWGo, the Go specs' one start, which builds the binary",
	"internal/testutil/cephtest/binary_guard_test.go":   "this guard, which holds the names as data",
	"internal/testutil/cephtest/testdata/binary-guard/": "the guard's fixtures, which hold the names as data",
	"hack/rooket/rgw-go-start.sh": "the shell's one start, which rgw-go-up.sh's serve line runs with " +
		"RGW_GO_SERVICE_MAP=false: it builds bin/rgw-go, or runs a derived image's /usr/bin/rgw-go",
	"hack/rooket/rgw_go_start_test.go": "rgw-go-start.sh's specs, which pin its start lines and run its image path " +
		"against a fake engine, starting nothing",
	"hack/bench/gateway/internal/gateway/gateways.go": "builds bin/rgw-go under the bench's tags only for " +
		"rgw-go-up.sh to start",
	"hack/bench/gateway/internal/gateway/cli_test.go":                   "runs against a fake execer and starts nothing",
	"hack/bench/gateway/internal/gateway/world_test.go":                 "runs against a fake execer and starts nothing",
	"hack/bench/gateway/internal/gateway/testdata/run-rgw-go.txt":       "a command trace the fake execer's specs compare",
	"hack/bench/gateway/internal/gateway/testdata/run-rgw-go-smoke.txt": "a command trace the fake execer's specs compare",
	"hack/image/build.sh": "builds the derived image's binary in the release's builder and runs it only inside " +
		"that image",
	"hack/image/Containerfile":      "copies the binary into the derived image as /usr/bin/rgw-go",
	"hack/image/scripts_test.go":    "the builder scripts' specs, whose ./cmd/rgw-go is an argument to a fake engine",
	".goreleaser.yaml":              "the release builds",
	".github/workflows/release.yml": "the release workflow, which builds a binary only to read its version stamp",
	"internal/cli/root_test.go":     "its /usr/bin/rgw-go is an argv string for cli.Argv",
	"hack/citecheck/internal/citecheck/mainclaims_test.go": "a Markdown fixture's file name, for the main-claim " +
		"check, which runs nothing",
}

// binaryNames are the names of rgw-go's binary: its package directory,
// which ./cmd/rgw-go and the import path github.com/jhoblitt/rgw-go/cmd/rgw-go
// hold, and bin/rgw-go, where rgw-go-start.sh builds it.
var binaryNames = [][]byte{[]byte("cmd/rgw-go"), []byte("bin/rgw-go")}

var (
	// goBuild is a go build or go install, and allPattern a package pattern
	// that holds rgw-go's package without naming it.
	goBuild    = regexp.MustCompile(`\bgo\s+(?:-C\s+\S+\s+)?(build|install)\b`)
	allPattern = regexp.MustCompile(`(?:^|[\s"'])(?:\./|github\.com/jhoblitt/rgw-go/)(?:cmd/)?\.\.\.`)
	writesOut  = regexp.MustCompile(`\s-o[\s=]`)
)

// allowed reports whether path may name the binary.
func allowed(path string) bool {
	if strings.HasPrefix(path, "docs/") || strings.HasSuffix(path, ".md") {
		return true
	}
	for p := range binaryAllowed {
		if path == p || strings.HasSuffix(p, "/") && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// namesBinary reports whether data names rgw-go's binary, or holds a line
// that builds it from a pattern: a go install, or a go build that keeps its
// output.
func namesBinary(data []byte) bool {
	for _, n := range binaryNames {
		if bytes.Contains(data, n) {
			return true
		}
	}
	if !bytes.Contains(data, []byte("...")) {
		return false
	}
	for line := range strings.Lines(string(data)) {
		if m := goBuild.FindStringSubmatch(line); m != nil && allPattern.MatchString(line) &&
			(m[1] == "install" || writesOut.MatchString(line)) {
			return true
		}
	}
	return false
}

// binaryReferences are the files, relative to root, that name rgw-go's
// binary and may not.
func binaryReferences(root string, files []string) []string {
	GinkgoHelper()
	var bad []string
	for _, f := range files {
		if allowed(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		Expect(err).NotTo(HaveOccurred())
		if namesBinary(data) {
			bad = append(bad, f)
		}
	}
	return bad
}

// staleAllowed are allow's entries, from the files relative to root, that
// match no file or match only files naming nothing of rgw-go's binary: each
// is to be dropped.
func staleAllowed(root string, files []string, allow map[string]string) []string {
	GinkgoHelper()
	var stale []string
	for p := range allow {
		var named bool
		for _, f := range files {
			if f != p && (!strings.HasSuffix(p, "/") || !strings.HasPrefix(f, p)) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, f))
			Expect(err).NotTo(HaveOccurred())
			named = named || namesBinary(data)
		}
		if !named {
			stale = append(stale, p)
		}
	}
	slices.Sort(stale)
	return stale
}

// fixtureFiles are the files under the fixture tree name, relative to it.
func fixtureFiles(name string) (root string, files []string) {
	GinkgoHelper()
	root = filepath.Join("testdata", "binary-guard", name)
	Expect(filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		files = append(files, filepath.ToSlash(rel))
		return err
	})).To(Succeed())
	Expect(files).NotTo(BeEmpty(), "fixture %s", name)
	return root, files
}

var _ = Describe("the guard over every reference to rgw-go's binary", func() {
	It("passes over the tracked files, and every allow-listed file names the binary", func(ctx SpecContext) {
		root := filepath.Dir(cephtest.ModuleRelative("go.mod"))
		cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		Expect(files).To(ContainElement("cmd/rgw-go/main.go"))

		Expect(binaryReferences(root, files)).To(BeEmpty(),
			"start rgw-go through cephtest.StartRGWGo, or allow-list the file in binary_guard_test.go with its reason")
		Expect(staleAllowed(root, files, binaryAllowed)).To(BeEmpty(),
			"allow-listed files that are not tracked or no longer name the binary; drop them")
	})

	DescribeTable("lists an allow-list entry that names nothing as stale",
		func(allow, want []string) {
			root, files := fixtureFiles("stale")
			entries := map[string]string{}
			for _, p := range allow {
				entries[p] = "a fixture's entry"
			}
			Expect(staleAllowed(root, files, entries)).To(Equal(want))
		},
		Entry("a file that names the binary, which stays",
			[]string{"hack/rooket/rgw-go-start.sh"}, []string(nil)),
		Entry("a file that names nothing",
			[]string{"hack/rooket/rgw-go-start.sh", "hack/image/build.sh"}, []string{"hack/image/build.sh"}),
		Entry("a file the tree does not hold",
			[]string{"hack/rooket/gone.sh"}, []string{"hack/rooket/gone.sh"}),
		Entry("a directory none of whose files names the binary",
			[]string{"hack/image/", "hack/rooket/"}, []string{"hack/image/"}),
	)

	DescribeTable("fails a start that bypasses StartRGWGo, naming the file that holds the binary's name",
		func(fixture string, want []string) {
			root, files := fixtureFiles(fixture)
			Expect(binaryReferences(root, files)).To(Equal(want))
		},
		Entry("one file building rgw-go into a package variable that another file starts",
			"split", []string{"test/integration/build_test.go"}),
		Entry("a spec starting the bin/rgw-go rgw-go-up.sh's build left behind",
			"bin", []string{"internal/foo/start_test.go"}),
		Entry("a go build of ./cmd/rgw-go in a script under hack/",
			"script", []string{"hack/foo/start.sh"}),
		Entry("an allow-listed file's name in another directory",
			"elsewhere", []string{"hack/other/gateways.go"}),
		Entry("a go install of the whole module, which names no package",
			"install", []string{"hack/foo/install.sh"}),
	)

	It("leaves docs/, Markdown and a go build that keeps no output alone", func() {
		root, files := fixtureFiles("exempt")
		Expect(files).To(ContainElements("docs/notes.txt", "hack/foo/README.md", "hack/foo/build.sh"))
		Expect(binaryReferences(root, files)).To(BeEmpty())
	})
})
