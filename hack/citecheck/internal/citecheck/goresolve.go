package citecheck

import (
	"cmp"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	// reTag is a release named before a citation: an [S] or [T] marker, or
	// a tag.
	reTag = regexp.MustCompile(`\[([ST])\]|\b(` + tagForm + `)\b`)
	// reAtTag is "at <tag>", or "in" or "on" one, right after a citation.
	reAtTag = regexp.MustCompile("^[`\\s]*\\)?\\s*(?:at|in|on) (" + tagForm + `)\b`)
	// reHeader is a comment saying what a bare line number means: the tag,
	// and the file when it names one.
	reHeader = regexp.MustCompile(`(?i)\ba bare (?:(` + goPath + `) )?line(?: number)?s?(?: below)? (?:is|are|being) ` +
		`(?:(` + goPath + `)'s at (` + tagForm + `)|(` + tagForm + `)'s(?: (` + goPath + `))?)`)
	reIdent   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	reSpan    = regexp.MustCompile("`[^`]+`")
	reCamel   = regexp.MustCompile(`[a-z][A-Z]`)
	reAcronym = regexp.MustCompile(`^[A-Z]{2,}[a-z]`)
	reNumber  = regexp.MustCompile(`^(?:v\d+|\d+)$`)
)

// markTags are the releases the [S] and [T] markers name, at which a
// citation's file is looked for too, besides its own ref, to tell whether it
// is ceph's.
var markTags = map[string]string{"S": "v19.2.6", "T": "v20.2.4"}

// stopWords are words the identifier patterns match that name no code.
var stopWords = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq(`this that with from when where which then than into only each every both none
	must also have does done else true false null zero radosgw squid tentacle tags line lines file return returns
	check checks error errors code case cases value values item items name names the and for not are was were
	its per via see here there same other`) {
		stopWords[w] = true
	}
}

// goDoc is what a Go file's comments cite, the header conventions they
// state, and the names the file's code is written in.
type goDoc struct {
	groups  [][]*paraScan
	headers []header
	// code holds every identifier the file's Go code writes, and pkgs the
	// names its imports are referred to by.
	code, pkgs map[string]bool
}

// header is a comment saying what a bare line number means: the tag a
// citation that names none is at, the file it names, when it names one,
// whose citations alone take the tag, and the files a continuation that
// follows no citation in its own comment may be of: that file, or else the
// files the header's paragraph names before it.
type header struct {
	off   int
	tag   string
	named string
	names []string
}

func readGo(src []byte) *goDoc {
	d := &goDoc{code: map[string]bool{}, pkgs: map[string]bool{}}
	if f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution); err == nil {
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				d.code[id.Name] = true
			}
			return true
		})
		for _, imp := range f.Imports {
			name := path.Base(strings.Trim(imp.Path.Value, `"`))
			if imp.Name != nil {
				name = imp.Name.Name
			}
			d.pkgs[name] = true
		}
	}
	for _, g := range goComments(src) {
		var scans []*paraScan
		for _, p := range g.paras {
			ps := scanPara(p)
			scans = append(scans, ps)
			for _, m := range reHeader.FindAllStringSubmatchIndex(ps.masked, -1) {
				group := func(i int) string {
					if m[2*i] < 0 {
						return ""
					}
					return ps.masked[m[2*i]:m[2*i+1]]
				}
				h := header{off: p.offset(m[0]), tag: cmp.Or(group(3), group(4))}
				if name := cmp.Or(group(1), group(2), group(5)); name != "" {
					h.named, h.names = name, []string{name}
				} else {
					for _, it := range slices.Backward(ps.items) {
						if it.start < m[0] && it.kind != urlItem && it.kind != contItem {
							h.names = append(h.names, cmp.Or(it.path, it.name))
						}
					}
				}
				d.headers = append(d.headers, h)
			}
		}
		d.groups = append(d.groups, scans)
	}
	return d
}

// offset returns the file's offset of the offset off in the paragraph's
// text.
func (p *commentPara) offset(off int) int {
	i := p.lineAt(off)
	return min(p.lines[i].start+off-p.at[i], p.lines[i].end)
}

// headerBefore returns the last header convention before the offset off,
// or nil.
func (d *goDoc) headerBefore(off int) *header {
	var h *header
	for i := range d.headers {
		if d.headers[i].off < off {
			h = &d.headers[i]
		}
	}
	return h
}

// goChecker checks and resolves the citations of one Go file, which name
// names as given.
type goChecker struct {
	ctx  context.Context
	c    *Checker
	m    mode
	name string
	doc  *goDoc
}

// candidate is a file a continuation may cite: a path a link or a resolved
// citation gives, or a name written.
type candidate struct{ name, path string }

// refOf returns the ref of the plain citation ps.items[ii], at the offset
// off in the file, or "" when no rule gives one, and records on the item
// which rule did. The rules, nearest first: "at <tag>" after it, past any
// citations listed with it, or "at both tags", which is v19.2.6; its own
// [S] or [T] marker; the last marker or tag in its clause before it, unless
// the clause names another release too and that one is not right before
// it, which leaves it with none; the one release the words before the "("
// it opens name; for a continuation, the ref of the citation it is listed
// after, blanks, commas, "and" or "or" alone between them, but not across
// that citation's own "at <tag>"; and the file's header convention, which,
// when it names a file, holds only if the citation turns out to be of that
// file.
func (g *goChecker) refOf(ps *paraScan, ii, off int) string {
	it, norm := ps.items[ii], ps.norm
	if t, both := tagAfter(norm, it.pos+1); both {
		it.both, it.rule = true, "at both tags"
		return markTags["S"]
	} else if t != "" {
		it.rule = "at <tag>"
		return t
	}
	if it.mark != "" {
		it.rule = "its marker"
		return markTags[it.mark]
	}
	var prev *goItem
	from := 0
	for k := ii - 1; k >= 0; k-- {
		if kind := ps.items[k].kind; kind == linkItem || kind == fileItem || kind == contItem {
			prev, from = ps.items[k], ps.items[k].pos+1
			break
		}
	}
	ownTag := false
	if loc := reAtTag.FindStringIndex(norm[from:it.pos]); loc != nil {
		from += loc[1]
		ownTag = prev != nil
	}
	before := norm[from:it.pos]
	clause := from
	if cut := max(strings.LastIndex(before, "("), strings.LastIndex(before, ";"), strings.LastIndex(before, ". ")); cut >= 0 {
		clause += cut + 1
	}
	if ms := reTag.FindAllStringSubmatchIndex(norm[clause:it.pos], -1); ms != nil {
		m := ms[len(ms)-1]
		tied := strings.TrimSpace(norm[clause+m[1]:it.pos]) == ""
		it.rule, it.releases = "clause", releasesNamed(norm[clause:it.pos])
		if len(it.releases) > 1 && !tied {
			return ""
		}
		if m[2] < 0 {
			return norm[clause+m[4] : clause+m[5]]
		}
		if tied {
			it.marked, it.marker = true, clause+m[0]
		}
		return markTags[norm[clause+m[2]:clause+m[3]]]
	}
	if t, markAt := releaseBeforeParen(norm, it.pos); t != "" {
		if markAt >= 0 {
			it.marked, it.marker = true, markAt
		}
		it.rule = `the release before "("`
		return t
	}
	if it.kind == contItem && prev != nil && prev.ref != "" && !ownTag && reListed.MatchString(before) {
		it.marked, it.marker, it.fromHeader, it.scope = prev.marked, prev.marker, prev.fromHeader, prev.scope
		it.rule = "listed after " + prev.text
		return prev.ref
	}
	if h := g.doc.headerBefore(off); h != nil {
		it.fromHeader, it.scope, it.rule = true, h.named, "header"
		return h.tag
	}
	return ""
}

// releasesNamed returns the releases s names, one tag each: those its tags
// and markers name, then those only its codenames name.
func releasesNamed(s string) []string {
	var tags, majors []string
	add := func(t string) {
		if m := majorOf(t); !slices.Contains(majors, m) {
			majors, tags = append(majors, m), append(tags, t)
		}
	}
	for _, m := range reTag.FindAllStringSubmatch(s, -1) {
		add(cmp.Or(markTags[m[1]], m[2]))
	}
	for _, c := range reCodename.FindAllString(s, -1) {
		add(codenameTags[strings.ToLower(c)])
	}
	return tags
}

// releaseBeforeParen returns the release named by the words before the "("
// that a citation at pos in norm opens, blanks aside, back to the last
// citation, "(", ")", ". ", ";" or ",", an "at <tag>" just after that
// citation left to it, when they name exactly one, by tag, by marker or by
// codename, as "radosgw at v19.2.6 lets it through (:228)" does; and, when
// that release is a lone marker right before the "(", the marker's offset,
// or -1.
func releaseBeforeParen(norm string, pos int) (tag string, markAt int) {
	j := pos
	for j > 0 && norm[j-1] == ' ' {
		j--
	}
	if j == 0 || norm[j-1] != '(' {
		return "", -1
	}
	s := norm[:j-1]
	start := strings.LastIndexByte(s, citeMark) + 1
	if loc := reAtTag.FindStringIndex(s[start:]); start > 0 && loc != nil {
		start += loc[1]
	}
	start = max(start, strings.LastIndex(s, ". ")+1, strings.LastIndexAny(s, "();,")+1)
	words := s[start:]
	tags := map[string]string{}
	ambiguous := false
	name := func(t string) {
		m := majorOf(t)
		if prev, ok := tags[m]; ok && prev != t {
			ambiguous = true
		}
		tags[m] = t
	}
	markAt, marks := -1, 0
	for _, m := range reTag.FindAllStringSubmatchIndex(words, -1) {
		if m[2] >= 0 {
			name(markTags[words[m[2]:m[3]]])
			markAt, marks = start+m[0], marks+1
			continue
		}
		name(words[m[4]:m[5]])
	}
	for _, c := range reCodename.FindAllString(words, -1) {
		if t := codenameTags[strings.ToLower(c)]; tags[majorOf(t)] == "" {
			tags[majorOf(t)] = t
		}
	}
	if ambiguous || len(tags) != 1 {
		return "", -1
	}
	for _, t := range tags {
		tag = t
	}
	if marks != 1 || len(reCodename.FindAllString(words, -1)) > 0 || strings.TrimSpace(norm[markAt+3:j-1]) != "" {
		markAt = -1
	}
	return tag, markAt
}

// majorOf returns a tag's major version, "19" for v19.2.6.
func majorOf(tag string) string {
	m, _, _ := strings.Cut(strings.TrimPrefix(tag, "v"), ".")
	return m
}

// reListed is what may stand between a continuation and the citation it is
// listed after: blanks, commas, "and" and "or".
var reListed = regexp.MustCompile(`^[\s,]*(?:(?:and|or)[\s,]+)?$`)

var (
	// reCodename is a release named by its codename.
	reCodename = regexp.MustCompile(`(?i)\b(squid|tentacle)\b`)
	// codenameTags are the tags the markers name, by their release's
	// codename.
	codenameTags = map[string]string{"squid": markTags["S"], "tentacle": markTags["T"]}
)

// otherRelease returns a release the words before the placeholder at pos
// name, back to the last citation, ".", ";" or ",", as a tag, a marker or a
// codename, that is not ref's, or "": a citation whose ref only the header
// convention gives is not linked when the words that lead to it say it is at
// another release, as "as v20.2.4 drops it (:5028)" does.
func otherRelease(norm string, pos int, ref string) string {
	s := norm[:pos]
	if k := max(strings.LastIndex(s, ". "), strings.LastIndexAny(s, ";,"+string(citeMark))); k >= 0 {
		s = s[k+1:]
	}
	want := majorOf(ref)
	for _, m := range reTag.FindAllStringSubmatch(s, -1) {
		if t := cmp.Or(markTags[m[1]], m[2]); majorOf(t) != want {
			return t
		}
	}
	for _, m := range reCodename.FindAllString(s, -1) {
		if majorOf(codenameTags[strings.ToLower(m)]) != want {
			return m
		}
	}
	return ""
}

// tagAfter returns the tag of an "at <tag>" at k in norm, or after the
// citations that follow k, joined by blanks, commas, "and" or "or", or after
// a comma that closes them, and whether it is instead "at both tags".
func tagAfter(norm string, k int) (tag string, both bool) {
	for {
		if t, b := atTag(norm[k:]); t != "" || b {
			return t, b
		}
		j := k
		for j < len(norm) && (norm[j] == ' ' || norm[j] == ',') {
			j++
		}
		if j > k && strings.Contains(norm[k:j], ",") {
			if t, b := atTag(norm[j:]); t != "" || b {
				return t, b
			}
		}
		for _, w := range []string{"and ", "or "} {
			if strings.HasPrefix(norm[j:], w) {
				j += len(w)
				for j < len(norm) && norm[j] == ' ' {
					j++
				}
				break
			}
		}
		if j >= len(norm) || norm[j] != citeMark {
			return "", false
		}
		k = j + 1
	}
}

// reBoth is "at both tags", "at both releases", or "at both" ending its
// clause, right after a citation.
var reBoth = regexp.MustCompile("^[`\\s]*\\)?\\s*at both(?: (?:tags|releases)\\b|$|[).,;:])")

// atTag returns the tag of an "at <tag>", or "in" or "on" one, at the start
// of s, and whether s starts with "at both tags" instead.
func atTag(s string) (tag string, both bool) {
	if m := reAtTag.FindStringSubmatch(s); m != nil {
		return m[1], false
	}
	return "", reBoth.MatchString(s)
}

// candidates returns the files a continuation may cite, nearest first: the
// citations and file mentions before it in its comment, then those of the
// header convention before it.
func (g *goChecker) candidates(gi, pi, ii, off int) []candidate {
	var out []candidate
	seen := map[candidate]bool{}
	add := func(c candidate) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for p := pi; p >= 0; p-- {
		items := g.doc.groups[gi][p].items
		end := len(items)
		if p == pi {
			end = ii
		}
		for k := end - 1; k >= 0; k-- {
			switch it := items[k]; {
			case it.kind == linkItem, it.path != "":
				add(candidate{path: it.path})
			case it.kind == fileItem, it.kind == mentionItem:
				add(candidate{name: it.name})
			}
		}
	}
	if h := g.doc.headerBefore(off); h != nil {
		for _, n := range h.names {
			add(candidate{name: n})
		}
	}
	return out
}

// commit returns the commit ref names in the clone, or "" when it names
// none.
func (g *goChecker) commit(ref string) (string, error) {
	r, err := g.c.resolve(g.ctx, ref)
	return r.commit, err
}

// isCeph reports whether a plain citation cites ceph: whether the file it
// names, or for a continuation the nearest candidate, is a file in the clone
// at its ref or at a release a marker names. A continuation no candidate
// precedes cites ceph when a header convention says what a bare line number
// means. Without a clone, under --form-only, a file is ceph's when it is not
// a Go file and the citation has a ref.
func (g *goChecker) isCeph(it *goItem, cands []candidate, off int) (bool, error) {
	name := it.name
	if it.kind == contItem {
		switch {
		case len(cands) == 0:
			return g.doc.headerBefore(off) != nil && (g.m != formOnly || it.ref != ""), nil
		case cands[0].path != "" && g.m != formOnly:
			return true, nil
		}
		name = cmp.Or(cands[0].path, cands[0].name)
	}
	if g.m == formOnly {
		return it.ref != "" && path.Ext(name) != ".go", nil
	}
	refs := []string{markTags["S"], markTags["T"]}
	if it.ref != "" {
		refs = append([]string{it.ref}, refs...)
	}
	for _, ref := range refs {
		commit, err := g.commit(ref)
		if err != nil {
			return false, err
		}
		if commit == "" {
			continue
		}
		paths, err := g.c.filesNamed(g.ctx, commit, name)
		if err != nil {
			return false, err
		}
		if len(paths) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// resolve finds the path and ref a plain citation means and sets its link,
// or returns why it cannot.
func (g *goChecker) resolve(ps *paraScan, it *goItem, cands []candidate) (string, error) {
	switch {
	case it.ref == "" && len(it.releases) > 1:
		return fmt.Sprintf(`no ref: its clause names %s, and nothing ties one to it: write "at <tag>" after it`,
			strings.Join(it.releases, " and ")), nil
	case it.ref == "":
		return `no ref: no "at <tag>" follows it, no [S] or [T] marks it, and no comment above says what a bare line ` +
			`number means`, nil
	}
	if ps.hint != "" {
		return fmt.Sprintf("its paragraph names %s, whose files citecheck does not resolve: if the file is ceph's, "+
			"link it by hand", ps.hint), nil
	}
	if o := otherRelease(ps.norm, it.pos, it.ref); it.fromHeader && o != "" {
		return fmt.Sprintf(`the words before it name %s, but only the header convention gives it a ref, %s: `+
			`write "at <tag>" after it`, o, it.ref), nil
	}
	r, err := g.c.resolve(g.ctx, it.ref)
	if err != nil || r.problem != "" {
		return r.problem, err
	}
	ids := g.doc.cephIdents(nearby(ps.norm, it.pos))
	files := clauseFiles(ps, it)
	if it.kind == contItem && len(ids) == 0 && len(files) > 1 && !namedRightBefore(ps, it) {
		return fmt.Sprintf("its clause names %s, and no identifier beside it tells which it cites: link it by hand",
			strings.Join(files, " and ")), nil
	}
	var p, why string
	if it.kind == fileItem {
		p, why, err = g.fileOf(r.commit, it, candidate{name: it.name}, ids, true)
	} else {
		p, why, err = g.contOf(r.commit, it, cands, ids)
	}
	if err != nil || why != "" {
		return why, err
	}
	if it.scope != "" && path.Base(p) != path.Base(it.scope) {
		return fmt.Sprintf(`no ref: only the header convention gives one, %s, and it gives it to %s's lines, not %s's: `+
			`write "at <tag>" after it`, it.ref, path.Base(it.scope), path.Base(p)), nil
	}
	cite := Citation{Text: it.text, Form: Link, Ref: it.ref, Path: p, From: it.from, To: it.to}
	reason, err := g.c.Check(g.ctx, cite)
	if err != nil || reason != "" {
		return reason, err
	}
	if it.both {
		same, err := g.sameAtBoth(r.commit, p, it.from, it.to)
		if err != nil || !same {
			return fmt.Sprintf("it is cited at both tags, but lines %d-%d of %s are not the same at %s and %s: "+
				`write "at <tag>" after it`, it.from, it.to, p, markTags["S"], markTags["T"]), err
		}
	}
	u := citationURL(cite)
	if !readsBack(u, cite) {
		return fmt.Sprintf("the link %s would read back as another citation", u), nil
	}
	it.path, it.url = p, u
	slog.DebugContext(g.ctx, "resolved a citation", slog.String("file", g.name),
		slog.Int("line", ps.para.lines[ps.para.lineAt(it.start)].n), slog.String("citation", it.text),
		slog.String("ref", it.ref), slog.String("ref_from", it.rule), slog.String("path", p),
		slog.Any("identifiers", ids), slog.Any("clause_files", files), slog.Any("clause_releases", it.releases))
	return "", nil
}

// clauseFiles returns the base names of the files cited or named in a
// citation's clause before it, back to the last "(", ";" or ". ", in the
// order first named.
func clauseFiles(ps *paraScan, it *goItem) []string {
	s := ps.norm[:it.pos]
	start := max(strings.LastIndex(s, "("), strings.LastIndex(s, ";"), strings.LastIndex(s, ". ")) + 1
	var files []string
	for _, o := range ps.items {
		if o.pos >= it.pos {
			break
		}
		if o.pos < start || o.kind == urlItem {
			continue
		}
		if f := path.Base(cmp.Or(o.path, o.name)); f != "." && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files
}

// namedRightBefore reports whether a continuation's file is named right
// before it, blanks alone between, as a file named at the end of a comment
// line names the lines that open the next.
func namedRightBefore(ps *paraScan, it *goItem) bool {
	for _, o := range slices.Backward(ps.items) {
		if o.pos < it.pos {
			return o.kind == mentionItem && strings.TrimSpace(ps.norm[o.pos+1:it.pos]) == ""
		}
	}
	return false
}

// sameAtBoth reports whether lines from to to of path p are the same at
// commit, v19.2.6's, and at v20.2.4, so that a citation "at both tags" may
// be pinned to v19.2.6.
func (g *goChecker) sameAtBoth(commit, p string, from, to int) (bool, error) {
	other, err := g.commit(markTags["T"])
	if err != nil || other == "" {
		return false, err
	}
	var texts [2]string
	for i, c := range []string{commit, other} {
		obj, err := g.c.read(g.ctx, c, p)
		if err != nil || obj.kind != "blob" || to > obj.f.lines() {
			return false, err
		}
		texts[i] = obj.f.text[obj.f.lineStart(from):obj.f.lineEnd(to)]
	}
	return texts[0] == texts[1], nil
}

// fileOf returns the path at commit of the file a candidate names for a
// citation: its one path, the paths under src/rgw/ and src/cls/ preferred
// when it has several, or, when identifiers are named beside the
// citation, the one path whose cited lines hold one. With
// strict unset, as for a continuation's farther candidates, a candidate that
// fits no such path is passed over: p and why are both "". A path whose
// lines are not all in the file is returned under strict when it is the only
// one, for the link check to say so.
func (g *goChecker) fileOf(commit string, it *goItem, cd candidate, ids []string, strict bool) (p, why string, err error) {
	paths, err := g.pathsOf(commit, cd)
	if err != nil {
		return "", "", err
	}
	switch {
	case len(paths) == 0 && strict:
		return "", fmt.Sprintf("%s is no file at %s", cd.name, it.ref), nil
	case len(paths) == 0:
		return "", "", nil
	case len(ids) == 0 && len(paths) == 1:
		return paths[0], "", nil
	case len(ids) == 0:
		return "", fmt.Sprintf("%s is any of %s at %s, and no identifier beside it tells which", cd.name,
			strings.Join(paths, ", "), it.ref), nil
	}
	var held []string
	outside := false
	for _, p := range paths {
		st, err := g.holds(commit, p, it.from, it.to, ids, true)
		if err != nil {
			return "", "", err
		}
		switch st {
		case "ok":
			held = append(held, p)
		case "range":
			outside = true
		}
	}
	switch {
	case len(held) == 1:
		return held[0], "", nil
	case len(held) > 1:
		return "", fmt.Sprintf("%s is any of %s at %s, and the cited lines of more than one hold %s", cd.name,
			strings.Join(held, ", "), it.ref, strings.Join(ids, ", ")), nil
	case !strict:
		return "", "", nil
	case outside && len(paths) == 1:
		return paths[0], "", nil
	}
	return "", fmt.Sprintf("the cited lines of %s at %s, and the 3 either side, hold none of %s", strings.Join(paths, ", "),
		it.ref, strings.Join(ids, ", ")), nil
}

// pathsOf returns the paths at commit a candidate may be: a link's or a
// resolved citation's own, or those its name has, the ones under src/rgw/
// and src/cls/ when it has several and any is.
func (g *goChecker) pathsOf(commit string, cd candidate) ([]string, error) {
	if cd.path != "" {
		return []string{cd.path}, nil
	}
	paths, err := g.c.filesNamed(g.ctx, commit, cd.name)
	return preferRGW(paths), err
}

// contOf returns the path a continuation cites: the nearest candidate's when
// no identifier is named beside it, and otherwise the first path whose cited
// lines hold one, each tried once. The first path tried, the nearest
// candidate's, holds one by the cited lines, the 3 either side or the header
// of a block enclosing them; a farther one only by the lines and the 3
// either side, and only a strong identifier, one with an "_", a lower-case
// letter before a capital, or ten bytes or more, which a generic name such
// as a base class's is not. When none holds and the nearest file has fewer
// lines than are cited, that file is returned, for the link check to say so.
func (g *goChecker) contOf(commit string, it *goItem, cands []candidate, ids []string) (p, why string, err error) {
	if len(cands) == 0 {
		return "", "no citation or file name comes before it in its comment, and the header convention names no file", nil
	}
	if len(ids) == 0 {
		return g.fileOf(commit, it, cands[0], nil, true)
	}
	strong := strongIdents(ids)
	// tried holds each path tried, and held what was looked for in each.
	var tried, held []string
	outside, passed := "", false
	for _, cd := range cands {
		paths, err := g.pathsOf(commit, cd)
		if err != nil {
			return "", "", err
		}
		for _, p := range paths {
			if slices.Contains(tried, p) {
				continue
			}
			first := len(tried) == 0
			if !first && len(strong) == 0 {
				passed = true
				continue
			}
			check, enclosing := strong, false
			if first {
				check, enclosing = ids, true
			}
			tried = append(tried, p)
			held = append(held, fmt.Sprintf("of %s in %s", strings.Join(check, ", "), p))
			st, err := g.holds(commit, p, it.from, it.to, check, enclosing)
			if err != nil || st == "ok" {
				return p, "", err
			}
			if st == "range" && first {
				outside = p
			}
		}
	}
	switch {
	case outside != "":
		return outside, "", nil
	case len(tried) == 0:
		return "", fmt.Sprintf("%s is no file at %s", cands[0].name, it.ref), nil
	case passed:
		return "", fmt.Sprintf("the cited lines of %s at %s, its nearest file, and the 3 either side, hold none of %s, "+
			"and none of those is distinctive enough to take it to a farther file", tried[0], it.ref, strings.Join(ids, ", ")), nil
	}
	return "", fmt.Sprintf("the cited lines, and the 3 either side, hold none %s at %s", strings.Join(held, ", nor "),
		it.ref), nil
}

// strongIdents returns the identifiers distinctive enough to choose a file
// by: those with an "_", a lower-case letter before a capital, or ten bytes
// or more.
func strongIdents(ids []string) []string {
	var out []string
	for _, t := range ids {
		if strings.Contains(t, "_") || reCamel.MatchString(t) || len(t) >= 10 {
			out = append(out, t)
		}
	}
	return out
}

// holds returns how the lines from to to of path p at commit stand:
// "missing", "range" when they are not all in the file, "nocheck" when no
// identifier is named, and else "ok" when one of ids stands as a whole word
// in them, in the 3 lines either side, or, with enclosing set, in the header
// of a block that encloses them, a block as the symbol check reads one, and
// "unverified" when none does.
func (g *goChecker) holds(commit, p string, from, to int, ids []string, enclosing bool) (string, error) {
	obj, err := g.c.read(g.ctx, commit, p)
	switch {
	case err != nil:
		return "", err
	case obj.kind != "blob":
		return "missing", nil
	}
	f := obj.f
	n := f.lines()
	switch {
	case from < 1 || to > n || from > to:
		return "range", nil
	case len(ids) == 0:
		return "nocheck", nil
	}
	window := f.text[f.lineStart(max(1, from-3)):f.lineEnd(min(n, to+3))]
	for _, id := range ids {
		if hasWord(window, id) {
			return "ok", nil
		}
	}
	if !enclosing {
		return "unverified", nil
	}
	f.analyze()
	for _, b := range f.enclosing(from, to) {
		for _, id := range ids {
			if hasWord(f.code[b.head:b.open], id) {
				return "ok", nil
			}
		}
	}
	return "unverified", nil
}

// hasWord reports whether w stands in s as a whole word: no letter, digit
// or "_" on either side.
func hasWord(s, w string) bool {
	for i := 0; i <= len(s)-len(w); {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if (j == 0 || !isIdent(s[j-1])) && (j+len(w) == len(s) || !isIdent(s[j+len(w)])) {
			return true
		}
		i = j + 1
	}
	return false
}

// preferRGW keeps the paths under src/rgw/ and src/cls/ when there are
// several and any is.
func preferRGW(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	var rgw []string
	for _, p := range paths {
		if strings.HasPrefix(p, "src/rgw/") || strings.HasPrefix(p, "src/cls/") {
			rgw = append(rgw, p)
		}
	}
	if len(rgw) > 0 {
		return rgw
	}
	return paths
}

// nearby returns the text named beside the placeholder at pos in norm: from
// 220 bytes before it to 100 after, each widened to a blank. A citation is
// one placeholder byte, so that a second run, after --fix wrote the links,
// reads the same text.
func nearby(norm string, pos int) string {
	lo := 0
	if pos > 220 {
		lo = strings.LastIndexByte(norm[:pos-220], ' ') + 1
	}
	hi := len(norm)
	if from := pos + 1 + 100; from < len(norm) {
		if k := strings.IndexByte(norm[from:], ' '); k >= 0 {
			hi = from + k
		}
	}
	return norm[lo:hi]
}

// reSelector is a Go qualified identifier, pkg.Name.
var reSelector = regexp.MustCompile(`\b([a-z][A-Za-z0-9_]*)\.[A-Za-z_][A-Za-z0-9_]*`)

// cephIdents returns the identifiers in text that may name ceph's code:
// those idents finds, less the ones that name rgw-go's: an identifier the
// file's Go code writes, and one a package the file imports qualifies, as
// op.UserStore.
func (d *goDoc) cephIdents(text string) []string {
	text = reSelector.ReplaceAllStringFunc(text, func(s string) string {
		if pkg, _, _ := strings.Cut(s, "."); d.pkgs[pkg] {
			return strings.Repeat(" ", len(s))
		}
		return s
	})
	var out []string
	for _, t := range idents(text) {
		if !d.code[t] {
			out = append(out, t)
		}
	}
	return out
}

// idents returns the identifiers that could name code in text: every one in
// a code span, and outside them those with an "_", a lower-case letter
// before an upper-case one, or two capitals and a lower case letter first,
// less the stop words and versions.
func idents(text string) []string {
	var all []string
	for _, span := range reSpan.FindAllString(text, -1) {
		all = append(all, reIdent.FindAllString(span[1:len(span)-1], -1)...)
	}
	for _, tok := range reIdent.FindAllString(reSpan.ReplaceAllString(text, " "), -1) {
		if strings.Contains(tok, "_") || reCamel.MatchString(tok) || reAcronym.MatchString(tok) {
			all = append(all, tok)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range all {
		if stopWords[strings.ToLower(t)] || seen[t] || reNumber.MatchString(t) {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
