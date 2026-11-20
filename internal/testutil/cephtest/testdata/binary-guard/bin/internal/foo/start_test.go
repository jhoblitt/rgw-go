package foo_test

import (
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = It("starts the gateway rgw-go-up.sh built", func() {
	repo, err := filepath.Abs("../..")
	Expect(err).NotTo(HaveOccurred())
	Expect(exec.Command(filepath.Join(repo, "bin/rgw-go"), "serve", "--").Start()).To(Succeed())
})
