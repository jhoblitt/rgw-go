package cephtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported
	"github.com/onsi/gomega/gexec"

	"github.com/jhoblitt/rgw-go/internal/frontend"
)

// ServiceMapOff is the environment entry every rgw-go StartRGWGo starts runs
// with. rgw-go registers in the manager's service map unless told not to,
// and librados reports the build of the library the process links there,
// which `ceph versions` counts: a host rgw-go linking another Ceph build than
// the cluster's adds a second one.
const ServiceMapOff = "RGW_GO_SERVICE_MAP=false"

// rgwGoPackage is rgw-go's main package.
const rgwGoPackage = "github.com/jhoblitt/rgw-go/cmd/rgw-go"

// Form is the command line StartRGWGo starts rgw-go with.
type Form int

const (
	// Serve runs rgw-go serve with the arguments given: serve's own flags,
	// then "--" and radosgw's options.
	Serve Form = iota
	// Radosgw runs a copy of rgw-go named radosgw, which takes its whole
	// command line as radosgw's, as Rook launches it; rgw-go's own settings
	// then come from RGW_GO_* variables alone.
	Radosgw
)

var (
	// buildsMu guards builds and buildDirs.
	buildsMu sync.Mutex
	// builds build rgw-go once for each form a test process starts, in a
	// directory of its own that outlives every spec: the binary named for the
	// form's argv[0]. The build runs under no spec's context, so a spec that
	// ends cannot leave a canceled build for the next.
	builds = newBuilds()
	// buildDirs are the directories build made, which RemoveRGWGoBuilds
	// removes.
	buildDirs []string
)

func newBuilds() map[Form]func() (string, error) {
	return map[Form]func() (string, error){
		Serve:   sync.OnceValues(func() (string, error) { return build("rgw-go") }),
		Radosgw: sync.OnceValues(func() (string, error) { return build("radosgw") }),
	}
}

// build builds rgw-go as name in a new temporary directory and returns its
// path; a failed build leaves no directory behind.
func build(name string) (string, error) {
	dir, err := os.MkdirTemp("", "rgw-go-build-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, name)
	out, err := exec.CommandContext(context.Background(), "go", "build", "-tags=ceph_preview", "-o", bin, rgwGoPackage).CombinedOutput() //nolint:gosec // bin is in a directory of its own
	if err != nil {
		return "", errors.Join(fmt.Errorf("building %s: %w: %s", rgwGoPackage, err, out), os.RemoveAll(dir))
	}
	buildsMu.Lock()
	defer buildsMu.Unlock()
	buildDirs = append(buildDirs, dir)
	return bin, nil
}

// RemoveRGWGoBuilds removes every binary StartRGWGo has built in this test
// process, as gexec.CleanupBuildArtifacts removes gexec.Build's; a later
// StartRGWGo builds again. A suite that starts rgw-go calls it from its
// AfterSuite, since StartRGWGo runs in specs, whose cleanups would remove a
// build every later spec needs.
func RemoveRGWGoBuilds() {
	GinkgoHelper()
	buildsMu.Lock()
	defer buildsMu.Unlock()
	for _, dir := range buildDirs {
		Expect(os.RemoveAll(dir)).To(Succeed())
	}
	buildDirs = nil
	builds = newBuilds()
}

// StartRGWGo starts rgw-go, built once per form for the test process, in
// form with args, its output going to GinkgoWriter, and returns the running
// process. Its environment is
// the spec process's, then ServiceMapOff, then env, so that env, and a serve
// flag, can turn the registration back on and nothing inherited can. When the
// spec or container that started it ends, it stops rgw-go as Rook stops
// radosgw, with SIGTERM, and kills it, failing the spec, only when it has not
// exited a minute past the frontend's drain.
func StartRGWGo(form Form, args []string, env ...string) *gexec.Session {
	GinkgoHelper()
	argv := slices.Concat([]string{"serve"}, args)
	if form == Radosgw {
		argv = args
	}
	buildsMu.Lock()
	binary := builds[form]
	buildsMu.Unlock()
	bin, err := binary()
	Expect(err).NotTo(HaveOccurred())

	// bin is the binary built for form. Not exec.CommandContext: a process a
	// BeforeAll starts outlives that node's context, and DeferCleanup ends
	// it.
	cmd := exec.Command(bin, argv...) //nolint:noctx,gosec // see above
	cmd.Env = slices.Concat(os.Environ(), []string{ServiceMapOff}, env)
	s, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		defer s.Kill()
		s.Terminate()
		Eventually(s).WithTimeout(frontend.DrainTimeout+time.Minute).WithPolling(100*time.Millisecond).
			Should(gexec.Exit(), "rgw-go outlived SIGTERM and is killed")
	})
	return s
}
