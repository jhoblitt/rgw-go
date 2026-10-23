// Command s3tests decides which s3-tests the parity set runs, from the lists
// beside it, for run.sh, which runs them, and readies a release's rooket
// cluster for the lifecycle and MFA tests among them.
//
//	go run ./hack/s3tests expr [--lists DIR] [--set pr|gate] [--tests FILE]
//	go run ./hack/s3tests select [--lists DIR] [--set pr|gate] [--tests FILE] [--unfiltered FILE]
//	    [--gate-only-ids FILE] <COLLECTION
//	go run ./hack/s3tests hash [--lists DIR]
//	go run ./hack/s3tests lc-mode --release squid|tentacle --mode off|radosgw|rgw-go|both [--rooket PATH]
//	    [--root DIR] [--make PATH]
//	go run ./hack/s3tests classes [--release squid|tentacle [--rooket PATH] [--root DIR]]
//	go run ./hack/s3tests mfa --release squid|tentacle --uid UID [--rooket PATH]
//
// The gate set is the tests pytest collects from test_s3.py and
// test_headers.py less those carrying a marker excluded-markers.txt lists,
// plus those the include lists name, which such a marker removes, and less
// those the deselect lists name. The pr set, which a pull request's check
// runs, is the gate set less the tests gate-only.txt names, which only a
// phase gate and a run on demand afford; every recording runs the gate set,
// which --set gate, the default, selects. DIR, hack/s3tests by default,
// holds the lists; run.sh passes its own directory. Each list is a line per
// item; "#" starts a comment, and trailing blanks and blank lines are
// dropped.
//
// expr prints the pytest -m expression that leaves the markers out, "not M1
// and not M2 ...", in the list's order. It first refuses a --set that is
// neither pr nor gate, and, given --tests FILE, a TESTS file that is missing
// or names no test, as select would, so that a run stops on either before it
// changes anything.
//
// select reads pytest --collect-only -q's output under that expression on
// stdin, and its output for the same files without it from --unfiltered
// FILE, takes their lines that are node ids under s3tests/, and prints the
// ids of the set --set names, a line each, in the order of the collection
// without the expression, for pytest to run as they are. With no such
// collection, which select needs only once the include lists name a test,
// the order is the other's. An include line adds the test it names by its
// node id, or every parametrization of a base id. A deselect line removes
// the test it names by its base id, every parametrization of it. A
// gate-only line names a test of the gate set by its node id, or every
// parametrization of a base id the gate set holds, and the pr set leaves
// those out. --tests FILE keeps only the tests FILE names, a node id or a
// base id a line, each of which the set must hold, so a TESTS file names a
// gate-only test only for the gate set. select refuses a deselect line that
// would also remove a test of the set it does not name, were it a pytest
// --deselect prefix; a deselect line that names no collected test; an
// include line that names no test of the collection without the
// expression, or one the markers leave; an include line and a deselect line
// naming the same test; a collection without the expression that lacks a
// test the one under it holds; a gate-only line naming no test of the gate
// set, whichever set it selects; a TESTS line naming a test outside the set;
// a TESTS file that names none; and a set left empty. It prints every
// refusal, then exits 1. --gate-only-ids FILE writes, once the set is
// selected, the node ids of the gate set's tests the gate-only lines name, a
// line each, whichever set it selects, for hack/parity diff --gate-only.
//
// hash prints the digest each recorded result carries in its deselect meta:
// the first 12 hex digits of the sha256 of the deselect lists' ids and the
// include lists' ids, each of those after a "+", sorted by byte, a line
// each. With no include line, it is the deselect lists' digest alone. The
// gate-only lines stay out, so that a run of either set carries the digest.
//
// lc-mode sets an LC mode, the gateways whose lifecycle workers run: off,
// the parity mode, under which phase 1's set is recorded and compared,
// radosgw, rgw-go, or both. It sets radosgw's rgw_enable_lc_threads in the
// object store's rgwConfig, waits for Rook to reconcile it, and restarts the
// radosgw when the one running started before the option last changed,
// waiting until the service map lists one started after the change. It then
// restarts rgw-go through make -C ROOT rgw-go-up, its option given as
// RGW_GO_LC_THREADS and the serve flags rgw-go-up.sh recorded in
// rgw-go.flags as RGW_GO_FLAGS, when it runs with another option, and starts
// it, with none, when none runs and the mode runs its worker. A gateway that
// already runs as the mode says is left alone. It prints what each gateway
// runs with, a line each.
//
// classes prints the storage classes the s3-tests conf names beside
// STANDARD, comma-separated, as run.sh writes them into storage_classes.
// Given --release, it first adds them to the default placement of the
// radosgw's zonegroup and zone, on the STANDARD class's data pool and
// uncompressed, with the Rook operator's radosgw-admin, and commits the
// realm's period when that changed it or the period lacks one, waiting for
// the radosgw to reload it; it prints the classes' place instead, and warns
// when an rgw-go runs, which reads the zone only when it starts.
//
// mfa gives the user UID the TOTP device the s3-tests conf names for its
// MFA tests, afresh: it removes a device of that serial the user has, so
// that no earlier run's replay state carries over, creates it with the
// conf's seed, and checks that the user's record lists it and that it holds
// that seed. It then prints the conf's mfa_serial and mfa_seed lines, which
// run.sh writes into [s3 main].
//
// Each flag can also come from the environment, as S3TESTS_LISTS,
// S3TESTS_SET, S3TESTS_TESTS, S3TESTS_UNFILTERED, S3TESTS_GATE_ONLY_IDS,
// S3TESTS_MODE, S3TESTS_LOG_LEVEL and so on, or from the config file
// --config or S3TESTS_CONFIG names; a flag given, even empty, beats both.
// The root's --log-level (debug, info, warn or error; info by default) and
// --log-format (json or text; json by default) set the logger, which writes
// to stderr. s3tests exits 0 when it has done its work, 1 when select
// refuses the set or expr the TESTS file, and 2 for anything else: a refused
// command line, a --set other than pr or gate among them, a list, a TESTS
// file or a collection file it cannot read, a --gate-only-ids file it cannot
// write, and a cluster lc-mode, classes or mfa could not ready.
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
