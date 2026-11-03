package goceph_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("the C allocator", func() {
	It("is the full tcmalloc radosgw links, which owns what malloc returns, with its libunwind beside it", func() {
		Expect(goceph.CheckTcmalloc()).To(Succeed())
	})

	// Each line is one /proc/self/maps mapping, as the kernel writes it: the
	// file's real path after the inode, padded with spaces.
	const (
		privTcmalloc = "7f2a4c000000-7f2a4c013000 r--p 00000000 fd:02 4211 /home/u/.local/lib/tcm/libtcmalloc.so.4.5.9\n"
		privUnwind   = "7f2a4c400000-7f2a4c404000 r--p 00000000 fd:02 4212 /home/u/.local/lib/tcm/libunwind.so.8.1.0\n"
		hostUnwind   = "7f2a4c800000-7f2a4c804000 r--p 00000000 fd:01 9001 /usr/lib64/libunwind.so.8.2.0\n"
		hostMinimal  = "7f2a4cc00000-7f2a4cc0a000 r--p 00000000 fd:01 9002 /usr/lib64/libtcmalloc_minimal.so.4.6.3\n"
		heap         = "01c0a000-01c2b000 rw-p 00000000 00:00 0                                  [heap]\n"
	)

	DescribeTable("takes tcmalloc's libunwind only from tcmalloc's own directory",
		func(maps string, want types.GomegaMatcher) { Expect(goceph.TcmallocDepsBeside(maps)).To(want) },
		Entry("a DT_RPATH build, both from the private directory",
			heap+privTcmalloc+privUnwind, Succeed()),
		Entry("a tcmalloc built without libunwind",
			heap+privTcmalloc, Succeed()),
		Entry("a DT_RUNPATH build, which takes the host's libunwind beside the private tcmalloc",
			privTcmalloc+hostUnwind,
			MatchError(ContainSubstring("from /home/u/.local/lib/tcm but its libunwind.so.8 from /usr/lib64"))),
		Entry("no libtcmalloc.so.4, only the minimal library",
			hostMinimal+hostUnwind, MatchError(ContainSubstring("maps no libtcmalloc.so.4"))),
	)
})
