package gateway_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
)

// fakeAdmin is radosgw-admin bucket stats as the lookup runs it: the stats on
// standard output, which the lookup discards, and an exit status by bucket,
// with radosgw-admin's failure line on standard error, as bucket stats writes
// one. It exits 3 unless it was given the site the lookup was asked for.
const fakeAdmin = `#!/bin/sh
echo '{"bucket": "stats"}'
[ "$#" -eq 7 ] && [ "$1 $2 $3" = "bucket stats --bucket" ] &&
	[ "$5 $6 $7" = "--rgw-realm=r --rgw-zonegroup=zg --rgw-zone=z" ] || { echo "unexpected arguments: $*" >&2; exit 3; }
case "$4" in
bench-small|*\;*) exit 0 ;;
bench-gone) echo "failure: (2002) Unknown error 2002: " >&2; exit 2 ;;
*) echo "failure: (110) Connection timed out: " >&2; exit 1 ;;
esac
`

var _ = Describe("the bucket lookup", func() {
	DescribeTable("reads radosgw-admin's exit status off the last line the toolbox's sh prints, the bucket an argument of the script",
		func(ctx SpecContext, bucket string, exists bool, failure string) {
			bin := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(bin, "radosgw-admin"), []byte(fakeAdmin), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
			host := harness.NewExecer(time.Second)
			ex := &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				// The toolbox's command, run on this host.
				Expect(c.Args[:len(toolboxArgs)]).To(Equal(toolboxArgs))
				local := c.Args[len(toolboxArgs):]
				return host.Output(ctx, harness.Cmd{Name: local[0], Args: local[1:], Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH")}})
			}}
			got, err := gateway.LookUpBucket(ctx, ex, bucket)
			if failure != "" {
				Expect(err).To(MatchError(failure))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(exists))
		},
		Entry("a bucket that exists: exit status 0", "bench-small", true, ""),
		Entry("a bucket that does not: exit status 2", "bench-gone", false, ""),
		Entry("another status, which says nothing of the bucket", "bench-sick", false,
			"radosgw-admin bucket stats --bucket bench-sick exited 1: failure: (110) Connection timed out:"),
		// Spliced into the script, the name would end the shell before its status
		// line.
		Entry("a name the shell would read as commands, which reaches radosgw-admin whole",
			"bench-small; exit 2", true, ""),
	)

	DescribeTable("fails closed when what comes back tells nothing of the bucket",
		func(ctx SpecContext, out string, callErr error, failure string) {
			ex := &harnessfakes.FakeExecer{}
			ex.OutputReturns([]byte(out), callErr)
			got, err := gateway.LookUpBucket(ctx, ex, "bench-small")
			Expect(err).To(MatchError(ContainSubstring(failure)))
			Expect(got).To(BeFalse())
		},
		Entry("an output with no status line", "failure: (110) Connection timed out\n", nil,
			`radosgw-admin bucket stats --bucket bench-small ended in no exit status: "failure: (110) Connection timed out\n"`),
		Entry("no output at all", "", nil, `radosgw-admin bucket stats --bucket bench-small ended in no exit status: ""`),
		Entry("a status line with no number", "\nradosgw-admin-status=\n", nil, "ended in no exit status"),
		Entry("a status line followed by more", "\nradosgw-admin-status=2\nSegmentation fault\n", nil, "ended in no exit status"),
		Entry("a toolbox that failed, whatever it printed", "\nradosgw-admin-status=2\n",
			errors.New("exit status 1: error: unable to upgrade connection: container not found"), "unable to upgrade connection"),
	)
})
