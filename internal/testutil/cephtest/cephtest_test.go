package cephtest_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// setEnv sets key to value, or unsets it for "", until the spec ends.
func setEnv(key, value string) {
	old, had := os.LookupEnv(key)
	if value == "" {
		Expect(os.Unsetenv(key)).To(Succeed())
	} else {
		Expect(os.Setenv(key, value)).To(Succeed())
	}
	DeferCleanup(func() {
		if had {
			Expect(os.Setenv(key, old)).To(Succeed())
		} else {
			Expect(os.Unsetenv(key)).To(Succeed())
		}
	})
}

var _ = Describe("ModuleRelative", func() {
	It("resolves a relative path from the module root", func() {
		got := cephtest.ModuleRelative("hack/cluster/out/squid/ceph.conf")
		root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(got)))))
		Expect(filepath.Join(root, "go.mod")).To(BeAnExistingFile())
		Expect(got).To(HaveSuffix("/hack/cluster/out/squid/ceph.conf"))
	})

	It("leaves an absolute path and the empty path alone", func() {
		Expect(cephtest.ModuleRelative("/etc/ceph/ceph.conf")).To(Equal("/etc/ceph/ceph.conf"))
		Expect(cephtest.ModuleRelative("")).To(BeEmpty())
	})
})

var _ = Describe("Conf", func() {
	It("fails the spec, rather than skipping it, when the variable is unset", func() {
		setEnv(cephtest.ConfEnv, "")
		failures := InterceptGomegaFailures(func() { cephtest.Conf() })
		Expect(failures).To(ConsistOf(ContainSubstring(cephtest.ConfEnv + " is not set")))
	})

	It("returns the resolved path when the variable is set", func() {
		setEnv(cephtest.ConfEnv, "/etc/ceph/ceph.conf")
		Expect(cephtest.Conf()).To(Equal("/etc/ceph/ceph.conf"))
	})
})

var _ = Describe("ReadManifest", func() {
	It("decodes the manifest beside the conf", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"release":"squid"}`), 0o600)).To(Succeed())
		var m struct{ Release string }
		cephtest.ReadManifest(filepath.Join(dir, "ceph.conf"), &m)
		Expect(m.Release).To(Equal("squid"))
	})
})
