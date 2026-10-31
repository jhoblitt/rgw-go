package citecheck

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// registryDocs are the registries, by their paths from rgw-go's root, whose
// claims about ceph main must each name the main commit they were checked at.
var registryDocs = []string{"docs/ceph-upstream-bugs.md", "docs/exclusions.md"}

// isRegistry reports whether a file named on the command line is one of
// registryDocs: its cleaned path is one, or ends in "/" and one.
func isRegistry(name string) bool {
	p := filepath.ToSlash(filepath.Clean(name))
	for _, r := range registryDocs {
		if p == r || strings.HasSuffix(p, "/"+r) {
			return true
		}
	}
	return false
}

// unitKind is the kind of block a claim about main is judged in.
type unitKind int

const (
	unitPara unitKind = iota
	unitItem
	unitHeading
	unitRow
)

// mdUnit is a block of a registry: a paragraph outside every list, a list
// item, a heading or a table row.
type mdUnit struct {
	kind unitKind
	// indent is a list item's marker's column.
	indent int
	// parent is the list item an item is nested in, and sub the items nested
	// in it.
	parent *mdUnit
	sub    []*mdUnit
	// lines are its own lines, without their indentation: a list item's are
	// its paragraphs', those of the items nested in it aside.
	lines []unitLine
}

// unitLine is a line of a unit and its number in the document.
type unitLine struct {
	n    int
	text string
}

// text returns a unit's own lines joined by line breaks.
func (u *mdUnit) text() string {
	var b strings.Builder
	for i, l := range u.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.text)
	}
	return b.String()
}

// lineAt returns the number of the line that holds offset off of text().
func (u *mdUnit) lineAt(off int) int {
	for _, l := range u.lines {
		if off <= len(l.text) {
			return l.n
		}
		off -= len(l.text) + 1
	}
	return u.lines[len(u.lines)-1].n
}

// root returns the outermost list item a list item is nested in, or the
// item itself.
func (u *mdUnit) root() *mdUnit {
	for u.parent != nil {
		u = u.parent
	}
	return u
}

// readUnits returns a markdown document's units in the order they start.
// Fenced code blocks are skipped. A list item runs on through every line
// indented past its marker, its nested items' included, and through a line
// with less indentation that continues its paragraph with no blank line
// before it, as markdown's lazy continuation does; a blank line, a heading or
// a table row ends a paragraph outside lists, and a heading ends every list.
func readUnits(src []byte) []*mdUnit {
	var units, stack []*mdUnit
	var para *mdUnit
	var fence string
	// lazy reports whether the line before was a paragraph line of the
	// innermost open item, which a line with less indentation may continue.
	lazy := false
	add := func(u *mdUnit) *mdUnit {
		units = append(units, u)
		return u
	}
	for i, line := range strings.Split(string(src), "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r")
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		t := strings.TrimLeft(line, " \t")
		if t == "" {
			para, lazy = nil, false
			continue
		}
		indent := len(line) - len(t)
		f := opensFence(line)
		heading, row, marker := isHeading(line), strings.HasPrefix(t, "|"), reListMarker.MatchString(line)
		if heading {
			stack = nil
		}
		continues := lazy && !row && !marker && f == ""
		for !continues && len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		switch {
		case f != "":
			fence, para, lazy = f, nil, false
		case heading:
			add(&mdUnit{kind: unitHeading, lines: []unitLine{{n, t}}})
			para, lazy = nil, false
		case marker:
			u := &mdUnit{kind: unitItem, indent: indent, lines: []unitLine{{n, t}}}
			if len(stack) > 0 {
				u.parent = stack[len(stack)-1]
				u.parent.sub = append(u.parent.sub, u)
			}
			stack = append(stack, add(u))
			para, lazy = nil, true
		case len(stack) > 0:
			top := stack[len(stack)-1]
			top.lines = append(top.lines, unitLine{n, t})
			lazy = !row
		case row:
			add(&mdUnit{kind: unitRow, lines: []unitLine{{n, t}}})
			para = nil
		default:
			if para == nil {
				para = add(&mdUnit{kind: unitPara})
			}
			para.lines = append(para.lines, unitLine{n, t})
		}
	}
	return units
}

var (
	// reMainWord is the word main, in any case.
	reMainWord = regexp.MustCompile(`(?i)\bmain\b`)
	// reHexWord is a word of hex digits as long as a main commit is named
	// by or longer, and reShortHex one too short to be.
	reHexWord  = regexp.MustCompile(`\b[0-9a-fA-F]{9,40}\b`)
	reShortHex = regexp.MustCompile(`\b[0-9a-fA-F]{4,8}\b`)
	// reMdLink is a markdown link, its text and its destination.
	reMdLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
	// reBareURL is a URL outside a link's destination.
	reBareURL     = regexp.MustCompile(`https?://[^\s<>()]+`)
	reBacktickRun = regexp.MustCompile("`+")
	// reHistoryItem is the first line of a registry entry's Found item,
	// which records how and when the entry was found.
	reHistoryItem = regexp.MustCompile(`^[-*+]\s+\*\*Found:\*\*`)
)

// minCommitLen is the fewest hex digits a main commit is named by.
const minCommitLen = 9

// maskNonProse returns text with its code spans, its links' destinations, the
// text of its links to a heading and its bare URLs blanked, line breaks kept,
// so that no "main" in code, a path, an anchor or an entry's title is read
// as a claim and every offset still holds.
func maskNonProse(text string) string {
	b := []byte(text)
	blank := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	runs := reBacktickRun.FindAllStringIndex(text, -1)
	for i := 0; i < len(runs); i++ {
		for j := i + 1; j < len(runs); j++ {
			if runs[j][1]-runs[j][0] == runs[i][1]-runs[i][0] {
				blank(runs[i][0], runs[j][1])
				i = j
				break
			}
		}
	}
	for _, m := range reMdLink.FindAllStringSubmatchIndex(text, -1) {
		dest := text[m[4]:m[5]]
		if strings.HasPrefix(dest, "#") || strings.Contains(dest, ".md#") {
			blank(m[2], m[3])
		}
		blank(m[4]-1, m[1])
	}
	for _, m := range reBareURL.FindAllStringIndex(text, -1) {
		blank(m[0], m[1])
	}
	return string(b)
}

// commitsIn returns the commit ids text names, in order, each a word of
// minCommitLen to 40 hex digits with a digit and a letter in it.
func commitsIn(text string) []string {
	var ids []string
	for _, w := range reHexWord.FindAllString(text, -1) {
		if isCommitWord(strings.ToLower(w)) {
			ids = append(ids, w)
		}
	}
	return ids
}

// shortCommitIn returns the first word of text that would be a commit id but
// for its length, or "".
func shortCommitIn(text string) string {
	for _, w := range reShortHex.FindAllString(text, -1) {
		if isCommitWord(strings.ToLower(w)) {
			return w
		}
	}
	return ""
}

// mainCommits tells, with a clone, whether a commit id a claim names is on
// ceph main: a commit the clone has that its origin/main, or its main when it
// has no origin/main, descends from.
type mainCommits struct {
	c   *Checker
	tip string
	// why holds, for each id asked about, why it is not on main, or "".
	why map[string]string
}

// newMainCommits returns a mainCommits reading c, or an error when the clone
// has neither an origin/main nor a main branch.
func newMainCommits(ctx context.Context, c *Checker) (*mainCommits, error) {
	tip, err := c.branchTip(ctx, "origin/main")
	if err != nil {
		return nil, err
	}
	if tip == "" {
		return nil, fmt.Errorf("%s has neither an origin/main nor a main branch to check main commits against", c.dir)
	}
	return &mainCommits{c: c, tip: tip, why: map[string]string{}}, nil
}

// notOnMain returns why id is no commit on ceph main, or "" when it is one.
func (m *mainCommits) notOnMain(ctx context.Context, id string) (string, error) {
	if why, ok := m.why[id]; ok {
		return why, nil
	}
	r, err := m.c.resolve(ctx, id)
	if err != nil {
		return "", err
	}
	why := r.problem
	if why == "" {
		_, err := git(ctx, m.c.dir, nil, "merge-base", "--is-ancestor", r.commit, m.tip)
		switch e, ok := errors.AsType[*exec.ExitError](err); {
		case ok && e.ExitCode() == 1:
			why = fmt.Sprintf("commit %s is not on ceph main: %s's origin/main does not descend from it", id, m.c.dir)
		case err != nil:
			return "", err
		}
	}
	m.why[id] = why
	return why, nil
}

// mainClaims returns the number of claims about ceph main in a registry and
// those that name no main commit, each with the line its "main" is on, the
// sentence that holds it, and what to write. A claim names one when its
// scope, as claimScope says, names a commit id, and, with mc set, when one of
// the ids it names is on ceph main and the id it calls main's, as "main
// <id>" does, is one. In a Found item, which records history, a "main" is a
// claim only when an id follows it so, and then needs only that id on ceph
// main. A claim that main was not checked fails whatever its scope names.
func mainClaims(ctx context.Context, mc *mainCommits, src []byte) (int, []goFailure, error) {
	claims := 0
	var out []goFailure
	for _, u := range readUnits(src) {
		if u.kind == unitHeading {
			continue
		}
		history := u.kind == unitItem && reHistoryItem.MatchString(u.root().lines[0].text)
		text := u.text()
		masked := maskNonProse(text)
		for _, m := range reMainWord.FindAllStringIndex(masked, -1) {
			if notAClaim(masked, m[0], m[1]) {
				continue
			}
			id := mainID(masked[m[1]:])
			if history && id == "" {
				continue
			}
			claims++
			reason, err := claimProblem(ctx, mc, u, text, m[0], id, history)
			if err != nil {
				return 0, nil, err
			}
			if reason != "" {
				lo, hi := sentence(text, m[0], u.kind == unitRow)
				out = append(out, goFailure{line: u.lineAt(m[0]), text: claimText(text[lo:hi]), reason: reason})
			}
		}
	}
	return claims, out, nil
}

var (
	// reMainID is what follows a "main" that calls a commit id main's: the
	// id, after a blank, an "at", a "(" or a "'s", as "main 8aa24c51928",
	// "main at 8aa24c51928", "main (8aa24c51928" and "main's 8aa24c51928" do.
	reMainID = regexp.MustCompile(`^(?:'s)?\s+(?:at\s+|\()?([0-9a-fA-F]{7,40})\b`)
	// reUnchecked is what follows a "main" that says main was not checked,
	// and reUncheckedBefore what stands before one that does: "main not
	// checked", "main: not checked", "the main branch was not checked", "main
	// was never checked", "main has not been checked", "main not yet
	// checked", "main unchecked", "main remains unchecked", "main is still
	// unverified", "main was left unchecked", "not checked on main" and
	// "unchecked on main".
	reUnchecked = regexp.MustCompile(`(?i)^(?:\s+branch(?:es)?)?[\s:;,(-]*` +
		`(?:(?:was|is|were|are|has|have|had|remains?|remained|stays?|stayed)\s+)?(?:still\s+)?(?:left\s+)?` +
		`(?:(?:(?:not|never)\s+|(?:wasn't|isn't|weren't|aren't|hasn't|haven't|hadn't)\s+)(?:yet\s+)?(?:been\s+)?` +
		`(?:checked|verified)\b|(?:yet\s+)?un(?:checked|verified)\b)`)
	reUncheckedBefore = regexp.MustCompile(`(?i)(?:\b(?:not|never)\s+(?:yet\s+)?(?:been\s+)?|n't\s+(?:yet\s+)?` +
		`(?:been\s+)?|\bun)(?:checked|verified)\s+(?:on|at|against|in)\s+(?:the\s+)?(?:ceph\s+)?$`)
)

// mainID returns the commit id that after, the text right after a "main",
// calls main's, or "".
func mainID(after string) string {
	m := reMainID.FindStringSubmatch(after)
	if len(m) < 2 || !isCommitWord(strings.ToLower(m[1])) {
		return ""
	}
	return m[1]
}

// claimProblem returns why the claim whose "main" is at off in unit u's text
// fails, or "" when it names a main commit. id is the commit id the claim
// calls main's, or "", which must have minCommitLen hex digits or more, and
// history reports whether the claim is in a Found item, where only that id is
// checked.
func claimProblem(ctx context.Context, mc *mainCommits, u *mdUnit, text string, off int, id string,
	history bool,
) (string, error) {
	if reUnchecked.MatchString(text[off+len("main"):]) || reUncheckedBefore.MatchString(text[max(0, off-40):off]) {
		return fmt.Sprintf(`says ceph main was not checked: check it, and write the main commit it was checked at, as `+
			`"at main <commit>" with %d hex digits or more`, minCommitLen), nil
	}
	if id != "" && len(id) < minCommitLen {
		return fmt.Sprintf("calls %s main's, but %s has fewer than %d hex digits: write the main commit it was "+
			"checked at with %d or more", id, id, minCommitLen, minCommitLen), nil
	}
	if mc != nil && id != "" {
		why, err := mc.notOnMain(ctx, id)
		if err != nil {
			return "", err
		}
		if why != "" {
			return fmt.Sprintf("calls %s main's, but %s: name the main commit it was checked at, or the branch %s is on",
				id, why, id), nil
		}
	}
	if history {
		return "", nil
	}
	scope, where := claimScope(u, text, off)
	ids := commitsIn(scope)
	if len(ids) == 0 {
		reason := fmt.Sprintf(`a claim about ceph main that names no main commit: write the main commit it was checked `+
			`at, as "at main <commit>" with %d hex digits or more or as a link to ceph/ceph/blob/<commit>/..., in %s`,
			minCommitLen, where)
		if short := shortCommitIn(scope); short != "" {
			reason += fmt.Sprintf(" (%s has fewer than %d hex digits)", short, minCommitLen)
		}
		return reason, nil
	}
	if mc == nil {
		return "", nil
	}
	var whys []string
	for _, id := range ids {
		why, err := mc.notOnMain(ctx, id)
		if err != nil || why == "" {
			return "", err
		}
		whys = append(whys, why)
	}
	return fmt.Sprintf("a claim about ceph main that names no commit on ceph main: %s: write the main commit it was "+
		"checked at in %s", strings.Join(whys, "; "), where), nil
}

// claimScope returns the text a claim at off in unit u's text must name its
// commit in, and where that is, for the reason a failure gives: a list
// item's own text, all its paragraphs, but not the items nested in it or
// those it is nested in; a paragraph's sentence; or a table row whole.
func claimScope(u *mdUnit, text string, off int) (scope, where string) {
	switch u.kind {
	case unitItem:
		return text, "its own list item, not one nested in it or around it"
	case unitPara:
		lo, hi := sentence(text, off, false)
		return text[lo:hi], "its sentence"
	}
	return text, "its table row"
}

// claimText returns a claim's sentence as one line, cut to a length a
// failure's line can carry.
func claimText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const most = 160
	if r := []rune(s); len(r) > most {
		return string(r[:most]) + "…"
	}
	return s
}

var (
	// reOtherMain is what stands before a "main" that is another project's
	// branch, rather than ceph's.
	reOtherMain = regexp.MustCompile(`(?i)\b(?:rook|s3-tests|rgw-go|rgw-rs|go-ceph|rgw-bug-reproduction)(?:'s|')?\s+$`)
	// reMainNoun is what follows a "main" that is an adjective, as in the
	// s3-tests harness's main user.
	reMainNoun = regexp.MustCompile(`^\s+(?:users?|threads?|loops?|functions?)\b`)
)

// notAClaim reports whether the "main" at masked[lo:hi] names something
// other than ceph main: a file or a function, as main.go and main() do;
// another project's branch, as Rook main does; or a main thing, as a main
// user is.
func notAClaim(masked string, lo, hi int) bool {
	after, before := masked[hi:], masked[max(0, lo-40):lo]
	switch {
	case len(after) > 1 && after[0] == '.' && isIdent(after[1]), strings.HasPrefix(after, "("):
		return true
	}
	return reOtherMain.MatchString(before) || reMainNoun.MatchString(after)
}
