package cli_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// run runs rgw-go with args and returns what it printed. Run installs a
// default logger writing to stderr, so the previous one, and the log
// package's output it redirects, come back when the spec ends.
func run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	var out, errOut bytes.Buffer
	err = cli.Run(ctx, args, strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

func unsetenv(key string) {
	if v, ok := os.LookupEnv(key); ok {
		Expect(os.Unsetenv(key)).To(Succeed())
		DeferCleanup(os.Setenv, key, v)
	}
}

var _ = Describe("Run", func() {
	It("prints the version", func() {
		var out bytes.Buffer
		err := cli.Run(context.Background(), []string{"version"}, strings.NewReader(""), &out, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(Equal(version.String()+"\n"), "version output")
	})
	DescribeTable("installs a logger that puts a request's id on the lines logged under its context, and only on those",
		func(ctx SpecContext, format, inside, outside string) {
			oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
			DeferCleanup(func() {
				slog.SetDefault(oldLogger)
				log.SetOutput(oldWriter)
				log.SetFlags(oldFlags)
			})
			var logs bytes.Buffer
			Expect(cli.Run(ctx, []string{"--log-format=" + format, "version"}, strings.NewReader(""), io.Discard, &logs)).To(Succeed())
			slog.InfoContext(op.WithRequestID(ctx, "tx1"), "inside")
			slog.InfoContext(ctx, "outside")
			Expect(logs.String()).To(ContainSubstring(inside))
			Expect(logs.String()).To(ContainSubstring(outside))
			Expect(strings.Count(logs.String(), "request_id")).To(Equal(1))
		},
		Entry("as JSON", "json", `"msg":"inside","request_id":"tx1"}`, `"msg":"outside"}`),
		Entry("as text", "text", "msg=inside request_id=tx1\n", "msg=outside\n"),
	)
})

var _ = Describe("Argv", func() {
	It("passes rgw-go's own argv through", func() {
		Expect(cli.Argv([]string{"/usr/bin/rgw-go", "version"})).To(Equal([]string{"version"}))
	})
	It("rewrites radosgw's argv into serve -- args", func() {
		Expect(cli.Argv([]string{"/usr/bin/radosgw", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"})).
			To(Equal([]string{"serve", "--", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"}))
	})
	It("matches the base name only", func() {
		Expect(cli.Argv([]string{"radosgw"})).To(Equal([]string{"serve", "--"}))
		Expect(cli.Argv([]string{"/opt/radosgw-wrapper", "x"})).To(Equal([]string{"x"}))
	})
})

// Every serve spec that could get past its early arguments names a -c file
// that does not exist, so a wrong turn fails reading it instead of
// connecting anywhere.
var _ = Describe("serve", func() {
	var missing string

	BeforeEach(func() {
		for _, key := range []string{"CEPH_ARGS", "CEPH_CONF", "RGW_GO_METRICS_ADDR", "RGW_GO_RADOS_COMPLETIONS"} {
			unsetenv(key)
		}
		missing = filepath.Join(GinkgoT().TempDir(), "missing.conf")
	})

	It("prints the version and stops", func(ctx SpecContext) {
		out, _, err := run(ctx, "serve", "--", "-c", missing, "-v")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(version.String() + "\n"))
	})

	It("prints the version on --version when invoked as radosgw", func(ctx SpecContext) {
		out, _, err := run(ctx, cli.Argv([]string{"/usr/bin/radosgw", "-c", missing, "--version"})...)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(version.String() + "\n"))
	})

	It("refuses an unknown completion mode before reading any configuration", func(ctx SpecContext) {
		_, _, err := run(ctx, "serve", "--rados-completions=bogus", "--", "-c", missing, "--id=x")
		Expect(err).To(MatchError(ContainSubstring(`--rados-completions "bogus"`)))
		Expect(err).NotTo(MatchError(radosclient.ErrNotFound))
	})

	It("reads the completion mode from RGW_GO_RADOS_COMPLETIONS", func(ctx SpecContext) {
		GinkgoT().Setenv("RGW_GO_RADOS_COMPLETIONS", "pipes")
		_, _, err := run(ctx, "serve", "--", "-c", missing, "--id=x")
		Expect(err).To(MatchError(ContainSubstring(`"pipes"`)))
	})

	It("logs the settings it resolved, the metrics address from RGW_GO_METRICS_ADDR", func(ctx SpecContext) {
		GinkgoT().Setenv("RGW_GO_METRICS_ADDR", "127.0.0.1:9283")
		_, logs, err := run(ctx, "--log-level=debug", "serve", "--", "-c", missing, "-v")
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring(`"msg":"serve settings","metrics_addr":"127.0.0.1:9283","rados_completions":"callback"`))
	})

	It("takes --metrics-addr over RGW_GO_METRICS_ADDR", func(ctx SpecContext) {
		GinkgoT().Setenv("RGW_GO_METRICS_ADDR", "127.0.0.1:9283")
		_, logs, err := run(ctx, "--log-level=debug", "serve", "--metrics-addr=:9100", "--", "-c", missing, "-v")
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring(`"metrics_addr":":9100"`))
	})

	It("exits 1 on an empty command line, as radosgw does", func(ctx SpecContext) {
		for _, args := range [][]string{{"serve"}, {"serve", "--"}, cli.Argv([]string{"radosgw"})} {
			_, _, err := run(ctx, args...)
			Expect(err).To(MatchError("-h or --help for usage"), "%q", args)
		}
	})

	It("does not count CEPH_ARGS toward the command line", func(ctx SpecContext) {
		GinkgoT().Setenv("CEPH_ARGS", "-v")
		_, _, err := run(ctx, "serve", "--")
		Expect(err).To(MatchError("-h or --help for usage"))
	})

	DescribeTable("prints radosgw's usage on -h or --help and stops",
		func(ctx SpecContext, args []string) {
			out, _, err := run(ctx, slices.Concat([]string{"serve", "--", "-c", missing}, args)...)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(HavePrefix("usage: radosgw [options...]\noptions:\n"))
			Expect(out).To(ContainSubstring("  --setgroup GROUP  set gid to group or gid\n"))
			Expect(out).To(ContainSubstring("  RGW_GO_METRICS_ADDR "))
		},
		Entry("-h", []string{"-h"}),
		Entry("--help ahead of other arguments", []string{"--help", "--id=x"}),
		Entry("--help past a --, which radosgw's check does not stop at", []string{"--", "--help"}),
	)

	It("leaves -h in CEPH_ARGS alone, as radosgw checks only its command line for it", func(ctx SpecContext) {
		GinkgoT().Setenv("CEPH_ARGS", "-h -v")
		out, _, err := run(ctx, "serve", "--", "-c", missing)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(version.String() + "\n"))
	})

	It("reads early arguments from CEPH_ARGS ahead of the command line's", func(ctx SpecContext) {
		GinkgoT().Setenv("CEPH_ARGS", "--name=rgw.a")
		_, _, err := run(ctx, "serve", "--", "-c", missing, "-v")
		Expect(err).To(MatchError(ContainSubstring(`parsing name "rgw.a"`)))
	})

	It("keeps what follows -- in CEPH_ARGS out of the early arguments", func(ctx SpecContext) {
		GinkgoT().Setenv("CEPH_ARGS", "-c "+missing+" -- --name=rgw.a")
		out, _, err := run(ctx, "serve", "--", "-v")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(version.String() + "\n"))
	})
})
