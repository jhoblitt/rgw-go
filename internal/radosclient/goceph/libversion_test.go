package goceph_test

import (
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("the librados version", func() {
	// eio is the bare error rados_connect returns for a key it cannot parse.
	var eio error

	BeforeEach(func() {
		eio = &radosclient.Error{Errno: int32(syscall.EIO), Op: "connect"}
	})

	DescribeTable("leaves a failed connect as it was when librados parses AES256KRB5 keys",
		func(v string) { Expect(goceph.ExplainConnect(v, eio)).To(BeIdenticalTo(eio)) },
		Entry("the first such Squid release", "19.2.6"),
		Entry("a later Squid release", "19.2.10"),
		Entry("a Tentacle development build, above the 19.x floor", "19.3.0-1234-gabcdef"),
		Entry("the first such Tentacle release", "20.2.4"),
		Entry("a later Tentacle release", "20.2.5"),
		Entry("a Tentacle development build of the next minor", "20.3.0"),
		Entry("a release line after Tentacle", "21.2.0"),
		Entry("a version it cannot parse, which it does not judge", "unknown"),
		Entry("no version, when libceph-common lacks the symbol", ""),
	)

	DescribeTable("explains a failed connect when librados cannot parse them",
		func(v string) {
			err := goceph.ExplainConnect(v, eio)
			Expect(err).To(MatchError(goceph.ErrLibradosTooOld))
			Expect(err).To(MatchError(eio), "the connect's own error")
			Expect(err).To(MatchError(ContainSubstring("librados " + v + " cannot parse the AES256KRB5 cephx keys")))
		},
		Entry("the Squid release before the floor", "19.2.5"),
		Entry("the Squid release noble ships", "19.2.3"),
		Entry("a Tentacle release before its own floor, although 19.2.6 would pass", "20.2.3"),
		Entry("the first Tentacle release", "20.2.0"),
	)

	It("leaves a connect that succeeded alone, whatever the version", func() {
		Expect(goceph.ExplainConnect("19.2.5", nil)).To(Succeed())
	})

	DescribeTable("refuses a line older than Squid as a support policy",
		func(v string) { Expect(goceph.CheckLibradosLine(v)).To(MatchError(goceph.ErrLibradosTooOld)) },
		Entry("Reef", "18.2.7"),
		Entry("Quincy", "17.2.8"),
	)

	DescribeTable("does not refuse a Squid or newer line up front",
		func(v string) { Expect(goceph.CheckLibradosLine(v)).To(Succeed()) },
		Entry("a Squid release below the key floor", "19.2.5"),
		Entry("a Tentacle release below the key floor", "20.2.3"),
		Entry("a version it cannot parse", "unknown"),
	)

	It("refuses a line older than Squid before Connect touches librados", func(ctx SpecContext) {
		DeferCleanup(goceph.SetLibradosVersion("18.2.7"))
		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: "/nonexistent/ceph.conf"})
		Expect(err).To(MatchError(goceph.ErrLibradosTooOld))
		Expect(cluster).To(BeNil())
	})

	It("lets Connect try a Squid librados below the key floor", func(ctx SpecContext) {
		DeferCleanup(goceph.SetLibradosVersion("19.2.5"))
		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: "/nonexistent/ceph.conf"})
		Expect(err).To(HaveOccurred(), "reading the missing config file")
		Expect(err).NotTo(MatchError(goceph.ErrLibradosTooOld))
		Expect(cluster).To(BeNil())
	})

	It("reads the Ceph version of the librados the process runs", func() {
		Expect(goceph.LibradosVersion()).To(MatchRegexp(`^\d+\.\d+\.\d+`))
	})
})
