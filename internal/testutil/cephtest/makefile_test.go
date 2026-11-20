package cephtest_test

import (
	"context"
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

// serviceMapOffTargets are the Makefile's targets that run an rgw-go on the
// host against a cluster, themselves, through a script or through a
// recursive make.
var serviceMapOffTargets = []string{
	"integration", "admin-gate", "gate", "read-gate", "rgw-go-up", "lc-mode", "s3tests", "admin-suite",
	"s3tests-parity", "admin-parity", "admin-record", "s3tests-record", "s3tests-record-tests", "bench-gateway",
}

// makeRun runs make on the module's Makefile with args, outside any make
// the spec runs under and without RGW_GO_SERVICE_MAP, and GOBIN given so
// that reading the Makefile runs no go command, and returns its stdout and
// whether it exited 0.
func makeRun(ctx context.Context, args ...string) (stdout string, ok bool) {
	GinkgoHelper()
	root := filepath.Dir(cephtest.ModuleRelative("go.mod"))
	cmd := exec.CommandContext(ctx, "make", slices.Concat([]string{"-C", root, "--no-print-directory", "GOBIN=/nonexistent"}, args)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"MAKEFLAGS", "MFLAGS", "MAKELEVEL", "RGW_GO_SERVICE_MAP"}, k)
	})
	out, err := cmd.Output()
	return string(out), err == nil
}

var _ = Describe("the Makefile's RGW_GO_SERVICE_MAP=false", func() {
	It("is a target-specific variable of exactly the targets that run a host rgw-go, in make -pn's database", func(ctx SpecContext) {
		db, ok := makeRun(ctx, "-pn", "help")
		Expect(ok).To(BeTrue(), "make -pn help")
		var set []string
		for _, m := range regexp.MustCompile(`(?m)^([a-z0-9-]+): RGW_GO_SERVICE_MAP = (.*)$`).FindAllStringSubmatch(db, -1) {
			Expect(m[2]).To(Equal("false"), "%s's RGW_GO_SERVICE_MAP", m[1])
			set = append(set, m[1])
		}
		Expect(set).To(ConsistOf(serviceMapOffTargets))
	})

	// probe is what target's recipe environment holds as RGW_GO_SERVICE_MAP,
	// one line for each make that builds target, read under make -n by a
	// prerequisite the command line adds, whose recipe line, marked +, is the
	// only one make -n runs besides those naming $(MAKE), which run the same
	// dry make.
	probe := func(ctx context.Context, target string) []string {
		GinkgoHelper()
		out, ok := makeRun(ctx, "-n", "--eval=.PHONY: sm-probe",
			`--eval=sm-probe: ; +@echo "probe=$${RGW_GO_SERVICE_MAP-unset}"`,
			"--eval="+target+": sm-probe", target, "RELEASE=squid")
		Expect(ok).To(BeTrue(), "make -n %s: %s", target, out)
		var got []string
		for l := range strings.Lines(out) {
			if v, found := strings.CutPrefix(strings.TrimSuffix(l, "\n"), "probe="); found {
				got = append(got, v)
			}
		}
		Expect(got).NotTo(BeEmpty(), "make -n %s ran no probe: %s", target, out)
		return got
	}

	It("is exported to the recipes of each of those targets, and the processes they start", func(ctx SpecContext) {
		for _, t := range serviceMapOffTargets {
			Expect(probe(ctx, t)).To(HaveEach("false"), "make %s", t)
		}
	})

	It("is left out of make test and make check, whose specs set it themselves", func(ctx SpecContext) {
		for _, t := range []string{"test", "check"} {
			Expect(probe(ctx, t)).To(HaveEach("unset"), "make %s", t)
		}
	})
})
