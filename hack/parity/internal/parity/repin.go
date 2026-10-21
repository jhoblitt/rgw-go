package parity

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RepinOptions name the s3-tests pin to move and the clone that proves the
// move keeps the ledgers' outcomes.
type RepinOptions struct {
	// Clone is a git clone of Ceph that holds the pinned commit and New.
	Clone string
	// New is the commit to pin, a full commit id.
	New string
	// RunSh is hack/s3tests/run.sh, which names the suite's directory, its
	// test files and its repository; pinSourceProblems checks its text.
	RunSh string
	// PinFile is hack/s3tests/PIN, which holds the pin.
	PinFile string
	// Ledgers are the s3-tests ledgers, each at the pin.
	Ledgers []string
	// CephRepo is the repository whose TrustedBranch New must be on;
	// run.sh's S3TESTS_REPO when empty.
	CephRepo string
}

// Repinned is a pin move Repin made.
type Repinned struct {
	Old, New string
	Admitted Admission
	// Proof describes, a line each, what the proof checked.
	Proof []string
	// Added holds the pytest node ids of the tests an additive move adds
	// to run.sh's test files, which a subset record then records.
	Added []string
}

var (
	reRunShDir   = regexp.MustCompile(`(?m)^readonly S3TESTS_DIR=(\S+)$`)
	reRunShFiles = regexp.MustCompile(`(?m)^readonly TEST_FILES=\(([^)]*)\)$`)
	reRunShRepo  = regexp.MustCompile(`(?m)^S3TESTS_REPO=\$\{S3TESTS_REPO:-(\S+)\}$`)
	reCommitID   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// runSh is what Repin and CheckBase read from run.sh.
type runSh struct {
	text      string
	dir       string
	testFiles []string
	// repo is S3TESTS_REPO's default, the repository the pins are on.
	repo string
}

func readRunSh(file string) (runSh, error) {
	data, err := readFile(file)
	if err != nil {
		return runSh{}, fmt.Errorf("reading run.sh: %w", err)
	}
	return parseRunSh(data, file)
}

// parseRunSh reads the suite's directory, its test files and the
// repository from run.sh's text; file names it in errors.
func parseRunSh(data []byte, file string) (runSh, error) {
	r := runSh{text: string(data)}
	dir := reRunShDir.FindSubmatch(data)
	files := reRunShFiles.FindSubmatch(data)
	if dir == nil || files == nil {
		return runSh{}, fmt.Errorf("%s sets no S3TESTS_DIR or TEST_FILES where the pin move expects them", file)
	}
	r.dir = string(dir[1])
	r.testFiles = strings.Fields(string(files[1]))
	if repo := reRunShRepo.FindSubmatch(data); repo != nil {
		r.repo = string(repo[1])
	}
	if !fs.ValidPath(r.dir) || strings.HasPrefix(r.dir, "-") {
		return runSh{}, fmt.Errorf("%s's S3TESTS_DIR %q is not a path inside the clone", file, r.dir)
	}
	return r, nil
}

// git runs git in the clone and returns its output. Every caller puts
// --end-of-options or -- before a value it did not write itself, and checks
// a commit id as one, so that no value is read as an option.
func git(ctx context.Context, clone string, args ...string) (string, error) {
	dir, err := filepath.Abs(clone)
	if err != nil {
		return "", fmt.Errorf("finding %s: %w", clone, err)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // see above: no argument reads as an option
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// isAncestor reports whether old is an ancestor of next in the clone.
func isAncestor(ctx context.Context, clone, old, next string) (bool, error) {
	_, err := git(ctx, clone, "merge-base", "--is-ancestor", "--end-of-options", old, next)
	if e, ok := errors.AsType[*exec.ExitError](err); ok && e.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// errNotKept marks a move whose proof failed: the ledgers' outcomes may not
// hold at the new commit, which a full record settles.
var errNotKept = errors.New("needs a full record, which appends a base pin with a reason")

// proofError is an additive proof that failed: why, and each line or file
// that keeps the move from being additive, kept apart so that a caller can
// show each on a line of its own.
type proofError struct {
	err      error
	problems []string
}

func (e *proofError) Error() string { return e.err.Error() + ":\n" + strings.Join(e.problems, "\n") }

func (e *proofError) Unwrap() error { return e.err }

// Repin moves the s3-tests pin in opts.PinFile to opts.New when
// pinSourceProblems finds nothing in run.sh, opts.New is on the history of
// the s3-tests repository's TrustedBranch and the clone proves the move
// keeps every outcome the ledgers hold, and appends the pin to each ledger.
// The proof is a regression test's check against honest mistakes, and
// refuses what it does not recognize.
//
// Two proofs are tried. A New that descends from the pin is additive when
// the suite's directory diff from the pin to New only adds tests: no line
// changed or removed, no file added or removed, and no file changed but a
// test module or pytest.ini; a test module gains only new statements, each
// wholly inserted between existing ones and each @pytest.mark decorators on
// a def test_<name>(...) with its indented body, the name new to the module
// and the parameters bare names; and pytest.ini gains only markers. Any
// other New is reworded when the first-parent commits from where the two
// lines met to the pin and to New are as many, with the same tree at each.
// Nothing is written unless one proof holds.
func Repin(ctx context.Context, opts RepinOptions) (Repinned, error) {
	if !reCommitID.MatchString(opts.New) {
		return Repinned{}, fmt.Errorf("%s is not a full commit id", opts.New)
	}
	r, err := readRunSh(opts.RunSh)
	if err != nil {
		return Repinned{}, err
	}
	if problems := pinSourceProblems(r.text, opts.RunSh); len(problems) > 0 {
		return Repinned{}, fmt.Errorf("%s sets S3TESTS_COMMIT somewhere other than its one line reading %s:\n%s",
			opts.RunSh, opts.PinFile, strings.Join(problems, "\n"))
	}
	old, err := readPinFile(opts.PinFile)
	if err != nil {
		return Repinned{}, err
	}
	res := Repinned{Old: old, New: opts.New}
	if res.Old == res.New {
		return Repinned{}, fmt.Errorf("the pin is already %s", res.New)
	}

	ledgers, err := pinnedLedgers(opts.Ledgers, res.Old)
	if err != nil {
		return Repinned{}, err
	}
	repo := cmp.Or(opts.CephRepo, r.repo)
	if repo == "" {
		return Repinned{}, fmt.Errorf("no repository to check %s's branch in: %s sets no S3TESTS_REPO", res.New, opts.RunSh)
	}
	why, err := untrustedPins(ctx, repo, []string{res.New})
	if err != nil {
		return Repinned{}, err
	}
	if w, ok := why[res.New]; ok {
		return Repinned{}, errors.New(untrustedProblem(res.New, repo, w))
	}
	if res.Admitted, res.Proof, res.Added, err = provePin(ctx, opts.Clone, res.Old, res.New, r); err != nil {
		return Repinned{}, err
	}

	files := map[string][]byte{opts.PinFile: []byte(res.New + "\n")}
	order := []string{opts.PinFile}
	for i, l := range ledgers {
		if perr := AddPin(l, res.New, res.Admitted); perr != nil {
			return Repinned{}, fmt.Errorf("%s: %w", opts.Ledgers[i], perr)
		}
		data, merr := MarshalLedger(l)
		if merr != nil {
			return Repinned{}, merr
		}
		files[opts.Ledgers[i]] = data
		order = append(order, opts.Ledgers[i])
	}
	return res, writeAll(order, files)
}

// pinnedLedgers reads the s3-tests ledgers, each valid and its chain ending
// at pin.
func pinnedLedgers(files []string, pin string) ([]*Ledger, error) {
	ledgers := make([]*Ledger, 0, len(files))
	for _, file := range files {
		l, err := readLedger(file)
		if err != nil {
			return nil, err
		}
		if problems := Validate(l); len(problems) > 0 {
			return nil, fmt.Errorf("%s is not a valid ledger:\n%s", file, strings.Join(problems, "\n"))
		}
		switch {
		case l.SourceKey != "s3tests_commit":
			return nil, fmt.Errorf("%s is a %s ledger, not an s3-tests one", file, l.SourceKey)
		case l.head() != pin:
			return nil, fmt.Errorf("%s's chain ends at %s, not at the pin %s", file, l.head(), pin)
		}
		ledgers = append(ledgers, l)
	}
	return ledgers, nil
}

// writeAll writes each file in order, and puts back the ones it wrote when
// one fails, so that the pin moves everywhere or nowhere.
func writeAll(order []string, files map[string][]byte) error {
	written := map[string][]byte{}
	for _, file := range order {
		old, err := readFile(file)
		if err == nil {
			err = writeAtomic(file, files[file])
		}
		if err != nil {
			var undo []error
			for f, data := range written {
				undo = append(undo, writeAtomic(f, data))
			}
			return errors.Join(append([]error{fmt.Errorf("writing %s: %w; nothing moved", file, err)}, undo...)...)
		}
		written[file] = old
	}
	return nil
}

// provePin proves that moving the pin from old to next keeps every outcome
// a ledger holds, as Repin describes, and returns how next is admitted,
// what the proof checked and the node ids of the tests an additive move
// adds to run.sh's test files.
func provePin(ctx context.Context, clone, old, next string, r runSh) (admitted Admission, proof, added []string, err error) {
	for _, c := range []string{old, next} {
		if !reCommitID.MatchString(c) {
			return "", nil, nil, fmt.Errorf("%s is not a full commit id", c)
		}
	}
	for _, c := range []string{old, next} {
		if _, gerr := git(ctx, clone, "cat-file", "-e", "--end-of-options", c+"^{commit}"); gerr != nil {
			return "", nil, nil, fmt.Errorf("%s does not hold the commit %s: %w", clone, c, gerr)
		}
	}
	descends, err := isAncestor(ctx, clone, old, next)
	switch {
	case err != nil:
		return "", nil, nil, err
	case descends:
		proof, added, err = proveAdditive(ctx, clone, old, next, r)
		return AdmitAdditive, proof, added, err
	}
	proof, err = proveReworded(ctx, clone, old, next)
	return AdmitReworded, proof, nil, err
}

// proveReworded checks that old's and next's first-parent lines, from where
// they met, are as many commits with the same tree at each.
func proveReworded(ctx context.Context, clone, old, next string) ([]string, error) {
	out, err := git(ctx, clone, "merge-base", "--end-of-options", old, next)
	if err != nil {
		return nil, fmt.Errorf("%s and %s share no history, so the move %w: %w", old, next, errNotKept, err)
	}
	base := strings.TrimSpace(out)
	line := func(tip string) ([]string, error) {
		revs, lerr := git(ctx, clone, "rev-list", "--reverse", "--first-parent", "--end-of-options", base+".."+tip)
		return strings.Fields(revs), lerr
	}
	olds, err := line(old)
	if err != nil {
		return nil, err
	}
	news, err := line(next)
	if err != nil {
		return nil, err
	}
	if len(olds) != len(news) {
		return nil, fmt.Errorf("%d commits lead from %s to the pin but %d to %s: neither descends from the other, so the move %w",
			len(olds), base, len(news), next, errNotKept)
	}
	proof := make([]string, 0, len(olds))
	for i := range olds {
		trees := make([]string, 2)
		for j, c := range []string{olds[i], news[i]} {
			out, err := git(ctx, clone, "rev-parse", "--verify", "--quiet", "--end-of-options", c+"^{tree}")
			if err != nil {
				return nil, err
			}
			trees[j] = strings.TrimSpace(out)
		}
		if trees[0] != trees[1] {
			return nil, fmt.Errorf("commit %d after %s: %s has the tree %s but %s has %s, so the move %w",
				i+1, base, olds[i], trees[0], news[i], trees[1], errNotKept)
		}
		proof = append(proof, fmt.Sprintf("%s -> %s: tree %s", olds[i], news[i], trees[1]))
	}
	return proof, nil
}

// changed is a file the suite's directory diff touches.
type changed struct {
	status string
	added  int
	binary bool
}

// isTestModule reports whether a file of the suite's directory is a pytest
// test module, the only kind of Python file an additive move may change.
func isTestModule(file string) bool {
	base := path.Base(file)
	return strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")
}

// proveAdditive checks that the suite's directory diff from old to next
// only adds tests, as Repin describes, and returns the test node ids it
// adds to run.sh's test files.
func proveAdditive(ctx context.Context, clone, old, next string, r runSh) (proof, added []string, err error) {
	files, err := diffFiles(ctx, clone, old, next, r.dir)
	if err != nil {
		return nil, nil, err
	}
	markers, err := markersAt(ctx, clone, next, r.dir+"/pytest.ini")
	if err != nil {
		return nil, nil, err
	}
	var problems []string
	for _, file := range slices.Sorted(maps.Keys(files)) {
		c := files[file]
		switch {
		case c.status != "M":
			problems = append(problems, fmt.Sprintf("%s: %s; only a test module or pytest.ini may change", file, statusName(c.status)))
			continue
		case c.binary:
			problems = append(problems, file+": a binary change")
			continue
		case file != "pytest.ini" && !isTestModule(file):
			problems = append(problems, file+": changed; only a test module or pytest.ini may change")
			continue
		}
		text, inserted, removed, err := patchLines(ctx, clone, old, next, r.dir+"/"+file)
		if err != nil {
			return nil, nil, err
		}
		problems = append(problems, prefixed(file, removed)...)
		if file == "pytest.ini" {
			problems = append(problems, prefixed(file, additiveIni(text, inserted))...)
			proof = append(proof, fmt.Sprintf("%s: +%d markers", file, c.added))
			continue
		}
		p, names := additiveTestModule(text, inserted, markers)
		problems = append(problems, prefixed(file, p)...)
		if slices.Contains(r.testFiles, file) {
			added = append(added, testNodeIDs(file, names)...)
		}
		proof = append(proof, fmt.Sprintf("%s: +%d lines, %d tests", file, c.added, len(names)))
	}
	if len(problems) > 0 {
		return nil, nil, &proofError{err: fmt.Errorf("%s does not only add tests over %s, so the move %w", next, old, errNotKept), problems: problems}
	}
	if len(proof) == 0 {
		proof = append(proof, r.dir+": unchanged")
	}
	return proof, added, nil
}

// markersAt returns the markers the pytest.ini at file registers at commit,
// none when commit holds no such file.
func markersAt(ctx context.Context, clone, commit, file string) (map[string]bool, error) {
	out, err := git(ctx, clone, "ls-tree", "--end-of-options", commit, "--", file)
	if err != nil || out == "" {
		return map[string]bool{}, err
	}
	text, err := git(ctx, clone, "cat-file", "blob", "--end-of-options", commit+":"+file)
	if err != nil {
		return nil, err
	}
	return iniMarkers(text), nil
}

// testNodeIDs returns pytest's node ids for the tests a module adds.
func testNodeIDs(module string, names []string) []string {
	ids := make([]string, 0, len(names))
	for _, n := range names {
		ids = append(ids, module+"::"+n)
	}
	return ids
}

func prefixed(file string, problems []string) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, file+": "+p)
	}
	return out
}

func statusName(status string) string {
	switch status {
	case "A":
		return "added"
	case "D":
		return "removed"
	case "T":
		return "its type changed"
	}
	return "status " + status
}

// diffFiles returns each file under dir that differs between old and next,
// by its path under dir.
func diffFiles(ctx context.Context, clone, old, next, dir string) (map[string]changed, error) {
	out, err := git(ctx, clone, "diff-tree", "-r", "--no-renames", "-z", "--name-status", "--end-of-options", old, next, "--", dir)
	if err != nil {
		return nil, err
	}
	files := map[string]changed{}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		files[strings.TrimPrefix(fields[i+1], dir+"/")] = changed{status: fields[i]}
	}
	out, err = git(ctx, clone, "diff-tree", "-r", "--no-renames", "-z", "--numstat", "--end-of-options", old, next, "--", dir)
	if err != nil {
		return nil, err
	}
	for rec := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		file := strings.TrimPrefix(parts[2], dir+"/")
		c := files[file]
		if parts[0] == "-" {
			c.binary = true
		} else if c.added, err = strconv.Atoi(parts[0]); err != nil {
			return nil, fmt.Errorf("git's numstat for %s: %w", file, err)
		}
		files[file] = c
	}
	return files, nil
}

var reHunk = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// patchLines returns next's text of file, the lines the diff from old
// inserts, by index into that text's lines, and each line it removes or
// changes, named by its line in old.
func patchLines(ctx context.Context, clone, old, next, file string) (text string, inserted map[int]bool, removed []string, err error) {
	if text, err = git(ctx, clone, "cat-file", "blob", "--end-of-options", next+":"+file); err != nil {
		return "", nil, nil, err
	}
	patch, err := git(ctx, clone, "diff-tree", "-p", "-U0", "--no-renames", "--end-of-options", old, next, "--", file)
	if err != nil {
		return "", nil, nil, err
	}
	inserted = map[int]bool{}
	oldLine, newLine, inHunk := 0, 0, false
	for line := range strings.SplitSeq(patch, "\n") {
		if m := reHunk.FindStringSubmatch(line); m != nil {
			if oldLine, err = strconv.Atoi(m[1]); err == nil {
				newLine, err = strconv.Atoi(m[2])
			}
			if err != nil {
				return "", nil, nil, fmt.Errorf("git's hunk %q for %s: %w", line, file, err)
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '-':
			removed = append(removed, fmt.Sprintf("line %d of %s is changed or removed: %s", oldLine, old, line[1:]))
			oldLine++
		case '+':
			inserted[newLine-1] = true
			newLine++
		}
	}
	return text, inserted, removed, nil
}

// fetchCommits makes a treeless clone of repo, an https:// or file:// URL,
// in a temporary directory that holds commits, full commit ids, and their
// history, whose trees and blobs git fetches as a proof reads them, and
// returns its path; the caller removes it.
func fetchCommits(ctx context.Context, repo string, commits ...string) (string, error) {
	for _, c := range commits {
		if !reCommitID.MatchString(c) {
			return "", fmt.Errorf("%q is not a full commit id", c)
		}
	}
	return fetchTreeless(ctx, repo, commits...)
}

// fetchTreeless makes a treeless clone of repo, an https:// or file:// URL,
// in a temporary directory, fetches what refspecs name, full commit ids or
// refspecs this package writes, and returns its path; the caller removes
// it.
func fetchTreeless(ctx context.Context, repo string, refspecs ...string) (string, error) {
	if !strings.HasPrefix(repo, "https://") && !strings.HasPrefix(repo, "file://") {
		return "", fmt.Errorf("the repository %q is not an https:// or file:// URL", repo)
	}
	clone, err := os.MkdirTemp("", "parity-ceph-*")
	if err != nil {
		return "", fmt.Errorf("making the clone: %w", err)
	}
	// Every fetch starts git maintenance run --auto --detach, whose detached
	// process works in objects/ after the fetch returns, while the caller
	// removes the clone; the clone's own config turns it off for every git
	// run there, the lazy fetches of a later read among them.
	for _, args := range [][]string{
		{"init", "-q", "--bare"},
		{"config", "maintenance.auto", "false"},
		{"config", "gc.auto", "0"},
		{"remote", "add", "--", "origin", repo},
		{"config", "remote.origin.promisor", "true"},
		{"config", "remote.origin.partialclonefilter", "tree:0"},
		append([]string{"fetch", "-q", "--no-tags", "--filter=tree:0", "--end-of-options", "origin"}, refspecs...),
	} {
		if _, gerr := git(ctx, clone, args...); gerr != nil {
			return "", errors.Join(fmt.Errorf("fetching %s from %s: %w", strings.Join(refspecs, " "), repo, gerr), os.RemoveAll(clone))
		}
	}
	return clone, nil
}
