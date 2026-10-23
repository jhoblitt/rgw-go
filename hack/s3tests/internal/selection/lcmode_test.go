package selection_test

import (
	"context"
	"errors"
	"strings"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

var _ = Describe("the s3tests command's cluster subcommands", func() {
	var fake *harnessfakes.FakeExecer

	BeforeEach(func() {
		fake = &harnessfakes.FakeExecer{}
		fake.OutputReturns(nil, errors.New("exit status 1"))
	})

	runWith := func(ctx context.Context, args ...string) (status int, stdout, stderr string) {
		var out, errOut strings.Builder
		// Each reading of the clock is an hour on, so that every wait ends
		// after its first try.
		clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		err := selection.RunWith(ctx, args, strings.NewReader(""), &out, &errOut, clusterconf.Deps{
			Exec: fake, Proc: fstest.MapFS{},
			Now: func() time.Time {
				clock = clock.Add(time.Hour)
				return clock
			},
			Sleep: func(context.Context, time.Duration) error { return errors.New("the spec does not wait") },
		})
		if err != nil {
			errOut.WriteString(err.Error())
		}
		return selection.ExitStatus(err), out.String(), errOut.String()
	}

	It("prints the storage classes the conf names, as storage_classes takes them, reaching no cluster", func(ctx SpecContext) {
		status, stdout, stderr := runWith(ctx, "classes")
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("LUKEWARM, FROZEN\n"))
		Expect(fake.OutputCallCount()).To(BeZero())
	})

	It("prints them alone given the empty release run.sh gives it, whatever S3TESTS_RELEASE says", func(ctx SpecContext) {
		setenv("S3TESTS_RELEASE", "squid")
		status, stdout, stderr := runWith(ctx, "classes", "--release", "")
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("LUKEWARM, FROZEN\n"))
		Expect(fake.OutputCallCount()).To(BeZero())
	})

	It("refuses classes for a release other than squid or tentacle before it reaches the cluster", func(ctx SpecContext) {
		status, _, stderr := runWith(ctx, "classes", "--release", "reef")
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring(`--release must be squid or tentacle, not "reef"`))
		Expect(fake.OutputCallCount()).To(BeZero())
	})

	It("adds the classes to the release's cluster through the rooket it is given", func(ctx SpecContext) {
		status, _, stderr := runWith(ctx, "classes", "--release", "squid", "--rooket", "/opt/rooket")
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("the service map did not settle to one radosgw"))
		_, c := fake.OutputArgsForCall(0)
		Expect(c.Name).To(Equal("/opt/rooket"))
		Expect(c.Env).To(Equal([]string{"ROOKET_NAME=rgw-go-squid"}))
	})

	DescribeTable("refuses mfa's command line before it reaches the cluster, and exits 2",
		func(ctx SpecContext, want string, args ...string) {
			status, stdout, stderr := runWith(ctx, append([]string{"mfa"}, args...)...)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring(want))
			Expect(fake.OutputCallCount()).To(BeZero())
		},
		Entry("no release", `--release must be squid or tentacle, not ""`, "--uid", "s3tests-main"),
		Entry("another release", `--release must be squid or tentacle, not "reef"`, "--release", "reef", "--uid", "s3tests-main"),
		Entry("no user", "--uid names no user", "--release", "squid"),
	)

	It("gives the device on the release's cluster through the rooket it is given, printing nothing when it cannot", func(ctx SpecContext) {
		status, stdout, stderr := runWith(ctx, "mfa", "--release", "tentacle", "--rooket", "/opt/rooket", "--uid", "s3tests-main")
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("giving the user s3tests-main the MFA device s3tests-totp: "))
		_, c := fake.OutputArgsForCall(0)
		Expect(c.Name).To(Equal("/opt/rooket"))
		Expect(c.Env).To(Equal([]string{"ROOKET_NAME=rgw-go-tentacle"}))
	})

	DescribeTable("refuses lc-mode's command line before it reaches the cluster, and exits 2",
		func(ctx SpecContext, want string, args ...string) {
			status, stdout, stderr := runWith(ctx, append([]string{"lc-mode"}, args...)...)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring(want))
			Expect(fake.OutputCallCount()).To(BeZero())
		},
		Entry("a release other than squid or tentacle", `--release must be squid or tentacle, not "reef"`,
			"--release", "reef", "--mode", "off"),
		Entry("no release", `--release must be squid or tentacle, not ""`, "--mode", "off"),
		Entry("a mode it does not know", `--mode must be off, radosgw, rgw-go or both, not "all"`,
			"--release", "squid", "--mode", "all"),
		Entry("no mode", `--mode must be off, radosgw, rgw-go or both, not ""`, "--release", "squid"),
	)

	It("reaches the release's cluster through the rooket it is given, and exits 2 when it cannot", func(ctx SpecContext) {
		status, _, stderr := runWith(ctx, "lc-mode", "--release", "tentacle", "--mode", "radosgw", "--rooket", "/opt/rooket")
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("the service map did not settle to one radosgw"))
		Expect(fake.OutputCallCount()).To(Equal(1), "the first wait ends after its first try")
		_, c := fake.OutputArgsForCall(0)
		Expect(c).To(Equal(harness.Cmd{
			Name: "/opt/rooket",
			Args: []string{
				"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--",
				"ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20", "service", "dump", "-f", "json",
			},
			Env: []string{"ROOKET_NAME=rgw-go-tentacle"},
		}))
	})
})
