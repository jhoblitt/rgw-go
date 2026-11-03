package image_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// fakeEngine stands in for podman or docker: it records each call's
// arguments, one a line and then a line "--end--", in $FAKE_LOG, answers info
// with $FAKE_ROOTLESS, and answers image inspect with success for the
// references $FAKE_IMAGES lists. Every other call succeeds.
const fakeEngine = `#!/usr/bin/env bash
{ printf '%s\n' "$@"; echo --end--; } >>"${FAKE_LOG}"
case "$1" in
info) echo "${FAKE_ROOTLESS:-false}" ;;
image) grep -qxF -- "$3" "${FAKE_IMAGES}" ;;
esac
`

// world is a fake engine and the environment the scripts run in: nothing of
// the spec process's own but PATH and HOME, so that no GOFLAGS, GOMAXPROCS or
// RGW_GO_* the caller exports reaches a script, and a TMPDIR of the spec's own
// for the build context.
type world struct {
	dir, log, images string
	env              []string
}

func newWorld() *world {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	w := &world{dir: dir, log: filepath.Join(dir, "log"), images: filepath.Join(dir, "images")}
	engine := filepath.Join(dir, "engine")
	Expect(os.WriteFile(engine, []byte(fakeEngine), 0o700)).To(Succeed())
	Expect(os.WriteFile(w.images, nil, 0o600)).To(Succeed())
	w.env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"RGW_GO_ENGINE=" + engine, "FAKE_LOG=" + w.log, "FAKE_IMAGES=" + w.images,
		"GOMODCACHE=" + filepath.Join(dir, "mod"), "TMPDIR=" + dir,
	}
	return w
}

// have makes the engine hold the images refs.
func (w *world) have(refs ...string) {
	GinkgoHelper()
	data, err := os.ReadFile(w.images)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(w.images, append(data, strings.Join(refs, "\n")+"\n"...), 0o600)).To(Succeed())
}

// run runs a script of this directory in dir with the world's environment and
// env, and returns its stdout and stderr.
func (w *world) run(ctx context.Context, dir string, env []string, script string, args ...string) (stdout, stderr string, err error) {
	GinkgoHelper()
	path, absErr := filepath.Abs(script)
	Expect(absErr).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = slices.Concat(w.env, env)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// calls returns the engine calls the scripts made, each its arguments.
func (w *world) calls() [][]string {
	GinkgoHelper()
	data, err := os.ReadFile(w.log)
	if os.IsNotExist(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	var calls [][]string
	var call []string
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimSuffix(line, "\n"); line == "--end--" {
			calls = append(calls, call)
			call = nil
		} else {
			call = append(call, line)
		}
	}
	return calls
}

// runCall returns the one engine run the scripts made.
func (w *world) runCall() []string {
	GinkgoHelper()
	runs := slices.DeleteFunc(w.calls(), func(c []string) bool { return c[0] != "run" })
	Expect(runs).To(HaveLen(1))
	return runs[0]
}

// checkout is the repository root as pwd -P prints it.
func checkout() string {
	GinkgoHelper()
	repo, err := filepath.EvalSymlinks(filepath.Join("..", ".."))
	Expect(err).NotTo(HaveOccurred())
	repo, err = filepath.Abs(repo)
	Expect(err).NotTo(HaveOccurred())
	return repo
}

// pinned is the Ceph image a release's chart values pin.
func pinned(release string) (image, version string) {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(checkout(), "hack", "rooket", release, "values", "rook-ceph-cluster.yaml"))
	Expect(err).NotTo(HaveOccurred())
	var values struct {
		CephImage struct{ Repository, Tag string } `yaml:"cephImage"`
	}
	Expect(yaml.Unmarshal(data, &values)).To(Succeed())
	return values.CephImage.Repository + ":" + values.CephImage.Tag, strings.TrimPrefix(values.CephImage.Tag, "v")
}

// hash12 is the first twelve hex digits of the SHA-256 of the lines, each
// ended by a newline, and the file, as the scripts take a tag's hash.
func hash12(file string, lines ...string) string {
	GinkgoHelper()
	data, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())
	sum := sha256.Sum256(slices.Concat([]byte(strings.Join(lines, "\n")+"\n"), data))
	return hex.EncodeToString(sum[:])[:12]
}

// builderRef is the reference of a release's builder: its Ceph tag and the
// hash of its base image's name and Containerfile.builder, the tag
// cgo-build.sh has always built under.
func builderRef(release string) string {
	GinkgoHelper()
	base, _ := pinned(release)
	return "localhost/rgw-go-builder:" + base[strings.LastIndex(base, ":")+1:] + "-" + hash12("Containerfile.builder", base)
}

// lintPin is the golangci-lint version the Makefile pins, its one home.
func lintPin() string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(checkout(), "Makefile"))
	Expect(err).NotTo(HaveOccurred())
	m := regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION \?= (\S+)$`).FindSubmatch(data)
	Expect(m).NotTo(BeNil())
	return string(m[1])
}

// gitMounts is the mount run.sh adds for a worktree's common git directory,
// none for a checkout whose git directory is inside it.
func gitMounts(ctx context.Context) []string {
	GinkgoHelper()
	repo := checkout()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	Expect(err).NotTo(HaveOccurred())
	common, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	Expect(err).NotTo(HaveOccurred())
	if strings.HasPrefix(common+"/", repo+"/") {
		return nil
	}
	return []string{"-v", common + ":" + common}
}

var _ = Describe("the builder scripts", func() {
	var w *world

	BeforeEach(func() { w = newWorld() })

	It("cgo-build.sh runs a goreleaser build's go in the release's builder as it always has, "+
		"with the release's Ceph version beside the go variables", func(ctx SpecContext) {
		repo := checkout()
		builder := builderRef("tentacle")
		w.have(builder)
		_, stderr, err := w.run(ctx, repo, []string{
			"RGW_GO_CEPH_TAG=v20.2.4", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1",
		}, "cgo-build.sh", "build", "-trimpath", "-tags=ceph_preview", "-ldflags=-s -w", "-o", "dist/x/rgw-go", "./cmd/rgw-go")
		Expect(err).NotTo(HaveOccurred(), stderr)

		// The engine run cgo-build.sh made before it ran through run.sh, with
		// the one variable run.sh adds, RGW_GO_TEST_CEPH_VERSION, before the
		// image.
		want := slices.Concat(
			[]string{
				"run", "--rm", "--pull=never", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
				"--security-opt", "label=disable", "--platform", "linux/amd64",
				"-v", repo + ":" + repo, "-v", filepath.Join(w.dir, "mod") + ":/go/pkg/mod",
			},
			gitMounts(ctx),
			[]string{
				"-w", repo,
				"-e", "GOMODCACHE=/go/pkg/mod",
				"-e", "GOCACHE=" + repo + "/hack/image/out/gocache/" + builder[strings.LastIndex(builder, ":")+1:],
				"-e", "HOME=/tmp", "-e", "GOOS=linux", "-e", "GOARCH=amd64", "-e", "GOAMD64=",
				"-e", "CGO_ENABLED=1", "-e", "GOFLAGS=",
				"-e", "RGW_GO_TEST_CEPH_VERSION=20.2.4",
				builder,
				"go", "build", "-trimpath", "-tags=ceph_preview", "-ldflags=-s -w", "-o", "dist/x/rgw-go", "./cmd/rgw-go",
			},
		)
		Expect(w.runCall()).To(Equal(want))
		Expect(w.calls()).To(HaveLen(3), "an image inspect, the rootless probe and the run")
	})

	It("cgo-build.sh refuses a tag no release's chart values pin, running nothing", func(ctx SpecContext) {
		_, stderr, err := w.run(ctx, checkout(), []string{"RGW_GO_CEPH_TAG=v1.2.3"}, "cgo-build.sh", "version")
		Expect(err).To(HaveOccurred())
		Expect(stderr).To(Equal("cgo-build.sh: no release's chart values pin RGW_GO_CEPH_TAG v1.2.3\n"))
		Expect(w.calls()).To(BeEmpty())
	})

	DescribeTable("builder.sh --print names a release's builder by the hash of its base and Containerfile.builder, building nothing",
		func(ctx SpecContext, release string) {
			stdout, stderr, err := w.run(ctx, checkout(), nil, "builder.sh", "--print", release)
			Expect(err).NotTo(HaveOccurred(), stderr)
			Expect(stdout).To(Equal(builderRef(release) + "\n"))
			Expect(w.calls()).To(BeEmpty())
		},
		Entry("Squid", "squid"),
		Entry("Tentacle", "tentacle"),
	)

	It("builder.sh builds a builder the engine lacks from an empty context and prints only its reference", func(ctx SpecContext) {
		base, _ := pinned("squid")
		stdout, stderr, err := w.run(ctx, checkout(), nil, "builder.sh", "squid")
		Expect(err).NotTo(HaveOccurred(), stderr)
		Expect(stdout).To(Equal(builderRef("squid") + "\n"))
		calls := w.calls()
		Expect(calls).To(HaveLen(2))
		build := calls[1]
		Expect(build[:len(build)-1]).To(Equal([]string{
			"build", "--platform", "linux/amd64", "-f", filepath.Join(checkout(), "hack", "image", "Containerfile.builder"),
			"--build-arg", "CEPH_IMAGE=" + base, "-t", builderRef("squid"),
		}))
		Expect(build[len(build)-1]).NotTo(BeADirectory(), "the build context is removed")
	})

	It("run.sh maps the caller to itself under rootless podman with keep-id", func(ctx SpecContext) {
		w.have(builderRef("tentacle"))
		_, stderr, err := w.run(ctx, checkout(), []string{"FAKE_ROOTLESS=true"}, "run.sh", "--", "tentacle", "true")
		Expect(err).NotTo(HaveOccurred(), stderr)
		Expect(w.runCall()).To(SatisfyAll(ContainElement("--userns=keep-id"), Not(ContainElement("--user"))))
	})

	It("run.sh puts the caller's engine arguments after its own and before the image, and the command after it",
		func(ctx SpecContext) {
			builder := builderRef("squid")
			w.have(builder)
			_, stderr, err := w.run(ctx, checkout(), nil, "run.sh", "--network=host", "-e", "X=1", "--", "squid", "make", "test")
			Expect(err).NotTo(HaveOccurred(), stderr)
			run := w.runCall()
			Expect(run[len(run)-7:]).To(Equal([]string{
				"RGW_GO_TEST_CEPH_VERSION=19.2.7", "--network=host", "-e", "X=1", builder, "make", "test",
			}))
		})

	DescribeTable("run.sh passes a variable of the host's caps and of the citation check only when it is set",
		func(ctx SpecContext, name string) {
			w.have(builderRef("tentacle"))
			_, stderr, err := w.run(ctx, checkout(), nil, "run.sh", "--", "tentacle", "true")
			Expect(err).NotTo(HaveOccurred(), stderr)
			Expect(w.runCall()).NotTo(ContainElement(HavePrefix(name + "=")))

			w = newWorld()
			w.have(builderRef("tentacle"))
			_, stderr, err = w.run(ctx, checkout(), []string{name + "=4"}, "run.sh", "--", "tentacle", "true")
			Expect(err).NotTo(HaveOccurred(), stderr)
			Expect(w.runCall()).To(ContainElement(name + "=4"))
		},
		Entry("GOMAXPROCS", "GOMAXPROCS"),
		Entry("GOLANGCI_LINT_CONCURRENCY", "GOLANGCI_LINT_CONCURRENCY"),
		Entry("CITECHECK_CEPH", "CITECHECK_CEPH"),
	)

	It("RGW_GO_STAGE=check runs the check image, built on the builder with the Makefile's golangci-lint, "+
		"under the builder's build cache and a lint cache of its own", func(ctx SpecContext) {
		repo := checkout()
		builder := builderRef("tentacle")
		w.have(builder)
		tag := builder[strings.LastIndex(builder, ":")+1:]
		check := "localhost/rgw-go-check:" + tag + "-" + hash12("Containerfile.check", builder+" "+lintPin())
		_, stderr, err := w.run(ctx, repo, []string{"RGW_GO_STAGE=check"}, "run.sh", "--", "tentacle", "make", "lint")
		Expect(err).NotTo(HaveOccurred(), stderr)
		builds := slices.DeleteFunc(w.calls(), func(c []string) bool { return c[0] != "build" })
		Expect(builds).To(HaveLen(1))
		Expect(builds[0]).To(ContainElements(
			filepath.Join(repo, "hack", "image", "Containerfile.check"),
			"BUILDER="+builder, "GOLANGCI_LINT_VERSION="+lintPin(), check))
		Expect(w.runCall()).To(SatisfyAll(
			ContainElement("GOCACHE="+repo+"/hack/image/out/gocache/"+tag),
			ContainElement("GOLANGCI_LINT_CACHE="+repo+"/hack/image/out/lintcache/"+tag),
			ContainElement(check),
			Not(ContainElement(builder)),
		))
	})

	It("run.sh refuses to run from outside the checkout before it touches the engine", func(ctx SpecContext) {
		_, stderr, err := w.run(ctx, w.dir, nil, "run.sh", "--", "tentacle", "true")
		Expect(err).To(HaveOccurred())
		Expect(stderr).To(Equal(fmt.Sprintf("run.sh: run it from inside the checkout, %s\n", checkout())))
		Expect(w.calls()).To(BeEmpty())
	})
})
