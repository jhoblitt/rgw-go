package citecheck

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// contentResult is what checking the content of a file's links found: each
// link that fails or is listed, in the order of its lines and, on one line,
// as written, a listed one's reason starting "listed: "; and the number of
// links checked, held, failed and listed.
type contentResult struct {
	reports                       []goFailure
	checked, held, failed, listed int
}

// checkLinks reads every file before it checks any, as check does, then
// prints each link that fails or is listed and the counts of the links
// checked, held, failed and listed.
func checkLinks(ctx context.Context, ceph string, files []string, out io.Writer) error {
	docs := make([][]byte, len(files))
	for i, name := range files {
		data, err := os.ReadFile(name) //nolint:gosec // reading the files named is the command's work
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		docs[i] = data
	}
	c, err := NewChecker(ctx, ceph)
	if err != nil {
		return err
	}
	var checked, held, failed, listed int
	for i, name := range files {
		res, err := checkContent(ctx, c, name, docs[i])
		if err != nil {
			return fmt.Errorf("%s:%w", name, err)
		}
		checked, held, failed, listed = checked+res.checked, held+res.held, failed+res.failed, listed+res.listed
		for _, f := range res.reports {
			fmt.Fprintf(out, "%s:%d: %s: %s\n", name, f.line, f.text, f.reason)
		}
	}
	fmt.Fprintf(out, "links checked: %d, held: %d, failed: %d, listed: %d\n", checked, held, failed, listed)
	if failed > 0 {
		return fmt.Errorf("%d of %d %w", failed, checked, errLinksFailed)
	}
	return nil
}

// checkContent checks every link in the markdown or Go file name, which
// holds src, as a link is checked, and then that it holds what its text
// names: the release, the file and an identifier named beside it, as
// linkChecker.check says.
func checkContent(ctx context.Context, c *Checker, name string, src []byte) (contentResult, error) {
	lc := &linkChecker{c: c, name: name}
	if isGo(name) {
		lc.d = readGo(src)
	} else {
		lc.md = readMarkdown(src)
		lc.d = lc.md.goDoc
	}
	var res contentResult
	for gi, scans := range lc.d.groups {
		for pi, ps := range scans {
			for ii, it := range ps.items {
				if it.kind != linkItem {
					continue
				}
				line := ps.para.lines[ps.para.lineAt(it.start)].n
				lc.at, lc.line = [3]int{gi, pi, ii}, line
				reason, listed, err := lc.check(ctx, ps, it)
				if err != nil {
					return contentResult{}, fmt.Errorf("%d: %s: %w", line, it.text, err)
				}
				res.checked++
				switch {
				case reason == "":
					res.held++
					continue
				case listed:
					res.listed++
				default:
					res.failed++
				}
				res.reports = append(res.reports, goFailure{line: line, text: it.text, reason: reason})
			}
		}
	}
	slices.SortStableFunc(res.reports, func(a, b goFailure) int { return cmp.Compare(a.line, b.line) })
	return res, nil
}

// linkChecker checks the content of one file's links: d is the file's
// paragraphs, by section, and md the markdown document they are read from,
// or nil for a Go file.
type linkChecker struct {
	c    *Checker
	name string
	d    *goDoc
	md   *mdDoc
	// at is the section, paragraph and item of the link being checked, and
	// line the line it starts on.
	at   [3]int
	line int
}

// check returns why the link it fails, or, with listed set, why it is
// listed, or "" when it holds. A link fails when it fails a check a link
// must pass; when its text ties to it a release that is another commit than
// its ref's, as releaseReason says; when its text names another file; when
// the identifiers its own text names stand neither in its lines nor in the
// header of a block that encloses them; and, when its text names none, when
// those named beside it that its file holds stand in neither, and is listed
// instead when othersNames finds those other links'. A link to one line that
// holds none of the names beside it is listed instead when the definition or
// the statement the line starts, as widened reads it, holds one; one whose
// own text names them is not, as it says which line it means. When its file
// holds none of those, it fails still when the same lines of another file
// its paragraph names hold one, or of one its section names hold a
// qualified name none of whose parts is generic, as elsewhere says. When
// nothing beside it counts, or none of it stands anywhere, it fails when its
// text, lines alone, continues a citation of another file, and is listed
// otherwise.
func (lc *linkChecker) check(ctx context.Context, ps *paraScan, it *goItem) (reason string, listed bool, err error) {
	c, cite := lc.c, it.link
	if reason, err = c.Check(ctx, cite); err != nil || reason != "" {
		return reason, false, err
	}
	r, err := c.resolve(ctx, cite.Ref)
	if err != nil {
		return "", false, err
	}
	if reason, err = lc.releaseReason(ctx, ps, it, r.commit); err != nil || reason != "" {
		return reason, false, err
	}
	if name, how := lc.namedFile(it, false); name != "" && path.Base(name) != path.Base(cite.Path) {
		return fmt.Sprintf("its text names %s, %s, but it links %s", name, how, cite.Path), false, nil
	}
	listing := func(why string) (string, bool, error) {
		if name, how := lc.namedFile(it, true); name != "" && path.Base(name) != path.Base(cite.Path) {
			return fmt.Sprintf("its text names %s, %s, but it links %s, and %s", name, how, cite.Path, why), false, nil
		}
		return "listed: " + why, true, nil
	}
	ids, own, text := lc.linkIdents(ps, it)
	if len(ids) == 0 {
		if loose := lc.d.looseIdents(text); len(loose) > 0 {
			return listing(fmt.Sprintf("the words beside it that may name code, %s, are English words, generic names or "+
				"parts of a longer literal, and no whole code token stands beside it to check its lines by",
				strings.Join(loose, ", ")))
		}
		return listing("no identifier is named beside it to check its lines by")
	}
	obj, err := c.read(ctx, r.commit, cite.Path)
	if err != nil {
		return "", false, err
	}
	f := obj.f
	if linkHolds(f, cite.From, cite.To, ids) {
		slog.DebugContext(ctx, "a link holds what its text names", slog.String("file", lc.name), slog.Int("line", lc.line),
			slog.String("link", cite.Text), slog.Any("identifiers", ids), slog.Bool("own_text", own))
		return "", false, nil
	}
	if from, to, what := widened(f, cite.From, cite.To); !own && what != "" && linkHolds(f, from, to, ids) {
		return listing(fmt.Sprintf("line %d holds none of %s, but the %s that starts on it, through line %d, holds one",
			cite.From, strings.Join(ids, ", "), what, to))
	}
	if !own {
		named := ids
		ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !f.has(id) })
		if len(ids) == 0 {
			reason = fmt.Sprintf("none of the identifiers named beside it, %s, stands in %s at %s",
				strings.Join(named, ", "), cite.Path, cite.Ref)
			var other string
			if other, err = lc.elsewhere(ctx, r.commit, cite, named); err != nil || other != "" {
				return reason + ", but " + other, false, err
			}
			return listing(reason)
		}
		var why string
		if why, err = lc.othersNames(ctx, ps, it, r.commit, f, ids); err != nil {
			return "", false, err
		} else if why != "" {
			return listing(why)
		}
	}
	which := "named beside it"
	if own {
		which = "its text names"
	}
	reason = fmt.Sprintf("%s of %s at %s %s none of the identifiers %s, %s", linesOf(cite.From, cite.To), cite.Path,
		cite.Ref, holdVerb(cite.From, cite.To), which, strings.Join(ids, ", "))
	if !own {
		reason += ", which stand elsewhere in it"
	}
	if n := nearestHolding(f, cite.From, ids); n > 0 {
		reason += fmt.Sprintf("; the nearest line that holds one is %d", n)
	}
	tags, err := lc.holdingAt(ctx, cite, r.commit, ids)
	if err != nil {
		return "", false, err
	}
	if len(tags) > 0 {
		they := "they hold"
		if cite.From == cite.To {
			they = "it holds"
		}
		reason += "; at " + strings.Join(tags, " and ") + " " + they + " one"
	}
	return reason, false, nil
}

// othersNames returns why the names beside a link, ids, which its lines hold
// none of, are other links' rather than its own, or "": each of them stands
// in the lines of another link of its paragraph at the link's commit, those
// of its run first, and a link of its run cites the link's file at a release
// of another major version, v20 for a v19 link, holds none of them either,
// and its lines are the same code as the link's, blanks aside. A run that
// cites each piece of code at both releases names beside it what the other
// pieces hold; the release where the pieces agree shows the link is not a
// drifted copy of a piece that holds them. A name no other link holds may be
// the link's own, which its drifted lines lack, so one beside it shows
// nothing. Two point releases of one major version, v19.2.6 and v19.2.7,
// share their code, so their agreeing shows nothing; nor does a run at one
// release, since its links may each hold the names, as a list of call sites
// does, and one that holds none may be wrong. A link's lines are read as
// they are, never widened.
func (lc *linkChecker) othersNames(ctx context.Context, ps *paraScan, it *goItem, commit string, f *file,
	ids []string,
) (string, error) {
	cite := it.link
	major := releaseMajor(cite.Ref)
	if major == "" {
		return "", nil
	}
	text := blankless(f.text[f.lineStart(cite.From):f.lineEnd(cite.To)])
	var holder, counterpart *goItem
	var at string
	held := map[string]bool{}
	sibs := lc.siblings(ps, it)
	links := slices.Clone(sibs)
	for _, o := range ps.items {
		if o != it && o.kind == linkItem && o.link.Problem == "" && !slices.Contains(sibs, o) {
			links = append(links, o)
		}
	}
	for _, o := range links {
		inRun := slices.Contains(sibs, o)
		r, err := lc.c.resolve(ctx, o.link.Ref)
		if err != nil {
			return "", err
		}
		if r.commit == "" {
			continue
		}
		obj, err := lc.c.read(ctx, r.commit, o.link.Path)
		if err != nil {
			return "", err
		}
		if obj.kind != "blob" || o.link.To > obj.f.lines() {
			continue
		}
		if r.commit == commit {
			for _, id := range ids {
				if linkHolds(obj.f, o.link.From, o.link.To, []string{id}) {
					held[id] = true
					if holder == nil {
						holder = o
					}
				}
			}
			continue
		}
		if inRun && counterpart == nil && o.link.Path == cite.Path &&
			releaseMajor(o.link.Ref) != "" && releaseMajor(o.link.Ref) != major &&
			!linkHolds(obj.f, o.link.From, o.link.To, ids) &&
			blankless(obj.f.text[obj.f.lineStart(o.link.From):obj.f.lineEnd(o.link.To)]) == text {
			counterpart, at = o, o.link.Ref
		}
	}
	if counterpart == nil || slices.ContainsFunc(ids, func(id string) bool { return !held[id] }) {
		return "", nil
	}
	return fmt.Sprintf("the names beside it, %s, are other links': each stands in the lines of a link of its paragraph "+
		"at %s, %s first; %s cites the same code at %s and holds none either", strings.Join(ids, ", "), cite.Ref,
		holder.link.Text, counterpart.link.Text, at), nil
}

// blankless returns s with its blanks dropped, so that two releases' lines
// that differ only in their blanks compare equal.
func blankless(s string) string { return strings.Join(strings.Fields(s), "") }

// reReleaseTag is a release tag, v19.2.6.
var reReleaseTag = regexp.MustCompile(`^` + tagForm + `$`)

// releaseMajor returns the major version of a ref that is a release tag, as
// majorOf reads one, or "" for any other ref, such as a commit id.
func releaseMajor(ref string) string {
	if !reReleaseTag.MatchString(ref) {
		return ""
	}
	return majorOf(ref)
}

// releaseReason returns why a link's text ties to it a release other than
// commit, its ref's, or "": a release its text names, as namedRefs finds one,
// that is another commit; or, when nothing ties one to it, a paragraph that
// marks its Tentacle citations [T] while it links lines at v20.2.4 unmarked,
// unless a citation before it in its run carries the [T] or the words beside
// it name Tentacle, by its tag or its codename.
func (lc *linkChecker) releaseReason(ctx context.Context, ps *paraScan, it *goItem, commit string) (string, error) {
	named, tied := namedRefs(ps, it)
	for _, n := range named {
		other, err := lc.c.resolve(ctx, n.ref)
		if err != nil {
			return "", err
		}
		if other.commit != "" && other.commit != commit {
			return fmt.Sprintf("its text names %s, %s, but it links lines at %s", n.ref, n.how, it.link.Ref), nil
		}
	}
	if tied || !marksTentacle(ps, it) || namesTentacle(lc.beside(ps, it)) {
		return "", nil
	}
	t, err := lc.c.resolve(ctx, markTags["T"])
	if err != nil {
		return "", err
	}
	if t.commit != "" && t.commit == commit {
		return fmt.Sprintf("its paragraph marks [T] its links at %s and its text is unmarked, but it links lines at %s",
			markTags["T"], it.link.Ref), nil
	}
	return "", nil
}

// namesTentacle reports whether s names the release [T] marks, by its tag or
// its codename, and no other release: words that name two, as "parses on
// Tentacle and fails on Squid" does, name neither as the link's.
func namesTentacle(s string) bool {
	named := map[string]bool{}
	for _, t := range reTagIn.FindAllString(s, -1) {
		named[t] = true
	}
	for _, c := range reCodename.FindAllString(s, -1) {
		named[codenameTags[strings.ToLower(c)]] = true
	}
	return len(named) == 1 && named[markTags["T"]]
}

// reTagIn is a tag as a word of its own, anywhere in a text.
var reTagIn = regexp.MustCompile(`\b` + tagForm + `\b`)

// holdingAt returns the releases the markers name, other than commit, at
// which the same lines of the link's file hold one of ids.
func (lc *linkChecker) holdingAt(ctx context.Context, cite Citation, commit string, ids []string) ([]string, error) {
	var out []string
	for _, tag := range []string{markTags["S"], markTags["T"]} {
		alt, err := lc.c.resolve(ctx, tag)
		if err != nil {
			return nil, err
		}
		if alt.commit == "" || alt.commit == commit {
			continue
		}
		o, err := lc.c.read(ctx, alt.commit, cite.Path)
		if err != nil {
			return nil, err
		}
		if o.kind == "blob" && cite.To <= o.f.lines() && linkHolds(o.f, cite.From, cite.To, ids) {
			out = append(out, tag)
		}
	}
	return out, nil
}

// namedFile returns the file a link's text names, and how: the file its own
// text writes or, with continued set and for a text that writes lines alone,
// as a continuation does, the file the nearest link, citation or file name
// before it in its section names; or "" when its text names none.
func (lc *linkChecker) namedFile(it *goItem, continued bool) (name, how string) {
	if it.label == "" {
		return "", ""
	}
	own := plainItems(unescape(it.label))
	for _, o := range own {
		if o.kind == fileItem || o.kind == mentionItem {
			return o.name, "in its text"
		}
	}
	if !continued || !slices.ContainsFunc(own, func(o *goItem) bool { return o.kind == contItem }) {
		return "", ""
	}
	gi, pi, ii := lc.at[0], lc.at[1], lc.at[2]
	for p := pi; p >= 0; p-- {
		items := lc.d.groups[gi][p].items
		end := len(items)
		if p == pi {
			end = ii
		}
		for _, o := range slices.Backward(items[:end]) {
			switch {
			case o.kind == linkItem && o.link.Problem == "":
				return o.link.Path, "as the continuation of " + o.text + " before it"
			case o.kind == fileItem, o.kind == mentionItem:
				return o.name, "as the continuation of " + o.text + " before it"
			}
		}
	}
	return "", ""
}

// elsewhere returns, for a link none of whose identifiers ids stands in the
// file it links, the other file whose same lines at the link's commit hold
// one, and the identifier, "" when there is none: a link to the wrong file.
// It looks in the files its paragraph links or names, then, for a qualified
// name none of whose parts is generic, in those the rest of its section
// does, where a common name would stand in some file's lines by chance. A
// file that holds one only on other lines is no evidence: a name beside a
// link is as often an error table's or a neighboring citation's. Nor is a
// C++ keyword, which stands in any file's code.
func (lc *linkChecker) elsewhere(ctx context.Context, commit string, cite Citation, ids []string) (string, error) {
	gi, pi := lc.at[0], lc.at[1]
	scans := lc.d.groups[gi]
	order := []int{pi}
	for p := range scans {
		if p != pi {
			order = append(order, p)
		}
	}
	seen := map[string]bool{cite.Path: true}
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return cppKeywords[id] })
	qualified := slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		parts := strings.Split(id, "::")
		return len(parts) < 2 || slices.ContainsFunc(parts, func(p string) bool { return genericWords[strings.ToLower(p)] })
	})
	for _, p := range order {
		where := "its paragraph"
		if p != pi {
			where, ids = "its section", qualified
		}
		var paths []string
		for _, o := range scans[p].items {
			var named []string
			switch {
			case o.kind == linkItem && o.link.Problem == "":
				named = []string{o.link.Path}
			case o.kind == fileItem, o.kind == mentionItem:
				all, err := lc.c.filesNamed(ctx, commit, o.name)
				if err != nil {
					return "", err
				}
				named = preferRGW(all)
			}
			for _, n := range named {
				if !seen[n] {
					seen[n] = true
					paths = append(paths, n)
				}
			}
		}
		for _, name := range paths {
			obj, err := lc.c.read(ctx, commit, name)
			if err != nil {
				return "", err
			}
			if obj.kind != "blob" || cite.To > obj.f.lines() {
				continue
			}
			for _, id := range ids {
				if obj.f.has(id) && linkHolds(obj.f, cite.From, cite.To, []string{id}) {
					return fmt.Sprintf("%s of %s, which %s names, %s %s", linesOf(cite.From, cite.To), name, where,
						holdVerb(cite.From, cite.To), id), nil
				}
			}
		}
	}
	return "", nil
}

// linesOf names a citation's lines: "line 10" or "lines 10-12".
func linesOf(from, to int) string {
	if from == to {
		return "line " + strconv.Itoa(from)
	}
	return fmt.Sprintf("lines %d-%d", from, to)
}

// holdVerb is "holds" for one line and "hold" for several.
func holdVerb(from, to int) string {
	if from == to {
		return "holds"
	}
	return "hold"
}

// namedRef is a release a link's text ties to it, and how.
type namedRef struct{ ref, how string }

// namedRefs returns the releases a link's text ties to it, and whether
// anything ties a release to it at all, a tie that names no one release
// included:
//
//   - a marker or a tag in its own text, unless the text names two releases;
//   - a marker or a tag right before it, blanks alone between;
//   - an "at", "in" or "on" and a tag after it or after the citations listed
//     with it, unless "and", "or", "/" or "," and another tag follow it, and
//     "at both tags" after it, which names no one; but for a citation joined
//     by "and" or "or" to another's own "at <tag>" that ends its group, as B
//     is in "A at v19.2.7 and B, C at v20.2.4", only one right after it, and
//     else that tag;
//   - an "at", "in" or "on" and a marker right after it;
//   - and, when none of those ties it, an "at" or "in" and a tag that closes
//     the parenthesis it stands in, as parenRef finds one.
//
// So a clause that names two releases ties neither, as a sentence that does
// gives a plain citation none.
func namedRefs(ps *paraScan, it *goItem) (out []namedRef, tied bool) {
	var own []namedRef
	for _, m := range reTag.FindAllStringSubmatch(unescape(it.label), -1) {
		if m[1] != "" {
			own = append(own, namedRef{ref: markTags[m[1]], how: "by the [" + m[1] + "] in its text"})
		} else {
			own = append(own, namedRef{ref: m[2], how: "in its text"})
		}
	}
	if len(own) > 0 {
		tied = true
		if !slices.ContainsFunc(own, func(n namedRef) bool { return n.ref != own[0].ref }) {
			out = append(out, own...)
		}
	}
	if loc := reTiedBefore.FindStringIndex(ps.norm[:it.pos]); loc != nil {
		tied = true
		w := strings.TrimSpace(ps.norm[loc[0]:loc[1]])
		if m := reTag.FindStringSubmatch(w); len(m) > 1 && m[1] != "" {
			out = append(out, namedRef{ref: markTags[m[1]], how: "by the [" + m[1] + "] right before it"})
		} else {
			out = append(out, namedRef{ref: w, how: "right before it"})
		}
	}
	cut, joined := afterOwnTagCut(ps.norm, it.pos)
	if t, both := tagAfter(cut, it.pos+1); t != "" || both {
		tied = true
		if t != "" && !anotherTagAfter(ps.norm[it.pos+1:], t) {
			out = append(out, namedRef{ref: t, how: `by the "at ` + t + `" after it`})
		}
	} else if joined != "" {
		tied = true
		out = append(out, namedRef{ref: joined, how: `by the "at ` + joined + `" it is joined to by "and" or "or"`})
	}
	if m := reAtMark.FindStringSubmatch(ps.norm[it.pos+1:]); m != nil {
		tied = true
		out = append(out, namedRef{ref: markTags[m[1]], how: `by the "at [` + m[1] + `]" after it`})
	}
	if !tied {
		if t := parenRef(ps, it); t != "" {
			tied = true
			out = append(out, namedRef{ref: t, how: `by the "at ` + t + `" that closes its parenthesis`})
		}
	}
	return out, tied
}

// reAfterOwnTag is what stands between two citations when the first carries
// its own "at <tag>" and the second is joined to it by "and" or "or", as B is
// in "A at v19.2.7 and B", and the tag. A comma alone starts another group, as
// C does in "A and B at v19.2.7, C and D at v20.2.4".
var reAfterOwnTag = regexp.MustCompile(`^\s*(?:at|in|on)\s+(` + tagForm + `)\s*,?\s*(?:and|or)\s+$`)

// reGroupEnd is what ends a group of citations right after one: a comma, a
// ";", a ".", a ")" or the end of the text.
var reGroupEnd = regexp.MustCompile(`^\s*(?:[,;.)]|$)`)

// afterOwnTagCut returns norm and "", or, for the citation at pos when it is
// joined to a citation's own "at <tag>" and ends its group, norm cut at the
// citation after it, so that tagAfter reads only an "at <tag>" right after
// it, and that tag, which ties the citation when nothing after it does: in "A
// at v19.2.7 and B, C at v20.2.4 and D" B is at v19.2.7, and the v20.2.4 is
// C's and D's. A citation that "and" or "or" joins to the next one starts a
// group of its own, as B does in "A at v19.2.7 and B and C at v20.2.4", where
// B and C are at v20.2.4.
func afterOwnTagCut(norm string, pos int) (cut, joined string) {
	prev := strings.LastIndexByte(norm[:pos], citeMark)
	if prev < 0 {
		return norm, ""
	}
	m := reAfterOwnTag.FindStringSubmatch(norm[prev+1 : pos])
	if m == nil || !reGroupEnd.MatchString(norm[pos+1:]) {
		return norm, ""
	}
	if k := strings.IndexByte(norm[pos+1:], citeMark); k >= 0 {
		return norm[:pos+1+k], m[1]
	}
	return norm, m[1]
}

// reAnotherTag is "and", "or", "/" or "," and a tag, maybe after an "at",
// "in" or "on", at the start of a text.
var reAnotherTag = regexp.MustCompile(`^\s*(?:,|/|\band\b|\bor\b)\s*(?:(?:at|in|on)\s+)?` + tagForm + `\b`)

// anotherTagAfter reports whether the "at <tag>" for tag that tagAfter finds
// in s is followed by another tag, as "at v19.2.6 and v20.2.4" is; but not
// by one that only blanks part from the next citation, which is that
// citation's own, tied right before it, as in "at v19.2.6, v20.2.4 [M]".
func anotherTagAfter(s, tag string) bool {
	loc := regexp.MustCompile(`\b(?:at|in|on)\s+` + regexp.QuoteMeta(tag) + `\b`).FindStringIndex(s)
	if len(loc) != 2 {
		return false
	}
	m := reAnotherTag.FindStringIndex(s[loc[1]:])
	return len(m) == 2 && !strings.HasPrefix(strings.TrimLeft(s[loc[1]+m[1]:], " \t\n"), string(citeMark))
}

// marksTentacle reports whether another link of a link's paragraph carries a
// [T], as a paragraph does that marks its Tentacle links and leaves its
// Squid ones unmarked, and none before it in its run of citations does,
// whose [T] would mark the run's every citation.
func marksTentacle(ps *paraScan, it *goItem) bool {
	cites := func(o *goItem) bool { return o.kind == linkItem || o.kind == fileItem || o.kind == contItem }
	if !slices.ContainsFunc(ps.items, func(o *goItem) bool { return o != it && cites(o) && linkMark(ps, o) == "T" }) {
		return false
	}
	next := it.pos
	for i := slices.Index(ps.items, it) - 1; i >= 0; i-- {
		o := ps.items[i]
		if !cites(o) || !reRunGap.MatchString(strings.ReplaceAll(ps.norm[o.pos+1:next], "`", "")) {
			break
		}
		if linkMark(ps, o) == "T" {
			return false
		}
		next = o.pos
	}
	return true
}

// linkMark returns the marker a citation's text carries or that stands right
// before it, "S" or "T", or "".
func linkMark(ps *paraScan, o *goItem) string {
	if o.mark == "S" || o.mark == "T" {
		return o.mark
	}
	if m := reTag.FindStringSubmatch(unescape(o.label)); len(m) > 1 && m[1] != "" {
		return m[1]
	}
	if loc := reTiedBefore.FindStringIndex(ps.norm[:o.pos]); loc != nil {
		if m := reTag.FindStringSubmatch(strings.TrimSpace(ps.norm[loc[0]:loc[1]])); len(m) > 1 && m[1] != "" {
			return m[1]
		}
	}
	return ""
}

// reAtMark is an "at", "in" or "on" and a marker, [S] or [T], right after a
// citation.
var reAtMark = regexp.MustCompile(`^\s*(?:at|in|on)\s+\[([ST])\]`)

// reParenAt is an "at <tag>", "in <tag>", "all at <tag>" or "all in <tag>"
// after a ";" or "," that ends a parenthesis.
var reParenAt = regexp.MustCompile(`[;,]\s*(?:all\s+)?(?:at|in) (` + tagForm + `)\s*$`)

// parenRef returns the release an "at <tag>" names that closes the
// parenthesis a link stands in, after a ";" or ",", as "(`A`, [L]; `B`, [L];
// at v20.2.4)" names one for both links; or "" when there is none, or when a
// citation between the link and the ")" carries a tie of its own, a marker
// or a tag in its text or right before it or an "at <tag>" after it, which
// the closing tag does not reach across.
func parenRef(ps *paraScan, it *goItem) string {
	norm, pos := ps.norm, it.pos
	open, depth := -1, 0
	for i := pos - 1; i >= 0 && open < 0; i-- {
		switch norm[i] {
		case ')':
			depth++
		case '(':
			if depth == 0 {
				open = i
			}
			depth--
		}
	}
	if open < 0 {
		return ""
	}
	end := -1
	depth = 0
	for i := pos + 1; i < len(norm) && end < 0; i++ {
		switch norm[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				end = i
			}
			depth--
		}
	}
	if end < 0 {
		return ""
	}
	m := reParenAt.FindStringSubmatch(norm[open+1 : end])
	if m == nil {
		return ""
	}
	for _, o := range ps.items {
		if o.pos > pos && o.pos < end && ownTie(ps, o) {
			return ""
		}
	}
	return m[1]
}

// ownTie reports whether a citation carries a tie of its own: a marker or a
// tag in its text or right before it, or an "at <tag>", "at both tags" or
// "at [S]" or "at [T]" after it.
func ownTie(ps *paraScan, o *goItem) bool {
	if o.kind != linkItem && o.kind != fileItem && o.kind != contItem {
		return false
	}
	if o.mark != "" || reTag.MatchString(unescape(o.label)) || reTiedBefore.MatchString(ps.norm[:o.pos]) {
		return true
	}
	cut, joined := afterOwnTagCut(ps.norm, o.pos)
	if t, both := tagAfter(cut, o.pos+1); t != "" || both || joined != "" {
		return true
	}
	return reAtMark.MatchString(ps.norm[o.pos+1:])
}

// nearestHolding returns the line of f nearest to line from that holds one
// of ids, the earlier of two as near, or 0 when none does.
func nearestHolding(f *file, from int, ids []string) int {
	best := 0
	for n := 1; n <= f.lines(); n++ {
		line := f.text[f.lineStart(n):f.lineEnd(n)]
		if !slices.ContainsFunc(ids, func(id string) bool { return standsIn(line, id) }) {
			continue
		}
		if best == 0 || abs(n-from) < abs(best-from) {
			best = n
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
