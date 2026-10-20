// Command s3tests decides which s3-tests the parity set runs, from the lists
// beside it, for run.sh, which runs them.
//
//	go run ./hack/s3tests expr [--lists DIR] [--tests FILE]
//	go run ./hack/s3tests select [--lists DIR] [--tests FILE] [--unfiltered FILE] <COLLECTION
//	go run ./hack/s3tests hash [--lists DIR]
//
// The set is the tests pytest collects from test_s3.py and test_headers.py
// less those carrying a marker excluded-markers.txt lists, plus those the
// include lists name, which such a marker removes, and less those the
// deselect lists name. DIR, hack/s3tests by default, holds the lists;
// run.sh passes its own directory. Each list is a line per item; "#" starts
// a comment, and trailing blanks and blank lines are dropped.
//
// expr prints the pytest -m expression that leaves the markers out, "not M1
// and not M2 ...", in the list's order. Given --tests FILE, it first refuses
// a TESTS file that is missing or names no test, as select would, so that a
// run stops on one before it changes anything.
//
// select reads pytest --collect-only -q's output under that expression on
// stdin, and its output for the same files without it from --unfiltered
// FILE, takes their lines that are node ids under s3tests/, and prints the
// ids of the set, a line each, in the order of the collection without the
// expression, for pytest to run as they are. With no such collection,
// which select needs only once the include lists name a test, the order is
// the other's. An include line adds the test it names by its node id, or
// every parametrization of a base id. A deselect line removes the test it
// names by its base id, every parametrization of it. --tests FILE keeps
// only the tests FILE names, a node id or a base id a line, each of which
// the set must hold. select refuses a deselect line that would also remove
// a test of the set it does not name, were it a pytest --deselect prefix; a
// deselect line that names no collected test; an include line that names no
// test of the collection without the expression, or one the markers leave;
// an include line and a deselect line naming the same test; a collection
// without the expression that lacks a test the one under it holds; a TESTS
// line naming a test outside the set; a TESTS file that names none; and a
// set left empty. It prints every refusal, then exits 1.
//
// hash prints the digest each recorded result carries in its deselect meta:
// the first 12 hex digits of the sha256 of the deselect lists' ids and the
// include lists' ids, each of those after a "+", sorted by byte, a line
// each. With no include line, it is the deselect lists' digest alone.
//
// Each flag can also come from the environment, as S3TESTS_LISTS,
// S3TESTS_TESTS, S3TESTS_UNFILTERED, S3TESTS_LOG_LEVEL and so on, or from
// the config file --config or S3TESTS_CONFIG names; a flag given, even
// empty, beats both. The root's --log-level (debug, info, warn or error;
// info by default) and --log-format (json or text; json by default) set the
// logger, which writes to stderr. s3tests exits 0 when it has done its work,
// 1 when select refuses the set or expr the TESTS file, and 2 for anything
// else: a refused command line, a list, a TESTS file or a collection file it
// cannot read.
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
