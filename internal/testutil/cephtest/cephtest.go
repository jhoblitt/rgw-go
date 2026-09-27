// Package cephtest locates the disposable cluster hack/rooket/up.sh starts
// for the integration specs, which import it under the integration build tag.
package cephtest

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported
)

// TestPool is the scratch pool hack/rooket/up.sh creates for the specs.
const TestPool = "rgw-go-test"

// ConfEnv names the variable that points the specs at a cluster's ceph.conf.
const ConfEnv = "RGW_GO_TEST_CEPH_CONF"

// ModuleRelative resolves a relative path from the module root, so the
// documented hack/rooket/out/<release>/ paths work from the package
// directory go test runs in. "" and an absolute path come back unchanged.
func ModuleRelative(p string) string {
	GinkgoHelper()
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, p)
		}
		parent := filepath.Dir(dir)
		Expect(parent).NotTo(Equal(dir), "no go.mod above the working directory")
		dir = parent
	}
}

// Conf returns the ceph.conf ConfEnv names, resolved from the module root.
// It fails the spec when ConfEnv is unset: built with the integration tag, a
// suite runs against a cluster or fails, and never skips for want of one.
func Conf() string {
	GinkgoHelper()
	conf := ModuleRelative(os.Getenv(ConfEnv))
	Expect(conf).NotTo(BeEmpty(), "%s is not set; point it at hack/rooket/out/<release>/ceph.conf", ConfEnv)
	return conf
}

// ReadManifest decodes into v the manifest.json populate.sh wrote beside conf.
func ReadManifest(conf string, v any) {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(conf), "manifest.json"))
	Expect(err).NotTo(HaveOccurred(), "the cluster must be populated: make populate RELEASE=<release>")
	Expect(json.Unmarshal(b, v)).To(Succeed())
}
