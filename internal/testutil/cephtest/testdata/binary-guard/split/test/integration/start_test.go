package integration_test

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var _ = It("starts the gateway", func() {
	s, err := gexec.Start(exec.Command(rgwGo, "serve", "--"), GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	defer s.Kill()
})
