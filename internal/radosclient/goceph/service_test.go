package goceph_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("the service-map calls of a Cluster that is closing", func() {
	DescribeTable("refuse a call once Close has started, before it reaches librados",
		func(call func(radosclient.ServiceRegistrar) error) {
			Expect(call(goceph.ClosedRegistrar())).To(MatchError(radosclient.ErrClosed))
		},
		Entry("a registration", func(r radosclient.ServiceRegistrar) error {
			return r.ServiceRegister("goceph-spec", "1", map[string]string{"k": "v"})
		}),
		Entry("a status update", func(r radosclient.ServiceRegistrar) error {
			return r.ServiceUpdateStatus(map[string]string{"k": "v"})
		}),
	)
})

// newUnconnectedCluster returns a Cluster over a librados handle that never
// connects and closes it when the spec ends. Registering on such a handle is
// allowed: librados answers the refused calls itself and keeps a first
// registration, and shutting the handle down sends the manager nothing, so
// these specs reach past the Cluster's accounting without a cluster.
func newUnconnectedCluster() goceph.RegistrarCluster {
	GinkgoHelper()
	c := goceph.UnconnectedCluster()
	DeferCleanup(func() { closeWithin(c) })
	return c
}

// closeWithin closes c, failing the spec when Close waits for a call that
// never ended.
func closeWithin(c radosclient.Cluster) {
	GinkgoHelper()
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	Eventually(closed).WithTimeout(5*time.Second).Should(Receive(Succeed()), "Close waits for a call in flight")
}

var _ = Describe("the service-map calls of a Cluster on a handle that never connects", func() {
	var cluster goceph.RegistrarCluster

	BeforeEach(func() { cluster = newUnconnectedCluster() })

	DescribeTable("map what librados refuses to the seam's errors, keeping nothing",
		func(service, daemon string) {
			Expect(cluster.ServiceRegister(service, daemon, nil)).To(MatchError(radosclient.ErrInvalid))
			Expect(cluster.ServiceRegister("goceph-spec", "1", nil)).To(Succeed(), "the refused call kept nothing")
		},
		Entry("a Ceph daemon type as the service", "osd", "1"),
		Entry("an empty service name", "", "1"),
		Entry("an empty daemon name", "goceph-spec", ""),
	)

	It("answers a status update ENOTCONN, as librados does before it is connected", func() {
		Expect(cluster.ServiceUpdateStatus(map[string]string{"k": "v"})).To(HaveField("Errno", int32(syscall.ENOTCONN)))
	})

	DescribeTable("refuse a NUL as ErrNULName before go-ceph's C string would cut at it, keeping nothing",
		func(call func() error) {
			Expect(call()).To(MatchError(radosclient.ErrNULName))
			Expect(cluster.ServiceRegister("goceph-spec", "1", nil)).To(Succeed(), "the refused call kept nothing")
		},
		Entry("in the service name", func() error { return cluster.ServiceRegister("goceph-spec\x00x", "1", nil) }),
		Entry("in the daemon name", func() error { return cluster.ServiceRegister("goceph-spec", "1\x00x", nil) }),
		Entry("in a metadata key", func() error {
			return cluster.ServiceRegister("goceph-spec", "1", map[string]string{"k\x00x": "v"})
		}),
		Entry("in a metadata value", func() error {
			return cluster.ServiceRegister("goceph-spec", "1", map[string]string{"k": "v\x00x"})
		}),
		Entry("in a status key", func() error { return cluster.ServiceUpdateStatus(map[string]string{"k\x00x": "v"}) }),
		Entry("in a status value", func() error { return cluster.ServiceUpdateStatus(map[string]string{"k": "v\x00x"}) }),
	)

	DescribeTable("refuse an empty key as go-ceph does, keeping nothing",
		func(call func() error) {
			Expect(call()).To(MatchError(ContainSubstring("key must not be empty")))
			Expect(cluster.ServiceRegister("goceph-spec", "1", nil)).To(Succeed(), "the refused call kept nothing")
		},
		Entry("in the metadata", func() error {
			return cluster.ServiceRegister("goceph-spec", "1", map[string]string{"": "v"})
		}),
		Entry("in a status", func() error { return cluster.ServiceUpdateStatus(map[string]string{"": "v"}) }),
	)

	DescribeTable("end every call they begin, so Close does not wait for one that returned",
		func(call func() error) {
			Expect(call()).To(HaveOccurred())
			closeWithin(cluster)
		},
		Entry("a refused registration", func() error { return cluster.ServiceRegister("osd", "1", nil) }),
		Entry("a status update", func() error { return cluster.ServiceUpdateStatus(map[string]string{"k": "v"}) }),
	)
})

// librados does not lock its registration, so a Cluster that let two first
// calls overlap would answer both nil; each round builds a handle of its own.
var _ = Describe("two overlapping first registrations", func() {
	It("register once, the other being ErrExists", func() {
		for round := range 25 {
			cluster := newUnconnectedCluster()
			start := make(chan struct{})
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range errs {
				wg.Go(func() {
					<-start
					errs[i] = cluster.ServiceRegister("goceph-spec", strconv.Itoa(i), nil)
				})
			}
			close(start)
			wg.Wait()
			Expect(errs).To(ConsistOf(BeNil(), MatchError(radosclient.ErrExists)), "round %d", round)
		}
	})
})

var _ = Describe("the Cluster of a connect that failed", func() {
	It("is nil, so the service map is never reached on a handle that did not connect", func(ctx SpecContext) {
		// A monmap option naming a file that does not exist fails rados_connect
		// in its monitor bootstrap, before any monitor is asked.
		missing := filepath.Join(GinkgoT().TempDir(), "monmap")
		cluster, err := goceph.Connect(ctx, goceph.Config{NoConfigFile: true, Args: []string{"--monmap=" + missing}})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(cluster).To(BeNil())
	})
})

// testKey is a cephx key as ceph-authtool writes one: the AES type, a creation
// time, and a 16-byte secret.
func testKey() string {
	key := binary.LittleEndian.AppendUint16(nil, 1)
	key = binary.LittleEndian.AppendUint32(key, 0)
	key = binary.LittleEndian.AppendUint32(key, 0)
	key = binary.LittleEndian.AppendUint16(key, 16)
	return base64.StdEncoding.EncodeToString(append(key, make([]byte, 16)...))
}

var _ = Describe("the Cluster of a connect that was canceled", func() {
	It("is nil, with the context's error at once, and the handle is shut down once librados gives up", func(ctx SpecContext) {
		// A port nothing listens on: bound, then released.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		Expect(ln.Close()).To(Succeed())

		users := goceph.AioUsers()
		connectCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		// client_mount_timeout bounds how long rados_connect keeps looking for
		// the monitor, so the goroutine Connect leaves shuts the handle down
		// seconds after Connect returned.
		cluster, err := goceph.Connect(connectCtx, goceph.Config{
			NoConfigFile: true,
			Args:         []string{"--mon-host=" + addr, "--key=" + testKey(), "--client_mount_timeout=3"},
		})
		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(cluster).To(BeNil())
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second), "Connect returned when the context ended, not when librados gave up")

		// The goroutine releases the completion notifier after Shutdown, so
		// the count is the one observable of the shutdown.
		Eventually(goceph.AioUsers).WithTimeout(30 * time.Second).WithPolling(100 * time.Millisecond).Should(Equal(users))
	})
})
