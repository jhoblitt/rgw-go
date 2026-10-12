package parity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// TrustedBranch is the branch of the s3-tests repository whose history
// every s3-tests pin must be on: jhoblitt/ceph's rgw-go-s3tests, ceph/ceph
// main with the suite fixes rgw-go needs. A pin off it is most likely a
// mistake: a commit of a fork or of another branch.
const TrustedBranch = "rgw-go-s3tests"

// untrustedPins returns why each of pins is not the tip of repo's
// TrustedBranch or an ancestor of it, by pin; a pin the result does not
// hold is on the branch. It fetches the branch, then each pin by its id,
// without trees, into a temporary repository it removes, and asks git
// whether the pin is an ancestor of the tip. A pin that is not a full
// commit id never reaches git.
func untrustedPins(ctx context.Context, repo string, pins []string) (_ map[string]string, err error) {
	why := map[string]string{}
	ids := map[string]bool{}
	for _, p := range pins {
		if reCommitID.MatchString(p) {
			ids[p] = true
		} else {
			why[p] = "it is not a full commit id"
		}
	}
	if len(ids) == 0 {
		return why, nil
	}
	ref := "refs/heads/" + TrustedBranch
	clone, err := fetchTreeless(ctx, repo, "+"+ref+":"+ref)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(clone)) }()
	tip, err := git(ctx, clone, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	tip = strings.TrimSpace(tip)
	for _, p := range slices.Sorted(maps.Keys(ids)) {
		if _, ferr := git(ctx, clone, "fetch", "-q", "--no-tags", "--filter=tree:0", "--end-of-options", "origin", p); ferr != nil {
			why[p] = repo + " does not serve it: a commit of a fork, or of no branch at all"
			continue
		}
		on, aerr := isAncestor(ctx, clone, p, tip)
		if aerr != nil {
			return nil, aerr
		}
		if !on {
			why[p] = fmt.Sprintf("it is not an ancestor of the branch's tip %s: a commit of another branch, of a fork, or of none", tip)
		}
	}
	return why, nil
}

// untrustedProblem is the problem a pin named where makes when it is not on
// repo's TrustedBranch for the reason why.
func untrustedProblem(where, repo, why string) string {
	return fmt.Sprintf("%s: not on the history of %s's %s, where an s3-tests pin must be: %s", where, repo, TrustedBranch, why)
}

// trustedPin is a pin the judged side brings in, and where it names it.
type trustedPin struct {
	where, pin string
}

// checkTrust returns a problem for each s3-tests pin the judged side brings
// in that is not on the s3-tests repository's TrustedBranch: each pin an
// s3-tests ledger appends, the first of a ledger it starts included, and
// the PIN file's when it differs from the base's. A pin the base already
// names was checked when it came in. The branch is fetched from
// c.CephRepo, or else the base's S3TESTS_REPO, never from c.Clone. It
// also returns a problem for a PIN file parsePin refuses, and for each
// slip pinSourceProblems finds in the judged run.sh.
func checkTrust(ctx context.Context, c BaseCheck, changes []ledgerChange) ([]string, error) {
	var pins []trustedPin
	for _, ch := range changes {
		if ch.next.SourceKey != "s3tests_commit" {
			continue
		}
		from := 0
		if ch.old != nil {
			from = len(ch.old.Pins)
		}
		for i := from; i < len(ch.next.Pins); i++ {
			p := ch.next.Pins[i]
			pins = append(pins, trustedPin{fmt.Sprintf("%s: pin %d (%s, %s)", ch.file, i+1, p.Source, p.Admitted), p.Source})
		}
	}
	var problems []string
	// The base's run.sh names the repository. A base whose PIN file is
	// missing or unreadable names no pin, so the judged side's is checked.
	var base runSh
	if text, err := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Base+":"+c.RunSh); err == nil {
		if r, perr := parseRunSh([]byte(text), "the base's "+c.RunSh); perr == nil {
			base = r
		}
	}
	basePin := ""
	if text, err := git(ctx, c.Repo, "cat-file", "blob", "--end-of-options", c.Base+":"+c.PinFile); err == nil {
		if p, perr := parsePin([]byte(text), "the base's "+c.PinFile); perr == nil {
			basePin = p
		}
	}
	switch data, err := readSide(ctx, c, c.RunSh); {
	case errors.Is(err, errRemoved):
	case errors.Is(err, errNotRegular):
		problems = append(problems, c.RunSh+": not a regular file")
	case err != nil:
		return nil, err
	default:
		problems = append(problems, pinSourceProblems(string(data), c.RunSh)...)
	}
	switch data, err := readSide(ctx, c, c.PinFile); {
	case errors.Is(err, errRemoved):
		if basePin != "" {
			problems = append(problems, c.PinFile+": removed; the s3-tests pin lives there")
		}
	case errors.Is(err, errNotRegular):
		problems = append(problems, c.PinFile+": not a regular file")
	case err != nil:
		return nil, err
	default:
		pin, perr := parsePin(data, c.PinFile)
		switch {
		case perr != nil:
			problems = append(problems, perr.Error())
		case pin != basePin:
			pins = append(pins, trustedPin{fmt.Sprintf("%s: the pin %s", c.PinFile, pin), pin})
		}
	}
	if len(pins) == 0 {
		return problems, nil
	}
	repo := c.CephRepo
	if repo == "" {
		repo = base.repo
	}
	if repo == "" {
		return nil, errors.New("no repository to check the s3-tests pins' branch in: the base's run.sh sets no S3TESTS_REPO")
	}
	ids := make([]string, 0, len(pins))
	for _, p := range pins {
		ids = append(ids, p.pin)
	}
	why, err := untrustedPins(ctx, repo, ids)
	if err != nil {
		return nil, err
	}
	for _, p := range pins {
		if w, ok := why[p.pin]; ok {
			problems = append(problems, untrustedProblem(p.where, repo, w))
		}
	}
	return problems, nil
}
