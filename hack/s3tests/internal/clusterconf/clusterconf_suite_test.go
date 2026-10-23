package clusterconf_test

import (
	"log/slog"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Managed by go-conventions (references/testing.md owns the bootstrap):
// spec order is randomized within the suite and a committed Pending or
// focused spec fails the run under plain go test.
func TestClusterconf(t *testing.T) {
	RegisterFailHandler(Fail)
	suiteCfg, reporterCfg := GinkgoConfiguration()
	suiteCfg.RandomizeAllSpecs = true
	suiteCfg.FailOnPending = true
	RunSpecs(t, "clusterconf suite", suiteCfg, reporterCfg)
}

// The Configurer logs its steps through the default logger, which a spec's
// output keeps.
var _ = BeforeEach(func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(GinkgoWriter, nil)))
	DeferCleanup(func() { slog.SetDefault(prev) })
})
