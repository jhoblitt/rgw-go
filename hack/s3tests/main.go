// Command s3tests decides which s3-tests the parity set runs, from the lists
// beside it, for run.sh, which runs them.
//
//	go run ./hack/s3tests expr [--lists DIR] [--tests FILE]
//	go run ./hack/s3tests select [--lists DIR] [--tests FILE] <COLLECTION
//	go run ./hack/s3tests hash [--lists DIR]
//
// The set is the tests pytest collects from test_s3.py and test_headers.py
// less those carrying a marker excluded-markers.txt lists, and less those
// the deselect lists name. DIR, hack/s3tests by default, holds the lists;
// run.sh passes its own directory. Each list is a line per item; "#" starts
// a comment, and trailing blanks and blank lines are dropped.
//
// expr prints the pytest -m expression that leaves the markers out, "not M1
// and not M2 ...", in the list's order. Given --tests FILE, it first refuses
// a TESTS file that is missing or names no test, as select would, so that a
// run stops on one before it changes anything.
//
// select reads pytest --collect-only -q's output under that expression on
// stdin, takes its lines that are node ids under s3tests/, and prints the
// ids of the set, a line each, in collection order, for pytest to run as
// they are. A deselect line removes the test it names by its base id, every
// parametrization of it. --tests FILE keeps only the tests FILE names, a
// node id or a base id a line, each of which the set must hold. select
// refuses a deselect line that would also remove a collected test it does
// not name, were it a pytest --deselect prefix; a deselect line that names
// no collected test; a TESTS line naming a test outside the set; a TESTS
// file that names none; and a set left empty. It prints every refusal, then
// exits 1.
//
// hash prints the digest each recorded result carries in its deselect meta:
// the first 12 hex digits of the sha256 of the deselect lists' ids, sorted
// by byte, a line each.
//
// Each flag can also come from the environment, as S3TESTS_LISTS,
// S3TESTS_TESTS, S3TESTS_LOG_LEVEL and so on, or from the config file
// --config or S3TESTS_CONFIG names; a flag given, even empty, beats both.
// The root's --log-level (debug, info, warn or error; info by default) and
// --log-format (json or text; json by default) set the logger, which writes
// to stderr. s3tests exits 0 when it has done its work, 1 when select
// refuses the set or expr the TESTS file, and 2 for anything else: a refused
// command line, a list or a TESTS file it cannot read.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := selection.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		for line := range strings.SplitSeq(err.Error(), "\n") {
			fmt.Fprintln(os.Stderr, "s3tests: "+line)
		}
	}
	return selection.ExitStatus(err)
}
