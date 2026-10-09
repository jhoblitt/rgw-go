// Command parity records test outcomes from a suite's runs and compares a
// candidate's outcomes with a baseline's: radosgw's are the baseline that
// rgw-go's must match.
//
//	go run ./hack/parity record --format junit|gotest --suite NAME --out FILE [--meta k=v]... RUN-FILE [RUN-FILE...]
//	go run ./hack/parity diff --baseline FILE --candidate FILE [--known FILE] [--allow-meta-drift]
//
// --meta is read from the command line only. Every other flag can also come
// from the environment, as PARITY_ and its name upper-cased with "-" as "_"
// (PARITY_OUT, PARITY_ALLOW_META_DRIFT, PARITY_CONFIG), and every other flag
// but --config from the config file --config names, under its own name, in
// any format viper reads by the file's extension (YAML, JSON, TOML). The
// command line wins over the environment, and the environment over the file.
// The root's --log-level (debug, info, warn or error; info by default) and
// --log-format (json or text; json by default) set the logger, which writes
// to stderr.
//
// record reads the run files, junit XML reports as pytest writes them or go
// test -json event streams, and writes a result file. A junit test's id is
// its classname, "::" and its name; pytest reports a test whose call failed
// and whose teardown errored twice, and the error wins. A go test id is the
// Test field of the test's terminal pass, fail or skip event; package events
// are ignored, and a test that ran subtests is left to them unless it failed
// while none of them did. Outcomes come from the last run file, and a test
// whose outcome differs between any two of them is listed as unstable. Every
// test must appear in every run, or record fails naming the missing tests.
//
// diff compares the candidate with the baseline, skipping the baseline's
// unstable tests, and prints "<test>: baseline <o1>, candidate <o2>" for each
// test whose outcome differs, "missing" standing for a test one result
// lacks. --known names a known-difference list, the tests whose outcomes are
// expected to differ: one regular expression per line, matched against the
// whole test id, with "#" starting a comment. A listed test that differs is
// skipped; one that agrees prints "<test>: known difference no longer
// differs", and an entry matching no compared test prints "<pattern>: known
// difference matches no test", so a stale entry fails the run. A line
// starting with "~ " is an either-outcome entry, for a test whose outcome
// depends on timing: its test is skipped whether it differs or agrees, and
// only an entry that matches no test is reported.
//
// diff refuses to compare when the candidate lacks any of s3tests_commit,
// go_ceph_tag, release, ceph_version and deselect that the baseline's meta
// carries, or carries one with another value; --allow-meta-drift lets the
// values differ, never a key go missing. A key only the candidate carries is
// not compared.
//
// diff exits 0 when it prints nothing and 1 when it prints a difference;
// record exits 1 when it cannot record. Both exit 2 for a refused command
// line, and diff for results it refuses to compare or cannot read. go run
// reports any nonzero status as 1, and make any failed recipe as 2, so a
// caller that tells the statuses apart runs a binary go build made.
//
// A result file is JSON:
//
//	{
//	  "suite": "s3tests",
//	  "meta": {
//	    "ceph_version": "19.2.6",
//	    "deselect": "136c2f295daa",
//	    "gateway": "radosgw",
//	    "recorded": "2026-09-28T03:11:00Z",
//	    "release": "squid",
//	    "runs": "2",
//	    "s3tests_commit": "5522d1c351f75bc00ae0f64f742f3f095f5939d9"
//	  },
//	  "outcomes": {
//	    "s3tests.functional.test_s3::test_bucket_list_empty": "passed",
//	    "s3tests.functional.test_s3::test_bucket_list_maxkeys_none": "failed"
//	  },
//	  "unstable": ["s3tests.functional.test_s3::test_bucket_list_maxkeys_none"]
//	}
//
// An outcome is passed, failed, error or skipped. Every meta value is a
// string: the --meta pairs given to record, with runs, the number of run
// files, and recorded, the UTC time of recording, which record sets itself.
// An s3-tests result's s3tests_commit and deselect are hack/s3tests/run.sh's
// --print-commit and --print-deselect for the run, so a baseline recorded
// before the pin or a deselect list changed is refused like one from another
// release.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := parity.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parity:", err)
	}
	return parity.ExitStatus(err)
}
