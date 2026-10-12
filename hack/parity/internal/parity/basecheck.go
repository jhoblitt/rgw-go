package parity

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// DefaultLedgerPaths are the directories check --base reads ledgers from
// when given none. In CI the checker is built from the base branch, so
// these come from the base, never from the pull request it judges.
var DefaultLedgerPaths = []string{"test/s3tests/baseline", "test/admin/baseline"}

// BaseCheck names what CheckBase compares: the ledgers a pull request
// leaves, in a repository's working tree or at a commit, with the versions
// a git ref holds, the base branch's.
type BaseCheck struct {
	// Repo is the repository; Paths and RunSh are relative to it.
	Repo string
	// Base is the commit the ledgers must only append to.
	Base string
	// Head, when set, is the commit whose ledgers are judged, read from git
	// as data; the working tree is not read. Otherwise the working tree's
	// are judged.
	Head string
	// Paths are ledger files, or directories whose .json files are ledgers;
	// DefaultLedgerPaths when empty.
	Paths []string
	// RunSh is hack/s3tests/run.sh, read from Base, whose S3TESTS_DIR and
	// TEST_FILES an s3-tests pin's proof reads, and whose S3TESTS_REPO it
	// fetches from. pinSourceProblems checks the judged side's.
	RunSh string
	// PinFile is hack/s3tests/PIN, which holds the s3-tests pin.
	PinFile string
	// Clone, when set, is a clone of Ceph that holds every pin to prove.
	// Otherwise the proof fetches the pins without trees from CephRepo, or
	// from the base's S3TESTS_REPO when CephRepo is empty. Whether a pin is
	// on TrustedBranch is always asked of that repository, never of Clone.
	Clone, CephRepo string
}

// BaseReport is what CheckBase found: the problems, one line each, and a
// markdown summary for the reviewer of each changed ledger's appended pins
// and of its appended entries that needed a reason, every value a pull
// request wrote in a code span or a code block.
type BaseReport struct {
	Problems []string
	Summary  string
}

// pinProof is an appended pin a proof must admit as the ledger says.
type pinProof struct {
	old, next string
	admitted  Admission
}

// ledgerChange is a ledger the working tree changes.
type ledgerChange struct {
	file      string
	old, next *Ledger
	flat      bool
	pins      []int // indices into next.Pins of the appended pins
}

// reLedgerName is what a ledger's path may hold.
var reLedgerName = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// CheckBase checks every ledger under c.Paths on the judged side, c.Head
// or else the working tree, against c.Base: a ledger the base holds is
// never removed, and its judged version only appends to it as CheckAppend
// checks; a new ledger is valid; a ledger the base holds
// in the flat form ledgers replaced is checked against its conversion.
// Neither may be a draft, a file other than a regular one, or a name other
// than letters, digits, ".", "_", "/" and "-". A ledger the pull request
// adds or changes outside c.Paths is a problem too, so that none escapes
// the check. Every s3-tests pin the judged side brings in, in a ledger or
// in the PIN file, must be on TrustedBranch's history, as checkTrust
// checks. Each appended additive or
// reworded pin is proven again, as
// parity pin proves it, with the base's run.sh, and must be admitted as
// the proof admits it. A base pin has no proof, since its record replaced
// every outcome, so it must carry a reason, and the summary lists each
// outcome it re-stamps. A ledger whose file the judged side leaves
// unchanged is not read. With c.Head, every file is read from git as data,
// so nothing the head holds is checked out or run.
func CheckBase(ctx context.Context, c BaseCheck) (BaseReport, error) {
	if len(c.Paths) == 0 {
		c.Paths = DefaultLedgerPaths
	}
	for _, p := range append(slices.Clone(c.Paths), c.RunSh, c.PinFile) {
		if !fs.ValidPath(p) || strings.HasPrefix(p, "-") {
			return BaseReport{}, fmt.Errorf("%s is not a path inside the repository", p)
		}
	}
	base, err := resolveCommit(ctx, c.Repo, "base", c.Base)
	if err != nil {
		return BaseReport{}, err
	}
	c.Base = base
	if c.Head != "" {
		if c.Head, err = resolveCommit(ctx, c.Repo, "head", c.Head); err != nil {
			return BaseReport{}, err
		}
	}

	files, inBase, err := ledgerFiles(ctx, c)
	if err != nil {
		return BaseReport{}, err
	}
	var rep BaseReport
	var changes []ledgerChange
	for _, file := range files {
		ch, problems, rerr := readChange(ctx, c, file, inBase[file])
		if rerr != nil {
			return BaseReport{}, rerr
		}
		rep.Problems = append(rep.Problems, problems...)
		if ch != nil {
			changes = append(changes, *ch)
		}
	}
	outside, err := ledgersOutside(ctx, c)
	if err != nil {
		return BaseReport{}, err
	}
	rep.Problems = append(rep.Problems, outside...)
	untrusted, err := checkTrust(ctx, c, changes)
	if err != nil {
		return BaseReport{}, err
	}
	rep.Problems = append(rep.Problems, untrusted...)

	proofs := map[pinProof][]string{}
	for _, ch := range changes {
		if ch.next.SourceKey != "s3tests_commit" {
			continue
		}
		for _, i := range ch.pins {
			if p := ch.next.Pins[i]; p.Admitted != AdmitBase {
				proofs[pinProof{old: ch.next.Pins[i-1].Source, next: p.Source, admitted: p.Admitted}] = nil
			}
		}
	}
	failed, err := provePins(ctx, c, proofs)
	if err != nil {
		return BaseReport{}, err
	}

	var sum strings.Builder
	for _, ch := range changes {
		fmt.Fprintf(&sum, "### %s\n\n", codeSpan(ch.file))
		if ch.flat {
			sum.WriteString("The base holds this ledger in the flat form; it is checked against that form's conversion.\n\n")
		}
		for _, i := range ch.pins {
			p := ch.next.Pins[i]
			src, adm := codeSpan(p.Source), codeSpan(string(p.Admitted))
			if p.Admitted == AdmitBase {
				writeBasePin(&sum, ch.old, ch.next, i)
				continue
			}
			pp := pinProof{old: ch.next.Pins[i-1].Source, next: p.Source, admitted: p.Admitted}
			if ch.next.SourceKey != "s3tests_commit" {
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: pin %d (%s, %s): only an s3-tests pin can be proven %s",
					ch.file, i+1, p.Source, p.Admitted, p.Admitted))
				fmt.Fprintf(&sum, "- pin %s, admitted %s: **not proven**\n", src, adm)
				continue
			}
			if why := failed[pp]; len(why) > 0 {
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s: pin %d (%s, %s): %s", ch.file, i+1, p.Source, p.Admitted, why[0]))
				for _, line := range why[1:] {
					rep.Problems = append(rep.Problems, fmt.Sprintf("%s: pin %d: %s", ch.file, i+1, line))
				}
				fmt.Fprintf(&sum, "- pin %s, admitted %s: **not proven**\n", src, adm)
				continue
			}
			fmt.Fprintf(&sum, "- pin %s, admitted %s: proven over %s\n", src, adm, codeSpan(pp.old))
		}
		reasoned := Reasoned(ch.old, ch.next)
		fmt.Fprintf(&sum, "\n%d appended entries change an outcome or an unstable flag, or retire a test:\n\n%s\n",
			len(reasoned), codeBlock(reasoned))
	}
	rep.Summary = sum.String()
	return rep, nil
}

// writeBasePin writes the summary of next's pin i, a base pin after the
// first that next appends to old's chain, nil for a ledger next starts: its
// reason, and every outcome it re-stamps, so that a reviewer sees what it
// carries forward unproven as well as what it changes.
func writeBasePin(w io.Writer, old, next *Ledger, i int) {
	p := next.Pins[i]
	fmt.Fprintf(w, "- pin %s, admitted %s", codeSpan(p.Source), codeSpan(string(p.Admitted)))
	if p.Reason == "" {
		fmt.Fprint(w, ": recorded whole, unproven, **with no reason**\n")
	} else {
		fmt.Fprintf(w, ", for the reason %s: recorded whole, unproven\n", codeSpan(p.Reason))
	}
	var lines []string
	carried := 0
	for _, id := range slices.Sorted(maps.Keys(next.Tests)) {
		entries, from := next.Tests[id], 0
		if old != nil {
			from = len(old.Tests[id])
		}
		for j := from; j < len(entries); j++ {
			switch e := entries[j]; {
			case e.Source != p.Source:
			case j == 0:
				lines = append(lines, fmt.Sprintf("%s: %s, new", id, e.state()))
			case entries[j-1].state() == e.state():
				carried++
				lines = append(lines, fmt.Sprintf("%s: %s, unchanged", id, e.state()))
			default:
				lines = append(lines, fmt.Sprintf("%s: %s -> %s", id, entries[j-1].state(), e.state()))
			}
		}
	}
	fmt.Fprintf(w, "\n%d outcomes re-stamped at %s, **%d carried forward unchanged**:\n\n%s\n",
		len(lines), codeSpan(p.Source), carried, codeBlock(lines))
}

// resolveCommit returns the full id of the commit ref, the role named, names
// in the repository, refusing a ref git could read as an option.
func resolveCommit(ctx context.Context, repo, role, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("the %s %q is not a commit", role, ref)
	}
	out, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil || !reCommitID.MatchString(strings.TrimSpace(out)) {
		return "", fmt.Errorf("the %s %q is not a commit", role, ref)
	}
	return strings.TrimSpace(out), nil
}

// underPaths reports whether file is one of paths or under one of them.
func underPaths(file string, paths []string) bool {
	for _, p := range paths {
		if file == p || strings.HasPrefix(file, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// treeFiles returns the .json files under paths at commit.
func treeFiles(ctx context.Context, repo, commit string, paths []string) ([]string, error) {
	args := append([]string{"ls-tree", "-r", "-z", "--name-only", "--end-of-options", commit, "--"}, paths...)
	out, err := git(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for file := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if strings.HasSuffix(file, ".json") {
			files = append(files, file)
		}
	}
	return files, nil
}

// ledgerFiles returns the ledger files under c.Paths in the base or on the
// judged side, c.Head or the working tree, sorted, and which of them the
// base holds. A symbolic link named like a ledger is listed too, for
// readChange to refuse.
func ledgerFiles(ctx context.Context, c BaseCheck) (files []string, inBase map[string]bool, err error) {
	based, err := treeFiles(ctx, c.Repo, c.Base, c.Paths)
	if err != nil {
		return nil, nil, err
	}
	all := map[string]bool{}
	inBase = map[string]bool{}
	for _, file := range based {
		all[file], inBase[file] = true, true
	}
	if c.Head != "" {
		headed, err := treeFiles(ctx, c.Repo, c.Head, c.Paths)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range headed {
			all[file] = true
		}
		return slices.Sorted(maps.Keys(all)), inBase, nil
	}
	for _, p := range c.Paths {
		err := filepath.WalkDir(filepath.Join(c.Repo, p), func(path string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil
			case err != nil:
				return err
			case d.IsDir() || !strings.HasSuffix(path, ".json"):
				return nil
			}
			rel, err := filepath.Rel(c.Repo, path)
			all[filepath.ToSlash(rel)] = true
			return err
		})
		if err != nil {
			return nil, nil, fmt.Errorf("listing the ledgers under %s: %w", p, err)
		}
	}
	return slices.Sorted(maps.Keys(all)), inBase, nil
}

// errRemoved and errNotRegular are what readSide finds in place of a
// ledger file.
var (
	errRemoved    = errors.New("removed; a ledger is never removed")
	errNotRegular = errors.New("not a regular file; a ledger is one")
)

// readSide reads a ledger file on the judged side: c.Head's blob, read from
// git, or the working tree's file.
func readSide(ctx context.Context, c BaseCheck, file string) ([]byte, error) {
	if c.Head != "" {
		out, err := git(ctx, c.Repo, "ls-tree", "-z", "--end-of-options", c.Head, "--", file)
		switch {
		case err != nil:
			return nil, err
		case out == "":
			return nil, errRemoved
		case !strings.HasPrefix(out, "100644 blob ") && !strings.HasPrefix(out, "100755 blob "):
			return nil, errNotRegular
		}
		blob, err := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Head+":"+file)
		return []byte(blob), err
	}
	full := filepath.Join(c.Repo, file)
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errRemoved
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", file, err)
	case !info.Mode().IsRegular():
		return nil, errNotRegular
	}
	data, err := os.ReadFile(full) //nolint:gosec // a regular file under the paths the check covers
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return data, nil
}

// readChange reads a ledger file on the judged side and, when the base
// holds it, the base's version, and returns the change with its problems;
// a nil change for a file the judged side leaves as it was, or one it
// cannot read as a ledger.
func readChange(ctx context.Context, c BaseCheck, file string, inBase bool) (*ledgerChange, []string, error) {
	if !reLedgerName.MatchString(file) {
		return nil, []string{fmt.Sprintf("%q: a ledger's path holds only letters, digits, \".\", \"_\", \"/\" and \"-\"", file)}, nil
	}
	data, err := readSide(ctx, c, file)
	switch {
	case errors.Is(err, errRemoved), errors.Is(err, errNotRegular):
		return nil, []string{file + ": " + err.Error()}, nil
	case err != nil:
		return nil, nil, err
	}
	var baseData []byte
	if inBase {
		out, gerr := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Base+":"+file)
		if gerr != nil {
			return nil, nil, gerr
		}
		baseData = []byte(out)
		if bytes.Equal(data, baseData) {
			return nil, nil, nil
		}
	}
	next, err := ParseLedger(data)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", file, err)}, nil
	}
	ch := &ledgerChange{file: file, next: next}
	if inBase {
		if ch.old, ch.flat, err = parseBase(baseData); err != nil {
			return nil, []string{fmt.Sprintf("%s: the base's version: %v", file, err)}, nil
		}
	}
	var problems []string
	if ch.old == nil {
		problems = Validate(next)
	} else {
		problems = CheckAppend(ch.old, next, false)
	}
	from := 1
	if ch.old != nil {
		from = max(len(ch.old.Pins), 1)
	}
	for i := from; i < len(next.Pins); i++ {
		ch.pins = append(ch.pins, i)
	}
	return ch, prefixed(file, problems), nil
}

// ledgersOutside returns a problem for each JSON file outside c.Paths that
// the judged side adds or changes against the base and that holds a ledger
// or a flat baseline: one there would escape the check.
func ledgersOutside(ctx context.Context, c BaseCheck) ([]string, error) {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.json"}
	if c.Head != "" {
		args = []string{"diff-tree", "-r", "-z", "--no-renames", "--name-only", "--diff-filter=d", "--end-of-options", c.Base, c.Head, "--", "*.json"}
	}
	out, err := git(ctx, c.Repo, args...)
	if err != nil {
		return nil, err
	}
	var problems []string
	for file := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if file == "" || underPaths(file, c.Paths) {
			continue
		}
		var data []byte
		if c.Head != "" {
			blob, gerr := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Head+":"+file)
			if gerr != nil {
				continue
			}
			data = []byte(blob)
		} else {
			data, err = os.ReadFile(filepath.Join(c.Repo, file)) //nolint:gosec // a JSON file the repository holds
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", file, err)
			}
		}
		if !looksLikeLedger(data) {
			continue
		}
		if baseData, gerr := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Base+":"+file); gerr == nil && baseData == string(data) {
			continue
		}
		problems = append(problems, file+": a ledger outside the paths the check covers")
	}
	return problems, nil
}

// looksLikeLedger reports whether data is a JSON object with a ledger's
// members, or a flat baseline's, matching their names in any case, as
// encoding/json would.
func looksLikeLedger(data []byte) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) != nil {
		return false
	}
	has := map[string]bool{}
	for k := range probe {
		has[strings.ToLower(k)] = true
	}
	return (has["pins"] && has["tests"]) || (has["outcomes"] && has["meta"])
}

// parseBase reads the base's version of a ledger through the strict
// decoder, converting the flat form ledgers replaced, and reports whether
// it was flat. The base's ledger need not be in the form this build
// writes, so that a change to that form does not refuse every ledger.
func parseBase(data []byte) (*Ledger, bool, error) {
	l, err := decodeLedger(data)
	if !errors.Is(err, ErrFlatBaseline) {
		return l, false, err
	}
	var res Result
	if uerr := decodeStrict(data, &res); uerr != nil {
		return nil, true, fmt.Errorf("parsing the flat form: %w", uerr)
	}
	l, err = Migrate(res)
	return l, true, err
}

// provePins proves each appended pin with the base's run.sh, and returns,
// for each that fails, why: a line, then a line for each line or file of
// the suite that keeps the move from being additive. A pin that is not a
// pair of full commit ids fails before any git command sees it.
func provePins(ctx context.Context, c BaseCheck, proofs map[pinProof][]string) (_ map[pinProof][]string, err error) {
	commits := map[string]bool{}
	for p := range proofs {
		for _, id := range []string{p.old, p.next} {
			if !reCommitID.MatchString(id) {
				proofs[p] = []string{fmt.Sprintf("the proof fails: %s is not a full commit id", id)}
			}
		}
		if len(proofs[p]) == 0 {
			commits[p.old], commits[p.next] = true, true
		}
	}
	if len(commits) == 0 {
		return proofs, nil
	}
	text, err := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Base+":"+c.RunSh)
	if err != nil {
		return nil, fmt.Errorf("reading the base's %s: %w", c.RunSh, err)
	}
	r, err := parseRunSh([]byte(text), "the base's "+c.RunSh)
	if err != nil {
		return nil, err
	}
	clone := c.Clone
	if clone == "" {
		repo := cmp.Or(c.CephRepo, r.repo)
		if repo == "" {
			return nil, errors.New("no Ceph repository to prove the pins in: the base's run.sh sets no S3TESTS_REPO")
		}
		if clone, err = fetchCommits(ctx, repo, slices.Sorted(maps.Keys(commits))...); err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, os.RemoveAll(clone)) }()
	}
	for p, why := range proofs {
		if len(why) > 0 {
			continue
		}
		admitted, _, _, perr := provePin(ctx, clone, p.old, p.next, r)
		var pe *proofError
		switch {
		case errors.As(perr, &pe):
			proofs[p] = append([]string{"the proof fails: " + pe.err.Error()}, pe.problems...)
		case perr != nil:
			proofs[p] = []string{"the proof fails: " + perr.Error()}
		case admitted != p.admitted:
			proofs[p] = []string{fmt.Sprintf("the proof admits it %s", admitted)}
		}
	}
	return proofs, nil
}
