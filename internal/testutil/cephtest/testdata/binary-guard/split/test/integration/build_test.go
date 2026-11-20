package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var rgwGo string

var _ = BeforeSuite(func() {
	var err error
	rgwGo, err = gexec.Build("github.com/jhoblitt/rgw-go/cmd/rgw-go", "-tags=ceph_preview")
	Expect(err).NotTo(HaveOccurred())
})
