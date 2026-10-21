// Command parity records test outcomes from a suite's runs and compares a
// candidate's outcomes with a baseline's: radosgw's are the baseline that
// rgw-go's must match. A baseline is a ledger, kept append-only; a
// candidate is a result file.
//
//	go run ./hack/parity record --format junit|gotest --suite NAME --out FILE [--pin-file FILE] [--meta k=v]...
//	    RUN-FILE...
//	go run ./hack/parity record --format junit|gotest --suite NAME --ledger FILE [--subset] [--reasons FILE]
//	    [--draft NOTE] [--pin-reason REASON] [--pin-file FILE] [--meta k=v]... RUN-FILE RUN-FILE...
//	go run ./hack/parity diff --baseline LEDGER --candidate FILE [--known FILE]
//	    [--selection FILE [--gate-only FILE]] [--summary FILE] [--allow-meta-drift]
//	go run ./hack/parity check --old LEDGER --new LEDGER [--list-reasoned] [--draft]
//	go run ./hack/parity check --base COMMIT [--head COMMIT] [--repo DIR] [--run-sh FILE] [--pin-file FILE]
//	    [--clone DIR | --ceph-repo URL] [--summary FILE] [PATH...]
//	go run ./hack/parity validate LEDGER...
//	go run ./hack/parity pin --clone CLONE --new COMMIT [--run-sh FILE] [--pin-file FILE] [--ceph-repo URL]
//	    [--ledger FILE]... [--tests FILE]
//	go run ./hack/parity migrate BASELINE...
//	go run ./hack/parity fmt LEDGER...
//
// --meta, record's --draft and --pin-reason, diff's --selection and
// --gate-only, check's --draft and pin's --ledger are read from the command
// line only. Every other flag can also come from the environment, as PARITY_
// and its name upper-cased with "-" as "_" (PARITY_OUT,
// PARITY_ALLOW_META_DRIFT, PARITY_CONFIG), and every other flag but --config
// from the config file --config names, under its own name, in any format
// viper reads by the file's extension (YAML, JSON, TOML). The command line
// wins over the environment, and the environment over the file.
// The root's --log-level (debug, info, warn or error; info by default) and
// --log-format (json or text; json by default) set the logger, which writes
// to stderr.
//
// # Recording
//
// record reads the run files, junit XML reports as pytest writes them or go
// test -json event streams. A junit test's id is its classname, "::" and its
// name; pytest reports a test whose call failed and whose teardown errored
// twice, and the error wins. A go test id is the Test field of the test's
// terminal pass, fail or skip event; package events are ignored, and a test
// that ran subtests is left to them unless it failed while none of them
// did. Outcomes come from the last run file, and a test whose outcome
// differs between any two of them is unstable. Every test must appear in
// every run, or record fails naming the missing tests. record adds runs, the
// number of run files, and recorded, the UTC time of recording, to the
// --meta pairs.
//
// With --out it writes a result file:
//
//	{
//	  "suite": "s3tests",
//	  "meta": {"ceph_version": "19.2.6", "gateway": "rgw-go", "recorded": "2026-10-12T18:45:05Z",
//	           "release": "squid", "runs": "1", "s3tests_commit": "f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3"},
//	  "outcomes": {"s3tests.functional.test_s3::test_bucket_list_empty": "passed"},
//	  "unstable": []
//	}
//
// An outcome is passed, failed, error or skipped, and every meta value a
// string.
//
// With --ledger it appends to a ledger, from at least two run files so that
// a flaky test shows as unstable, and creates the ledger when the file is
// missing. The meta must carry release, ceph_version and one source key:
// s3tests_commit for s3-tests, hack/s3tests/run.sh --print-commit, or
// go_ceph_tag for go-ceph's rgw/admin suite. Its other pairs, such as
// gateway, deselect and pytest_freeze, go into each entry's meta.
//
// --pin-file FILE refuses an s3-tests record whose s3tests_commit is not
// the pin the PIN file FILE holds (see Pinning): a run at another commit,
// as S3TESTS_COMMIT in the environment gives run.sh to try a change, is
// never recorded.
//
// # The ledger
//
//	{
//	  "suite": "s3tests",
//	  "release": "squid",
//	  "source_key": "s3tests_commit",
//	  "pins": [
//	    {"source":"f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3","admitted":"base"},
//	    {"source":"0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","admitted":"additive"}
//	  ],
//	  "tests": {
//	    "s3tests.functional.test_s3::test_bucket_list_empty": [
//	      {"outcome":"passed","source":"f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3","ceph_version":"19.2.6",
//	       "recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false,"meta":{"gateway":"radosgw"}},
//	      {"outcome":"failed","source":"0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","ceph_version":"19.2.7",
//	       "recorded":"2026-11-02T09:12:40Z","runs":2,"unstable":false,"reason":"radosgw 19.2.7 answers 403"}
//	    ]
//	  }
//	}
//
// The file holds each pin and each entry on a line of its own. pins is the
// chain of sources the suite has been pinned at, oldest first, each admitted
// one of three ways: base, recorded whole by a full record; additive, proven
// by pin to only add tests over the pin before it; or reworded, proven by pin
// to have the same trees as the pin before it. The first pin is a base pin,
// and no source is pinned twice. A base pin after the first re-stamps every
// outcome at a source no proof ties to the one before, so it carries a
// reason, as an entry does under the reason rule below, and a pin's reason is
// never changed. Each test holds its entries, oldest first, and its state is
// its last entry. An entry's outcome is passed, failed, error, skipped or
// retired, for a test the selection no longer runs; its source is a pinned
// source no earlier than the previous entry's; it was recorded from at least
// two runs; a test's first entry cannot retire it, and a retired entry is not
// unstable; an entry's meta names no field of an entry, no source key and no
// release, in any case. A test id, a source, a suite, a release and a Ceph
// version are printable ASCII with no blank, so that no letter passes for
// another, and do not start with "-"; no text in the file holds a control or
// format character.
//
// Every JSON file parity reads, a ledger, a result or a flat baseline, goes
// through one strict decoder, which refuses what another reader of the same
// bytes could read otherwise: a byte order mark, text that is not UTF-8,
// anything after the value, a member the schema does not hold, a member
// named twice, and a member named apart from the schema's only in case,
// which encoding/json would fold into it. A ledger must also be byte for
// byte the form parity writes, so that a person reading its diff, a
// line-based tool and a JSON library read the same thing; fmt rewrites a
// ledger the strict decoder reads into that form.
//
// The reason rule: an entry needs a reason, one that holds a letter or a
// digit, when it changes the previous entry's outcome, changes its unstable
// flag, or retires the test.
// Two unstable entries' outcomes are not compared, since an unstable test's
// outcome is whichever run came last. The rule keeps a radosgw regression
// from entering the baseline unremarked, where a failure recorded as the
// baseline would let the same failure on rgw-go pass.
//
// A reason whose first word is "unreviewed", in any case and after anything
// that is not a letter or a digit, marks a draft: a record made with --draft
// NOTE gives every entry that needs a reason --reasons does not give, and a
// base pin --pin-reason gives none, the reason "unreviewed: NOTE". diff
// compares with a draft, so that a record can be compared before anyone
// explains its changes, but validate and check refuse one, so a draft is
// never committed as it is; record refuses a reasons file that gives such a
// reason itself.
//
// Entries and pins are never edited, removed or reordered, only appended.
// check --old --new fails, printing each, on any way the new ledger is not
// the old one with pins and entries appended, or breaks the schema or the
// reason rule; --draft accepts drafts, and --list-reasoned then prints "pin
// <n> (<source>, <admitted>): <reason>" for each appended pin with a reason,
// and "<test>: <old state> -> <new state>: <reason>" for each appended entry
// that needed one. validate checks ledgers alone against the schema and the
// reason rule.
//
// check --base COMMIT judges every ledger under the PATHs of the repository
// --repo names, test/s3tests/baseline and test/admin/baseline when none is
// given, against COMMIT's, as a pull request's check does: a ledger COMMIT
// holds is never removed, and must only be appended to, as check --old --new
// checks; a new ledger must be valid; a ledger COMMIT holds in the flat form
// is judged against its conversion; none may be a draft, a file other than a
// regular one, or a path other than letters, digits, ".", "_", "/" and "-";
// and a ledger the working tree adds or changes outside the PATHs is refused,
// so none escapes the check. --head COMMIT judges COMMIT's ledgers instead of
// the working tree's, reading every file of it from git as data, so that a
// job can judge a pull request it never checks out. Every s3-tests pin the
// judged side brings in, each pin an s3-tests ledger appends and the PIN
// file's when it differs from COMMIT's, must be on rgw-go-s3tests's history,
// as pin requires (see Pinning); the branch is fetched from --ceph-repo or
// COMMIT's run.sh's S3TESTS_REPO, never asked of --clone. The judged PIN file
// must hold a pin by the rule, and the judged run.sh is checked for the slips
// Pinning describes. Each appended additive or reworded pin of an s3-tests
// ledger, a pair of full commit ids, is proven again, as pin proves it, with
// COMMIT's run.sh, never the working tree's, in --clone, or else in a clone
// that fetches the pins' commits without their trees from --ceph-repo,
// COMMIT's run.sh's S3TESTS_REPO by default, an https:// or file:// URL; it
// must be admitted as the proof admits it. --summary appends, for each
// changed ledger, its appended pins, each base pin with its reason and every
// outcome it re-stamps, counting those it carries forward unchanged, and
// every appended entry that changes an outcome or an unstable flag or retires
// a test, with its reason, as markdown, each value from the ledger in a code
// span or a code block it cannot close.
//
// # Appending
//
// The record's source must be the chain's head, or, for a full record, a
// source the chain does not pin, which then joins it as a base pin whose
// reason is --pin-reason, or with --draft the draft's; record refuses such a
// record with neither, and --pin-reason for a record that appends no pin.
// record appends an entry for each test it ran whose last entry differs in
// outcome, unstable flag or ceph_version, or is retired or not valid (see
// diff), and leaves the others alone: a full record under a new Ceph version
// appends for every test. A full record also retires every ledger test it did
// not run. --subset marks a record of chosen tests: it appends only those,
// retires nothing, and runs at the head; it cannot start a ledger. An append
// that needs a reason takes it from --reasons, a file whose lines are a test
// id, blanks and the reason, "#" starting a comment line; or else, with
// --draft, the draft's. record refuses, listing every one, an append that
// needs a reason neither gives, and a reason for a test it does not append,
// and then writes nothing. It prints each appended entry that needed a
// reason, as check --list-reasoned does.
//
// # Comparing
//
// diff compares the candidate result with the ledger, test by test, judging
// each by its last entry. A last entry is valid when its source is pinned
// at or after the chain's last base pin and its ceph_version is the
// candidate's. It prints, by test id:
//   - "<test>: no baseline entry" for a candidate test the ledger lacks;
//   - nothing for a retired test the candidate lacks, and "<test>: baseline
//     retired, candidate <outcome>" for one it runs;
//   - "<test>: baseline stale, re-record" for a test whose last entry is
//     not valid;
//   - nothing for an unstable test;
//   - "<test>: baseline <o1>, candidate <o2>" for any other test whose
//     outcome differs, "missing" standing for a test the candidate lacks.
//
// --known names a known-difference list, the tests whose outcomes are
// expected to differ: one regular expression per line, matched against the
// whole test id, with "#" starting a comment. A listed test that differs is
// skipped; one that agrees prints "<test>: known difference no longer
// differs", and an entry matching no compared or printed test prints
// "<pattern>: known difference matches no test", so a stale entry fails the
// run. A line starting with "~ " is an either-outcome entry, for a test
// whose outcome depends on timing: its test is skipped whether it differs
// or agrees, and only an entry that matches no test is reported. A test
// with no baseline entry counts as differing: either kind of entry skips
// it. The lines for a retired test the candidate runs and for a stale test
// are printed whatever the list says.
//
// --selection names the file of the pytest node ids the candidate's run
// selected, one a line, as hack/s3tests/run.sh leaves them in its .ids file,
// for a run of fewer tests than the ledger holds, such as the per-pull-request
// set. Each node id is read as the id pytest's junit report gives its test,
// classname "::" name: the parametrization, from the first "[", stays as it
// is, and of the rest, split at "::", the file path's slashes become dots,
// its .py goes, and the last part is the name, the others the classname. A
// ledger test the selection does not name and the candidate lacks is not
// compared, so it never fails the run; every other test is compared as above,
// so a selected test the candidate lacks is missing and one the ledger lacks
// has no baseline entry. --gate-only names the file of the gate-only tests
// the run left out, in the same form, as run.sh leaves them in its
// .gate-only.ids file; it may name none. Each other unselected test the
// candidate lacks, unless its last entry retires it, is listed after the
// differences, under the line "dropped from the pr set: <n> ledger tests
// ...", as "  <test>: baseline <outcome>", and counted in the summary diff
// prints when it fails: a test the selection no longer runs, such as one a new
// deselect line removes. A comparison of the gate set names it missing, or
// stale when its last entry is, and skips one whose last entry is current and
// unstable, which the next full record of the gate set retires. --summary
// FILE appends that list, when it is not empty, to FILE as markdown, the line
// as a heading and the tests in a code block no ledger text can close, for a
// CI job's summary to show a pull request's reviewer. A
// known-difference entry that matches no compared test is not reported when
// it matches an unselected test, unless a comparison of every test would skip
// that test too, its last entry retired, or current and unstable; one that
// matches neither still fails the run. diff refuses a selection that names no
// test, a line of either file that is not a node id, FILE.py::NAME, and
// --gate-only without --selection.
//
// diff refuses to compare when the candidate lacks the ledger's source key,
// release or ceph_version, or carries a source other than the chain's head
// or another release; --allow-meta-drift lets the source and release
// differ, and compares a last entry recorded under another Ceph version,
// never a key go missing. A selection change, such as a deselect list's,
// shows as a test with no entry or one missing, until a record enters or
// retires it; under --selection, a test the change removes is listed as
// dropped instead.
//
// # Pinning
//
// The s3-tests pin is data: hack/s3tests/PIN, a regular file of exactly 40
// lowercase hex digits and a newline, is the single place the pin is set, and
// run.sh and the recorder, parity, read it the same way, by that one rule.
// run.sh sets S3TESTS_COMMIT from it by one line, readonly
// S3TESTS_COMMIT=${S3TESTS_COMMIT:-${pin}}, so that S3TESTS_COMMIT in the
// environment still runs another commit for a local try, and run.sh
// --print-commit prints the commit with, on stderr, which of the two it came
// from; record --pin-file refuses a run at any commit but PIN's. check --base
// also flags slips in run.sh's text around the pin: that line missing or
// doubled, the name elsewhere than as the expansion ${S3TESTS_COMMIT} outside
// full-line comments, or a line continuation joining it from pieces. It reads
// the text and does not decide what bash runs.
//
// Every s3-tests pin stays on the history of jhoblitt/ceph's rgw-go-s3tests
// branch, ceph/ceph main with the suite fixes rgw-go needs: it must be the
// branch's tip or an ancestor of it, as a treeless fetch of the branch and of
// the pin, by its full id, and git merge-base --is-ancestor decide. A commit
// of a fork, of another branch or of none is refused as a likely mistake,
// naming which. Pins are checked as they come in, by pin and by check --base,
// so a pin a ledger already holds was checked then.
//
// pin moves the pin in --pin-file, hack/s3tests/PIN by default, to such a
// commit that a clone of Ceph proves keeps every outcome the ledgers hold,
// and appends the pin to both s3-tests ledgers, whose chains must end at the
// old pin; the branch is fetched from --ceph-repo, run.sh's S3TESTS_REPO by
// default. The proof is a regression test's check against honest mistakes: it
// keeps a pin bump that does more than add tests from carrying the old
// outcomes over. It is deliberately conservative, and anything it does not
// recognize needs a full record. A commit that descends from the pin is
// additive when the suite's directory diff only adds tests:
//   - no line changed or removed, and no file added or removed;
//   - no file changed but a test module, test_*.py, or pytest.ini;
//   - a test module gains only new statements, each wholly inserted between
//     existing ones: bare @pytest.mark.<name> decorators, each followed by
//     a comment at most, on a def test_<name>(<bare names>): on one line,
//     whose name the module did not hold, and its body, every line of it
//     indented; a mark must name a marker pytest.ini registers at the new
//     pin, and its arguments refuse, since Python evaluates them at import
//     and pytest judges them at collection, where a parametrize whose names
//     and values disagree, or that has none, fails the module; any other
//     top-level line, and any line added inside an existing statement,
//     refuses;
//   - an added line of a test module is printable ASCII ending at a line
//     feed, with no tab, no ";" and no backslash at its end, and runs across
//     no line in a string or continuation, so that the proof, which does
//     not parse Python, reads its lines as Python does: the only added lines
//     at column 0 are the fixed forms above, and every other one sits in the
//     body of the test it follows, run only when that test runs;
//   - pytest.ini gains only markers, and no added line of it holds a control
//     or format character, or a line or paragraph separator, each of which
//     pytest's ini reader takes for a line break.
//
// The proof cannot judge what it would have to run Python to see: what the
// added tests do when they run. pin then prints the node ids of the tests it
// adds to run.sh's test files, and --tests writes them to a file for a subset
// record. Any other commit is reworded when its first-parent line and the
// pin's, from where they met, are as many commits with the same tree at each.
// pin refuses anything else, naming each line that keeps it from being
// additive: that needs a full record, which appends a base pin with a reason.
//
// migrate converts baselines in the flat form ledgers replaced, a result
// file with release, ceph_version, recorded, runs and a source key in its
// meta, to ledgers in place: one base pin at its source and one entry per
// test. Every other command refuses that form, naming migrate.
//
// Much of what parity prints is text a pull request wrote, which a CI job's
// log and summary show, so every value from a file is printed escaped so that
// it reads back exactly: "\" as "\\", each control or format character as
// \uXXXX, each byte that is not UTF-8 as \xXX, every "##[", which GitHub
// Actions reads as a workflow command anywhere in a line, as "#\x23[", and a
// line that Actions would read as a "::" command after any blanks after a
// "\". The summary puts each value in a code span or a code block that it
// cannot close and that Markdown reads back as the value.
//
// diff exits 0 when it prints no difference, whatever dropped tests it lists,
// and 1 when it prints one;
// check and validate exit 1 when they print a problem; record, pin and
// migrate exit 1 when they cannot do their work. All exit 2 for a refused
// command line, and diff, check and validate for files they refuse to
// compare or cannot read. go run reports any nonzero status as 1, and make
// any failed recipe as 2, so a caller that tells the statuses apart runs a
// binary go build made.
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
		fmt.Fprintln(os.Stderr, parity.SafeText("parity: "+err.Error()))
	}
	return parity.ExitStatus(err)
}
