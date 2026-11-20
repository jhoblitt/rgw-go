package cephtest_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// cephxKey is a cephx key as ceph-authtool writes one: the AES type, a
// creation time and a 16-byte secret. With it librados goes on to the
// monitors rather than failing on a missing keyring.
func cephxKey() string {
	key := binary.LittleEndian.AppendUint16(nil, 1)
	key = binary.LittleEndian.AppendUint64(key, 0)
	key = binary.LittleEndian.AppendUint16(key, 16)
	return base64.StdEncoding.EncodeToString(append(key, make([]byte, 16)...))
}

// deadMon is a loopback address nothing listens on: bound, then released.
// librados connecting there retries until its 300 s mount timeout, so an
// rgw-go given it keeps running until it is stopped.
func deadMon(ctx context.Context) string {
	GinkgoHelper()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	addr := ln.Addr().String()
	Expect(ln.Close()).To(Succeed())
	return addr
}

// procFile is s's process's /proc/<pid>/<file>.
func procFile(s *gexec.Session, file string) string {
	return filepath.Join("/proc", strconv.Itoa(s.Command.Process.Pid), file)
}

// execed waits until s's process runs its new image, whose argv and
// environment the kernel lays out late in exec: after it has closed the
// close-on-exec pipe gexec.Start's StartProcess waits on, so /proc/<pid>/
// cmdline and environ can read empty straight after gexec.Start returns, as
// CI saw. A process that exits first fails the wait.
func execed(s *gexec.Session) {
	GinkgoHelper()
	Eventually(func(g Gomega) []byte {
		g.Expect(s.ExitCode()).To(Equal(-1), "rgw-go exited before it ran")
		b, err := os.ReadFile(procFile(s, "cmdline"))
		g.Expect(err).NotTo(HaveOccurred())
		return b
	}).WithTimeout(10*time.Second).WithPolling(10*time.Millisecond).ShouldNot(BeEmpty(), "rgw-go's argv")
}

// procStrings reads /proc/<pid>/<file>, a NUL-separated list, once the
// process runs its new image.
func procStrings(s *gexec.Session, file string) []string {
	GinkgoHelper()
	execed(s)
	b, err := os.ReadFile(procFile(s, file))
	Expect(err).NotTo(HaveOccurred())
	return strings.Split(string(bytes.TrimSuffix(b, []byte{0})), "\x00")
}

// The binaries StartRGWGo built outlive every spec, so the suite removes
// them once it ends.
var _ = AfterSuite(func() { cephtest.RemoveRGWGoBuilds() })

var _ = Describe("StartRGWGo", func() {
	// cephArgs is radosgw's command line against mon, with no config file.
	cephArgs := func(mon string) []string {
		return []string{"--no-config-file", "--mon-host=" + mon, "--key=" + cephxKey()}
	}

	DescribeTable("starts rgw-go out of the service map whatever the spec process inherited, and leaves it running",
		func(ctx SpecContext, form cephtest.Form, dashDash []string, argv0 string, argv1 []string) {
			GinkgoT().Setenv("RGW_GO_SERVICE_MAP", "true")
			s := cephtest.StartRGWGo(form, append(dashDash, cephArgs(deadMon(ctx))...))
			env := procStrings(s, "environ")
			Expect(env).To(ContainElement("RGW_GO_SERVICE_MAP=false"))
			Expect(env).NotTo(ContainElement("RGW_GO_SERVICE_MAP=true"), "the inherited value")
			cmdline := procStrings(s, "cmdline")
			Expect(filepath.Base(cmdline[0])).To(Equal(argv0))
			Expect(len(cmdline)).To(BeNumerically(">", len(argv1)), "%q", cmdline)
			Expect(cmdline[1 : 1+len(argv1)]).To(Equal(argv1))
			Consistently(s).WithTimeout(time.Second).WithPolling(100*time.Millisecond).ShouldNot(gexec.Exit(),
				"rgw-go waits on the monitor until it is stopped")
		},
		Entry("as rgw-go serve", cephtest.Serve, []string{"--"}, "rgw-go", []string{"serve", "--", "--no-config-file"}),
		Entry("as a copy named radosgw, given radosgw's command line", cephtest.Radosgw, []string(nil), "radosgw",
			[]string{"--no-config-file"}),
	)

	It("lets the caller's environment turn registration back on", func(ctx SpecContext) {
		s := cephtest.StartRGWGo(cephtest.Radosgw, cephArgs(deadMon(ctx)), "RGW_GO_SERVICE_MAP=true")
		env := procStrings(s, "environ")
		Expect(env).To(ContainElement("RGW_GO_SERVICE_MAP=true"))
		Expect(env).NotTo(ContainElement("RGW_GO_SERVICE_MAP=false"))
	})

	It("stops rgw-go with SIGTERM when the spec ends", func(ctx SpecContext) {
		var s *gexec.Session
		// Cleanups run last registered first, so StartRGWGo's stops rgw-go
		// before this one looks.
		DeferCleanup(func() {
			Expect(s).NotTo(BeNil())
			Expect(s.ExitCode()).NotTo(Equal(-1), "rgw-go still running")
			Expect(s.Err).To(gbytes.Say(`"msg":"stopping","signal":"terminated"`))
		})
		s = cephtest.StartRGWGo(cephtest.Serve, append([]string{"--"}, cephArgs(deadMon(ctx))...), "RGW_GO_LOG_LEVEL=debug")
		// rgw-go handles SIGTERM once it runs serve, which sizes the Go
		// memory limit first; one sent before then kills it unlogged.
		Eventually(s.Err).WithTimeout(time.Minute).WithPolling(50 * time.Millisecond).Should(gbytes.Say(`"msg":"go memory limit `))
	})

	It("removes what it built when RemoveRGWGoBuilds runs, and builds again after", func(ctx SpecContext) {
		// exe is the binary s runs, while it runs.
		exe := func(s *gexec.Session) string {
			GinkgoHelper()
			execed(s)
			path, err := os.Readlink(procFile(s, "exe"))
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Base(path)).To(Equal("radosgw"), "the binary rgw-go runs as")
			return path
		}
		stop := func(s *gexec.Session) {
			GinkgoHelper()
			s.Terminate()
			Eventually(s).WithTimeout(time.Minute).WithPolling(50 * time.Millisecond).Should(gexec.Exit())
		}
		first := cephtest.StartRGWGo(cephtest.Radosgw, cephArgs(deadMon(ctx)))
		built := exe(first)
		Expect(built).To(BeAnExistingFile())
		stop(first)
		cephtest.RemoveRGWGoBuilds()
		Expect(filepath.Dir(built)).NotTo(BeADirectory(), "the build directory of %s", built)

		again := cephtest.StartRGWGo(cephtest.Radosgw, cephArgs(deadMon(ctx)))
		rebuilt := exe(again)
		Expect(rebuilt).To(BeAnExistingFile())
		Expect(filepath.Dir(rebuilt)).NotTo(Equal(filepath.Dir(built)))
	})

	It("returns the running process alone, never a path to the binary", func() {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "rgwgo.go", nil, parser.SkipObjectResolution)
		Expect(err).NotTo(HaveOccurred())
		var exported []string
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			exported = append(exported, fn.Name.Name)
			if fn.Name.Name == "RemoveRGWGoBuilds" {
				Expect(fn.Type.Results).To(BeNil(), "RemoveRGWGoBuilds's results")
				continue
			}
			Expect(fn.Type.Results.List).To(HaveLen(1), "%s's results", fn.Name.Name)
			Expect(types.ExprString(fn.Type.Results.List[0].Type)).To(Equal("*gexec.Session"), "%s's result", fn.Name.Name)
		}
		Expect(exported).To(ConsistOf("StartRGWGo", "RemoveRGWGoBuilds"))
	})
})
