package cli_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Managed by go-conventions (references/testing.md owns the bootstrap):
// spec order is randomized within the suite and a committed Pending or
// focused spec fails the run under plain go test.
func TestCli(t *testing.T) {
	RegisterFailHandler(Fail)
	suiteCfg, reporterCfg := GinkgoConfiguration()
	suiteCfg.RandomizeAllSpecs = true
	suiteCfg.FailOnPending = true
	RunSpecs(t, "cli suite", suiteCfg, reporterCfg)
}
