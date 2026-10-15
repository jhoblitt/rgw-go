package citecheck

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// releaseWord is a word in a scope that names a release or a ref.
type releaseWord struct {
	// at and end are its offsets in its paragraph's norm.
	at, end int
	// name is the tag it names, or, for a ref no scope gives, the word as
	// written; tag reports which, and codename whether a codename names it.
	name          string
	tag, codename bool
	// owner is the norm offset of the citation it is the own release of, or
	// -1: a link's ref, an "at <tag>" right after a citation, or a tag or
	// marker right before one.
	owner int
}

// ownedBy reports whether a release word is the own release of a citation
// other than the one at self.
func (w releaseWord) ownedBy(self int) bool { return w.owner >= 0 && w.owner != self }

var (
	// reOtherRef is a ref no scope can give a citation: a branch, a commit
	// id, a release by a codename the markers do not name, or several
	// releases at once.
	reOtherRef = regexp.MustCompile(`(?i)\b(?:origin/)?(?:main|master)\b|\b[0-9a-f]{7,40}\b|` +
		`\b(?:reef|quincy|pacific|octopus|nautilus|mimic|luminous|umbrella)\b|\bboth (?:tags|releases)\b|` +
		`\b(?:each|every|either) (?:tag|release)\b|\ball three\b`)
	// reOwnedAfter is what stands between a citation and the "at <tag>"
	// that is its own, a comma among it as tagAfter reads one.
	reOwnedAfter = regexp.MustCompile("\x01[`\\s]*\\)?\\s*,?\\s*(?:at|in|on) $")
	// reOwnedBefore is what stands between a tag or a marker and the
	// citation it is tied to.
	reOwnedBefore = regexp.MustCompile(`^[\s(]*\x01`)
)

// releaseWords returns the words of a paragraph's norm[lo:hi] that name a
// release or a ref, in order: tags, markers, codenames, and the refs
// reOtherRef names, a commit id only when it holds a digit and a letter; and
// the ref of each link there, as the link's own.
func releaseWords(ps *paraScan, lo, hi int) []releaseWord {
	norm := ps.norm
	s := norm[lo:hi]
	var ws []releaseWord
	for _, it := range ps.items {
		if it.kind == linkItem && lo <= it.pos && it.pos < hi {
			ws = append(ws, releaseWord{
				at: it.pos, end: it.pos + 1, name: it.ref, owner: it.pos,
				tag: reTagWord.MatchString(it.ref) && len(reTagWord.FindString(it.ref)) == len(it.ref),
			})
		}
	}
	add := func(at, end int, name string, tag, codename bool) {
		w := releaseWord{at: lo + at, end: lo + end, name: name, tag: tag, codename: codename, owner: -1}
		back := max(0, w.at-16)
		if loc := reOwnedAfter.FindStringIndex(norm[back:w.at]); loc != nil {
			w.owner = back + loc[0]
		} else if loc := reOwnedBefore.FindStringIndex(norm[w.end:]); loc != nil {
			w.owner = w.end + loc[1] - 1
		}
		ws = append(ws, w)
	}
	for _, m := range reTag.FindAllStringSubmatchIndex(s, -1) {
		if m[2] >= 0 {
			add(m[0], m[1], markTags[s[m[2]:m[3]]], true, false)
		} else {
			add(m[0], m[1], s[m[4]:m[5]], true, false)
		}
	}
	for _, m := range reCodename.FindAllStringIndex(s, -1) {
		add(m[0], m[1], codenameTags[strings.ToLower(s[m[0]:m[1]])], true, true)
	}
	for _, m := range reOtherRef.FindAllStringIndex(s, -1) {
		w := s[m[0]:m[1]]
		if hexOnly(w) && (!strings.ContainsAny(w, "0123456789") || strings.Trim(w, "0123456789") == "") {
			continue
		}
		add(m[0], m[1], strings.ToLower(w), false, false)
	}
	slices.SortFunc(ws, func(a, b releaseWord) int { return a.at - b.at })
	return ws
}

// hexOnly reports whether w is hex digits alone.
func hexOnly(w string) bool { return strings.Trim(w, "0123456789abcdefABCDEF") == "" }

// decide returns the ref a scope's release words give a citation at pos in
// the same norm, or, for a scope before it, at -1, and whether the scope
// decides, as one naming any release does: it gives the one release it names
// when that is a tag, named before the citation, by a codename only when
// codenames is set, and not another citation's own; otherwise the citation
// has no ref, and why says so.
func decide(it *goItem, ws []releaseWord, scope string, pos int, codenames bool) (string, bool) {
	if len(ws) == 0 {
		return "", false
	}
	var names []string
	owned, before := false, false
	for _, w := range ws {
		if !slices.Contains(names, w.name) {
			names = append(names, w.name)
		}
		owned = owned || w.ownedBy(pos)
		before = before || pos < 0 || w.at < pos
	}
	it.rule, it.releases = scope, names
	w := ws[0]
	switch {
	case len(names) > 1:
		it.why = fmt.Sprintf(`no ref: its %s names %s, and nothing ties one to it: write "at <tag>" after it`, scope,
			strings.Join(names, " and "))
	case owned:
		it.why = fmt.Sprintf(`no ref: its %s names %s as another citation's own: write "at <tag>" after it`, scope, w.name)
	case !w.tag:
		it.why = fmt.Sprintf("no ref: its %s names %s, which is no tag: link it by hand", scope, w.name)
	case w.codename && !codenames:
		it.why = fmt.Sprintf(`no ref: its %s names its release only by a codename: write "at <tag>" after it`, scope)
	case !before:
		it.why = fmt.Sprintf(`no ref: its %s names %s only after it: write "at <tag>" after it`, scope, w.name)
	default:
		it.why = ""
		return w.name, true
	}
	return "", true
}

// noScopeRef is why a plain markdown citation that no rule or scope gives a
// ref has none.
const noScopeRef = `no ref: no "at <tag>" follows it, no [S] or [T] marks it, and neither its sentence, the list ` +
	`items and the paragraph that lead in to it, nor its section's heading names a release`

// scopeRef returns the ref the scopes around a plain markdown citation give
// it, and whether one decided, nearest first: its sentence, or in a table
// its cell; the other cells of its row, and the cell its column's header
// row holds, each only when the cell names the release plainly; the list
// items it is nested in and the paragraph that leads in to its list or
// table; and its section's heading, which gives only a tag. The first scope
// that names any release decides. A lead-in that names two releases or more
// may state a convention, such as "a pair of line numbers is v19.2.6's then
// v20.2.4's", that its sentence's release does not override: under one, the
// sentence gives no ref when it names its release only by a codename, or
// when the parenthesis that holds the citation holds a ";", which separates
// such a pair. A lead-in that states the pair convention gives no ref
// when its list is not regular, and a citation in that list that a scope
// leaves with no ref is told why the convention is not applied.
func (d *mdDoc) scopeRef(ps *paraScan, it *goItem) (string, bool) {
	ref, ok := d.scopeDecide(ps, it)
	if pl := d.pairOf[it]; ok && ref == "" && pl != nil && pl.why != "" {
		it.why = pl.decline(pl.why)
	}
	return ref, ok
}

// scopeDecide returns the ref the scopes around a plain markdown citation
// give it, and whether one decided, as scopeRef describes.
func (d *mdDoc) scopeDecide(ps *paraScan, it *goItem) (string, bool) {
	at := d.at[ps]
	gi, pi := at[0], at[1]
	block := d.blocks[gi][pi]
	leads := d.leadIns(gi, pi)
	lo, hi := sentence(ps.norm, it.pos, block.kind == mdRow)
	scope := "sentence"
	if block.kind == mdRow {
		scope = "table cell"
	}
	ws := releaseWords(ps, lo, hi)
	ref, ok := decide(it, ws, scope, it.pos, true)
	if ok && ref != "" {
		ref = changedIn(it, ps.norm, ws, ref, scope)
	}
	if !ok && block.kind == mdRow {
		ref, ok = d.rowRef(gi, pi, it)
	}
	if ok {
		if ref != "" && (byCodename(ws, ref) || inPairedParen(ps.norm, it.pos)) && d.pairGuard(gi, leads, it) {
			return "", true
		}
		return ref, true
	}
	pl := d.pairOf[it]
	nested := pl != nil && pl.why != "" && slices.Contains(leads, d.at[pl.lead][1])
	for _, k := range leads {
		lead := d.groups[gi][k]
		if why := d.pairWhy(it, lead); why != "" {
			it.rule, it.why = "lead-in", why
			return "", true
		}
		if ref, ok := decide(it, releaseWords(lead, 0, len(lead.norm)), "lead-in", -1, true); ok {
			if ref != "" && nested {
				// A lead-in inside a pair list that is not regular gives no
				// ref: the pair lead-in above it says otherwise.
				it.rule, it.why = "lead-in", pl.decline(pl.why)
				return "", true
			}
			return ref, true
		}
	}
	if d.blocks[gi][0].kind == mdHeading && pi > 0 {
		h := d.groups[gi][0]
		hw := releaseWords(h, 0, len(h.norm))
		if ref, ok := decide(it, hw, "section's heading", -1, false); ok {
			if ref != "" {
				ref = changedIn(it, h.norm, hw, ref, "section's heading")
			}
			return ref, true
		}
	}
	it.why = noScopeRef
	return "", false
}

// reChangeWord is what stands right before a release that names the
// release a behavior changed in rather than the one a citation's lines were
// read at.
var reChangeWord = regexp.MustCompile(`(?i)(?:\b(fixed in|before|until|since)\s+|\b(pre)-)[*_]*$`)

// changedIn returns ref, the release a sentence or a heading gives a
// citation, or "" when a word for it in norm follows "fixed in", "before",
// "until", "since" or "pre-", and why then says so.
func changedIn(it *goItem, norm string, ws []releaseWord, ref, scope string) string {
	for _, w := range ws {
		if m := reChangeWord.FindStringSubmatch(norm[max(0, w.at-16):w.at]); m != nil {
			it.why = fmt.Sprintf(`no ref: its %s names %s after %q, which says when something changed, not the `+
				`release its lines are read at: write "at <tag>" after it`, scope, ref,
				strings.ToLower(cmp.Or(m[1], m[2]+"-")))
			return ""
		}
	}
	return ref
}

// byCodename reports whether release words name ref only by a codename.
func byCodename(ws []releaseWord, ref string) bool {
	return !slices.ContainsFunc(ws, func(w releaseWord) bool { return w.name == ref && !w.codename })
}

// inPairedParen reports whether the innermost parenthesis around pos in norm
// holds a ";".
func inPairedParen(norm string, pos int) bool {
	lo, depth := -1, 0
	for i := pos - 1; i >= 0 && lo < 0; i-- {
		switch norm[i] {
		case ')':
			depth++
		case '(':
			if depth == 0 {
				lo = i
			}
			depth--
		}
	}
	if lo < 0 {
		return false
	}
	depth = 0
	for i := lo + 1; i < len(norm); i++ {
		switch norm[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return false
			}
			depth--
		case ';':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// pairGuard reports whether one of a citation's lead-ins names two or more
// releases or refs, and when one does records why the citation has no ref.
func (d *mdDoc) pairGuard(gi int, leads []int, it *goItem) bool {
	for _, k := range leads {
		lead := d.groups[gi][k]
		if why := d.pairWhy(it, lead); why != "" {
			it.rule, it.why = "lead-in", why
			return true
		}
		var names []string
		for _, w := range releaseWords(lead, 0, len(lead.norm)) {
			if !slices.Contains(names, w.name) {
				names = append(names, w.name)
			}
		}
		if len(names) > 1 {
			it.rule, it.releases = "lead-in", names
			it.why = fmt.Sprintf(`no ref: its lead-in names %s, and nothing ties one to it: write "at <tag>" after it`,
				strings.Join(names, " and "))
			return true
		}
	}
	return false
}

// reSentenceEnd is the end of a sentence: a "." and a blank, with the "*"
// or "_" that close an emphasis, as "**Title.** " does, between them.
var reSentenceEnd = regexp.MustCompile(`\.[*_]*\s`)

// sentenceStart returns the offset in norm of the start of the sentence that
// holds pos.
func sentenceStart(norm string, pos int) int {
	ms := reSentenceEnd.FindAllStringIndex(norm[:pos], -1)
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1][1]
}

// sentence returns the bounds in norm of the sentence that holds pos, or in
// a table row the cell, between the unescaped "|" on either side.
func sentence(norm string, pos int, row bool) (lo, hi int) {
	if row {
		cells := cellBounds(norm)
		for _, c := range cells {
			if c[0] <= pos && pos < c[1] {
				return c[0], c[1]
			}
		}
		return 0, len(norm)
	}
	hi = len(norm)
	if loc := reSentenceEnd.FindStringIndex(norm[pos:]); loc != nil {
		hi = pos + loc[0] + 1
	}
	return sentenceStart(norm, pos), hi
}

// ownCell returns, for a citation at pos in a table row, the offset its cell
// starts at and the row's norm with every other cell blanked, so that the
// rules a Go comment is read by read the cell as a paragraph of its own;
// and for any other, 0 and the paragraph's norm.
func (d *mdDoc) ownCell(ps *paraScan, pos int) (lo int, norm string) {
	at := d.at[ps]
	if d.blocks[at[0]][at[1]].kind != mdRow {
		return 0, ps.norm
	}
	lo, hi := sentence(ps.norm, pos, true)
	return lo, strings.Repeat(" ", lo) + ps.norm[lo:hi] + strings.Repeat(" ", len(ps.norm)-hi)
}

// cellBounds returns the bounds of a table row's cells in its norm, split at
// each "|" no backslash escapes, the text before the first and after the last
// left out when blank.
func cellBounds(norm string) [][2]int {
	var cuts []int
	for i := 0; i < len(norm); i++ {
		switch norm[i] {
		case '\\':
			i++
		case '|':
			cuts = append(cuts, i)
		}
	}
	var cells [][2]int
	last := 0
	for _, c := range append(cuts, len(norm)) {
		if s := norm[last:c]; last != 0 && c != len(norm) || strings.TrimSpace(s) != "" {
			cells = append(cells, [2]int{last, c})
		}
		last = c + 1
	}
	return cells
}

// rePlainRelease is a table cell that names a release plainly: a tag, a
// marker, a codename, or a codename and its tag in parentheses, once "*",
// "`" and blanks are dropped.
var rePlainRelease = regexp.MustCompile(`^(?i:(squid|tentacle))?(?:\(?(v\d+\.\d+\.\d+|\[[ST]\])\)?)?$`)

// plainRelease returns the release a cell's text names plainly, or "".
func plainRelease(cell string) string {
	t := strings.Map(func(r rune) rune {
		if r == '*' || r == '`' || r == ' ' {
			return -1
		}
		return r
	}, cell)
	m := rePlainRelease.FindStringSubmatch(t)
	switch {
	case m == nil || t == "":
		return ""
	case strings.HasPrefix(m[2], "["):
		return markTags[m[2][1:2]]
	case m[2] != "":
		return m[2]
	}
	return codenameTags[strings.ToLower(m[1])]
}

// rowRef returns the ref a table row's other cells, and the cell of its
// header row above the citation's, give a citation in the row, and whether
// one decided: each gives the release it names only when it names it
// plainly, and one that names a release otherwise leaves the citation with
// none.
func (d *mdDoc) rowRef(gi, pi int, it *goItem) (string, bool) {
	ps := d.groups[gi][pi]
	cells := cellBounds(ps.norm)
	col := slices.IndexFunc(cells, func(c [2]int) bool { return c[0] <= it.pos && it.pos < c[1] })
	first := pi
	for first > 0 && d.blocks[gi][first-1].kind == mdRow {
		first--
	}
	var ws []releaseWord
	plain, column := true, ""
	for k, c := range cells {
		if k == col {
			continue
		}
		if cw := releaseWords(ps, c[0], c[1]); len(cw) > 0 {
			ws = append(ws, cw...)
			plain = plain && plainRelease(ps.norm[c[0]:c[1]]) != ""
			if head := d.columnHead(gi, first, pi, k); column == "" && !reReleaseColumn.MatchString(head) {
				column = fmt.Sprintf("in a column whose header, %q, names no release column", head)
				if first == pi {
					column = "in a cell with no header row above it"
				}
			}
		}
	}
	switch {
	case len(ws) > 0 && plain && column != "":
		it.rule, it.releases = "table row", []string{ws[0].name}
		it.why = fmt.Sprintf(`no ref: its table row names %s %s: write "at <tag>" after it`, ws[0].name, column)
		return "", true
	case len(ws) > 0:
		return decidePlain(it, ws, plain, "table row")
	}
	if first == pi || col < 0 {
		return "", false
	}
	header := d.groups[gi][first]
	hc := cellBounds(header.norm)
	if col >= len(hc) {
		return "", false
	}
	cw := releaseWords(header, hc[col][0], hc[col][1])
	return decidePlain(it, cw, plainRelease(header.norm[hc[col][0]:hc[col][1]]) != "", "table column's header")
}

// reReleaseColumn is a table header cell that names a column of releases.
var reReleaseColumn = regexp.MustCompile(`(?i)^(?:release|tag|version|ref)s?$`)

// columnHead returns the text of the header cell above column k of a table
// whose header row is section gi's paragraph first and whose row pi is
// read, "*" and "`" dropped and trimmed, or "" when the row is the header.
func (d *mdDoc) columnHead(gi, first, pi, k int) string {
	if first == pi {
		return ""
	}
	header := d.groups[gi][first]
	hc := cellBounds(header.norm)
	if k >= len(hc) {
		return ""
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '*' || r == '`' {
			return -1
		}
		return r
	}, header.norm[hc[k][0]:hc[k][1]]))
}

// decidePlain decides as decide does, but leaves a citation with no ref when
// the cells that name a release do not all name it plainly.
func decidePlain(it *goItem, ws []releaseWord, plain bool, scope string) (string, bool) {
	if len(ws) > 0 && !plain {
		it.rule, it.releases = scope, []string{ws[0].name}
		it.why = fmt.Sprintf(`no ref: its %s names %s in a cell that holds more than the release: write "at <tag>" after it`,
			scope, ws[0].name)
		return "", true
	}
	return decide(it, ws, scope, -1, true)
}

// leadIns returns the indexes, nearest first, of the paragraphs that lead in
// to section gi's paragraph pi: each list item it is nested in, and the
// paragraph right before each list or table it is in, when that paragraph
// ends with ":". A list or table ends, at its own level, at another list or
// table there before it, so a paragraph before that one leads in to neither.
func (d *mdDoc) leadIns(gi, pi int) []int {
	blocks := d.blocks[gi]
	var out []int
	thr, item := blocks[pi].indent, blocks[pi].kind == mdItem || blocks[pi].kind == mdRow
	// table reports whether what the walk is in at thr is a table, own
	// whether it is still on the rows of the citation's own table, and ended
	// whether another list or table at thr has ended what it is in.
	table := blocks[pi].kind == mdRow
	own, ended := table, false
	for k := pi - 1; k >= 0; k-- {
		b := blocks[k]
		if own && b.kind == mdRow && b.indent == thr {
			continue
		}
		own = false
		switch {
		case b.kind == mdHeading:
			return out
		case b.indent > thr:
			continue
		case b.kind == mdItem && b.indent < thr:
			out = append(out, k)
			thr, item, table, ended = b.indent, true, false, false
		case b.kind == mdRow, b.kind == mdItem && item && table:
			ended = true
		case ended && b.indent == thr, b.kind == mdItem && item, b.kind == mdItem && thr > 0,
			b.kind == mdText && b.indent == thr && !item:
			continue
		case b.kind == mdItem:
			return out
		default:
			if item && !ended && strings.HasSuffix(strings.TrimSpace(d.groups[gi][k].norm), ":") {
				out = append(out, k)
			}
			if thr, item = b.indent, false; thr == 0 {
				return out
			}
		}
	}
	return out
}

// mdUntied returns ref, the release the clause rule or the "(" rule found
// for a citation, or, in markdown, "" when the release is not tied to the
// citation, right before it, and the words before the citation, back to the
// start of its sentence, the last ";", or the citation before it and that
// one's own "at <tag>", name another release or ref outside other
// citations' own, as "the same at v20.2.4, where it is byte-identical to
// v19.2.6 (" and "main `:96`" do; or, tied or not, when its sentence names
// the release after a word changedIn reads as naming when something
// changed; why then says so.
func (g *goChecker) mdUntied(ps *paraScan, it *goItem, ref string, tied bool) string {
	if g.md == nil {
		return ref
	}
	cellLo, norm := g.md.ownCell(ps, it.pos)
	var mine []releaseWord
	for _, w := range releaseWords(ps, max(cellLo, sentenceStart(norm, it.pos)), it.pos) {
		if w.name == ref && !w.ownedBy(it.pos) {
			mine = append(mine, w)
		}
	}
	if changedIn(it, norm, mine, ref, "sentence") == "" {
		return ""
	}
	if tied {
		return ref
	}
	lo := max(cellLo, sentenceStart(norm, it.pos), strings.LastIndexByte(norm[:it.pos], ';')+1)
	for _, o := range slices.Backward(ps.items) {
		if o.pos < it.pos && (o.kind == linkItem || o.kind == fileItem || o.kind == contItem) {
			if from := o.pos + 1; from > lo {
				lo = from
				if loc := reAtTag.FindStringIndex(norm[from:it.pos]); loc != nil {
					lo += loc[1]
				}
			}
			break
		}
	}
	names := []string{ref}
	ws := releaseWords(ps, lo, it.pos)
	for _, w := range ws {
		if !w.ownedBy(it.pos) && !slices.Contains(names, w.name) {
			names = append(names, w.name)
		}
	}
	if len(names) == 1 {
		at := g.md.at[ps]
		if (byCodename(ws, ref) || inPairedParen(ps.norm, it.pos)) && g.md.pairGuard(at[0], g.md.leadIns(at[0], at[1]), it) {
			return ""
		}
		return ref
	}
	it.releases = names
	it.why = fmt.Sprintf(`no ref: the words before it name %s, and nothing ties one to it: write "at <tag>" after it`,
		strings.Join(names, " and "))
	return ""
}

// clauseRefs returns the refs of the citations before it in its clause,
// back to the last "(", ";" or ". ", in the order they come.
func clauseRefs(ps *paraScan, it *goItem) []string {
	s := ps.norm[:it.pos]
	start := max(strings.LastIndex(s, "("), strings.LastIndex(s, ";"), strings.LastIndex(s, ". ")) + 1
	var refs []string
	for _, o := range ps.items {
		switch {
		case o.pos >= it.pos:
			return refs
		case o.pos < start, o.kind != linkItem && o.kind != fileItem && o.kind != contItem, o.ref == "",
			slices.Contains(refs, o.ref):
		default:
			refs = append(refs, o.ref)
		}
	}
	return refs
}

// paraCandidates returns the files a markdown continuation is looked for
// in: when its nearest candidate is named in its own paragraph, that one and
// past it only the others its paragraph names before it, since an
// identifier beside it can stand by chance in a file a paragraph above
// names; otherwise every candidate.
func paraCandidates(ps *paraScan, it *goItem, cands []candidate) []candidate {
	if len(cands) < 2 {
		return cands
	}
	in := map[candidate]bool{}
	for _, o := range ps.items {
		if o.pos >= it.pos {
			break
		}
		switch {
		case o.kind == linkItem, o.path != "":
			in[candidate{path: o.path}] = true
		case o.kind == fileItem, o.kind == mentionItem:
			in[candidate{name: o.name}] = true
		}
	}
	if !in[cands[0]] {
		return cands
	}
	out := cands[:1:1]
	for _, c := range cands[1:] {
		if in[c] {
			out = append(out, c)
		}
	}
	return out
}

// contGuard returns why a markdown continuation is not resolved, or "": it
// has no identifier beside it and no file before it in its own paragraph,
// so that only a file named in another paragraph would be taken for it.
func contGuard(ps *paraScan, it *goItem, ids []string) string {
	if it.kind != contItem || len(ids) > 0 {
		return ""
	}
	for _, o := range ps.items {
		if o.pos >= it.pos {
			break
		}
		if o.kind == linkItem || o.kind == fileItem || o.kind == mentionItem || o.path != "" {
			return ""
		}
	}
	return "no file is named before it in its paragraph, and no identifier beside it confirms the file named before " +
		"that: link it by hand"
}

// reAtWord is an "at", "on" or "in" and the word after it, right after a
// citation.
var reAtWord = regexp.MustCompile("^[`\\s]*\\)?\\s*(at|on|in) ([^\\s,;)]+)")

// atOther returns the "at <ref>", "on <ref>" or "in <ref>" right after the
// citation whose placeholder ends at k in norm, or after the citations listed
// after it, when the ref is no tag: a branch, a commit id, a version that is
// not a tag, or "all"; or "".
func atOther(norm string, k int) string {
	for {
		if m := reAtWord.FindStringSubmatch(norm[k:]); m != nil {
			w := strings.TrimRight(m[2], ".:")
			if otherRefWord(m[1], w) {
				return m[1] + " " + w
			}
			return ""
		}
		j := k
		for j < len(norm) && (norm[j] == ' ' || norm[j] == ',') {
			j++
		}
		for _, w := range []string{"and ", "or "} {
			if strings.HasPrefix(norm[j:], w) {
				j += len(w)
				break
			}
		}
		for j < len(norm) && norm[j] == ' ' {
			j++
		}
		if j >= len(norm) || norm[j] != citeMark {
			return ""
		}
		k = j + 1
	}
}

var (
	reVersion = regexp.MustCompile(`^v?\d+(?:\.\d+)*$`)
	reCommit  = regexp.MustCompile(`^[0-9a-f]{7,40}(?:[\^~]\d*)*$`)
)

// otherRefWord reports whether the word after an "at", "on" or "in" names a
// ref that is no tag.
func otherRefWord(prep, w string) bool {
	lw := strings.ToLower(w)
	switch {
	case reTagWord.MatchString(w) && len(reTagWord.FindString(w)) == len(w), lw == "both":
		return false
	case lw == "main", lw == "master", lw == "origin/main", lw == "upstream":
		return true
	case isCommitWord(lw):
		return true
	case prep == "at" && (reVersion.MatchString(w) || lw == "all" || strings.Contains(w, "/")):
		return true
	case prep == "in" && (reVersion.MatchString(w) || lw == "boost"):
		return true
	}
	return false
}

// isBranchRef reports whether an "at", "on" or "in" and the word after it
// name the branch the docs cite ceph's code on: "on main", "at main", and
// "at" or "on" origin/main.
func isBranchRef(prep, w string) bool {
	return (prep == "at" || prep == "on") && (w == "main" || w == "origin/main")
}

// isCommitWord reports whether a lower-cased word is a commit id, with a
// digit and a letter in it, and the "^" or "~" suffixes git reads.
func isCommitWord(lw string) bool {
	return reCommit.MatchString(lw) && strings.ContainsAny(lw, "0123456789") && strings.Trim(lw, "0123456789^~") != ""
}

// mdIsCeph reports whether a plain markdown citation cites ceph: whether the
// list of ceph's files, or, with a clone, the clone at its ref, at the
// commit or the main branch an "at <ref>" or "on main" after it names, or at
// a release a marker names,
// has the file it names or, for a continuation, its nearest candidate's; a
// continuation that follows a link cites the link's file. A file whose name
// is the path of one of rgw-go's from rgw-go's root is rgw-go's, never
// ceph's. Under --form-only, a citation of a file the list lacks, tied to a
// tag or commit the list was not made at, is taken for ceph's, offList
// recording the ref, since nothing tells it is not, unless its paragraph
// names another repository, as rook or go-ceph. It records the paths found,
// which the reason it fails by names.
func (g *goChecker) mdIsCeph(it *goItem, cands []candidate, off int) (bool, error) {
	name := it.name
	if it.kind == contItem {
		switch {
		case len(cands) == 0:
			return g.doc.headerBefore(off) != nil && (g.m != formOnly || it.ref != ""), nil
		case cands[0].path != "":
			it.cephPaths = []string{cands[0].path}
			return true, nil
		}
		name = cands[0].name
	}
	if g.md.ownPath(name) {
		return false, nil
	}
	if it.cephPaths = g.files.named(name); len(it.cephPaths) > 0 {
		return true, nil
	}
	if g.m == formOnly {
		tied := it.ref
		if isCommitWord(strings.ToLower(it.atRef)) {
			tied = cmp.Or(tied, it.atRef)
		}
		if tied != "" && !slices.Contains(g.files.refs, tied) && g.md.itemPara[it].hint == "" {
			it.offList = tied
			return true, nil
		}
		return false, nil
	}
	for _, ref := range []string{it.ref, it.atRef, markTags["S"], markTags["T"]} {
		if ref == "" {
			continue
		}
		var commit string
		var err error
		if ref == it.atRef && isBranchRef("at", ref) {
			commit, err = g.c.branchTip(g.ctx, ref)
		} else {
			commit, err = g.commit(ref)
		}
		switch {
		case err != nil:
			return false, err
		case commit == "":
			continue
		}
		paths, err := g.c.filesNamed(g.ctx, commit, name)
		if err != nil {
			return false, err
		}
		if len(paths) > 0 {
			it.cephPaths = paths
			return true, nil
		}
	}
	return false, nil
}

// notLink returns why a plain citation of ceph fails when it is not
// resolved: it is not a link, and, in markdown, which of ceph's files it
// names and what to write instead, since rgw-go's own files stay plain.
func (g *goChecker) notLink(it *goItem) string {
	if g.md == nil || len(it.cephPaths) == 0 {
		return notALink
	}
	paths := it.cephPaths
	more := ""
	if len(paths) > 3 {
		paths, more = paths[:3], fmt.Sprintf(" and %d more", len(it.cephPaths)-3)
	}
	return fmt.Sprintf("%s: ceph has %s%s: link it, or, if it cites rgw-go's own file, write that file's path from "+
		"rgw-go's root", notALink, strings.Join(paths, ", "), more)
}
