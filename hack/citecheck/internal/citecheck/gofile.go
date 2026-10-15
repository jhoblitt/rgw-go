package citecheck

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// goFailure is a citation in a Go file that does not hold, or that --fix
// cannot rewrite.
type goFailure struct {
	line         int
	text, reason string
}

// goResult is what checking a Go file found: the citations that fail and
// the number checked, and under --fix the plain citations rewritten into
// links and the file's new contents, or nil when none was.
type goResult struct {
	failures           []goFailure
	checked, rewritten int
	out                []byte
}

// textEdit replaces the bytes [start, end) of a paragraph's text with the
// link its plain citation resolves to. When marked, the [S] or [T] at mark
// that gave the citation its ref, and the blanks after it, go with it, lead
// being what else stood between them, a "(" or nothing, unless one of group,
// the citations the marker gives a ref, is not rewritten. When named, the
// file's name or the directories at nameAt, which a blank or a line break
// alone splits from the citation and which the link names too, go with it.
type textEdit struct {
	start, end int
	text       string
	item       *goItem
	marked     bool
	mark       int
	lead       string
	group      []*goItem
	named      bool
	nameAt     int
}

// withMarker returns the edit that also drops its marker, when it has one.
func (e textEdit) withMarker() textEdit {
	if e.marked {
		e.start, e.text = e.mark, e.lead+e.text
	}
	return e
}

// withName returns the edit that also drops the name before it, when it has
// one.
func (e textEdit) withName() textEdit {
	if e.named {
		e.start = e.nameAt
	}
	return e
}

// nameBefore returns where the name that a blank or a line break alone
// splits from the plain citation ps.items[ii] starts, now that it resolves to
// its link, and whether the link names it too, so that it goes with the
// citation: the directories a file's citation continues from the line before,
// or the file named right before a continuation.
func nameBefore(ps *paraScan, ii int) (int, bool) {
	it := ps.items[ii]
	switch {
	case it.dir != "":
		return it.dirAt, strings.HasSuffix("/"+it.path, "/"+it.dir+it.name)
	case it.kind != contItem || it.marked || ii == 0:
		return 0, false
	}
	o := ps.items[ii-1]
	if o.kind != mentionItem || strings.TrimSpace(ps.para.text[o.end:it.start]) != "" {
		return 0, false
	}
	return o.start, it.path == o.name || strings.HasSuffix(it.path, "/"+o.name)
}

// markKey names the marker at a norm offset of a paragraph.
type markKey struct {
	ps *paraScan
	at int
}

// checkGo checks the citations in the comments of the Go file name, which
// holds src, in mode m: each link as a markdown link is checked, and each
// plain citation of ceph's code as not a link, or under fix resolved and
// rewritten into one.
func checkGo(ctx context.Context, c *Checker, m mode, name string, src []byte) (goResult, error) {
	files, err := cephFiles()
	if err != nil {
		return goResult{}, err
	}
	g := &goChecker{ctx: ctx, c: c, m: m, name: name, doc: readGo(src), files: files, root: rgwgoRoot(name)}
	var res goResult
	lineOf := func(ps *paraScan, it *goItem) int { return ps.para.lines[ps.para.lineAt(it.start)].n }
	fail := func(line int, it *goItem, reason string) {
		res.failures = append(res.failures, goFailure{line: line, text: it.text, reason: reason})
	}
	edits := map[*commentPara][]textEdit{}
	var touched []*commentPara
	marks := map[markKey][]*goItem{}
	var markOrder []markKey
	for gi, scans := range g.doc.groups {
		for pi, ps := range scans {
			for ii, it := range ps.items {
				var reason string
				var err error
				switch it.kind {
				case linkItem:
					switch reason = formProblem(it.link); {
					case reason == "" && ps.para.code:
						reason = notALink + ": it is in a code block, which godoc does not link: write it outside the code block"
					case reason == "" && m != formOnly:
						reason, err = c.Check(ctx, it.link)
					}
				case fileItem, contItem:
					reason, err = g.plain(gi, pi, ii)
					if err == nil && !it.ceph {
						continue
					}
				default:
					continue
				}
				if err != nil {
					return goResult{}, fmt.Errorf("%d: %s: %w", lineOf(ps, it), it.text, err)
				}
				res.checked++
				if it.kind != linkItem && it.marked {
					k := markKey{ps: ps, at: it.marker}
					if _, ok := marks[k]; !ok {
						markOrder = append(markOrder, k)
					}
					marks[k] = append(marks[k], it)
				}
				switch {
				case reason != "":
					fail(lineOf(ps, it), it, reason)
				case it.url != "":
					if _, ok := edits[ps.para]; !ok {
						touched = append(touched, ps.para)
					}
					te := textEdit{start: it.start, end: it.end, text: it.url, item: it}
					te.nameAt, te.named = nameBefore(ps, ii)
					edits[ps.para] = append(edits[ps.para], te)
				}
			}
		}
	}
	for _, k := range markOrder {
		dropMarker(k, marks[k], edits[k.ps.para])
	}
	if len(touched) > 0 {
		out, refused := rewriteGo(src, touched, edits)
		for _, p := range touched {
			for _, e := range edits[p] {
				if why, ok := refused[e.item]; ok {
					fail(p.lines[p.lineAt(e.start)].n, e.item, why)
				} else {
					res.rewritten++
				}
			}
		}
		if res.rewritten > 0 {
			res.out = out
		}
	}
	slices.SortStableFunc(res.failures, func(a, b goFailure) int { return cmp.Compare(a.line, b.line) })
	return res, nil
}

// dropMarker makes the edit of the first citation a marker gives a ref drop
// the marker too, with the blanks after it, when nothing but blanks and a
// "(" stands between them and every citation the marker may govern is
// resolved: those it gives a ref, governed, and every other plain citation
// listed after the first, blanks, commas, "and" or "or" alone between, which
// a reader takes the marker for as well.
func dropMarker(k markKey, governed []*goItem, es []textEdit) {
	first := governed[0]
	group := slices.Clone(governed)
	for _, it := range listedAfter(k.ps, first) {
		if it.kind != linkItem && !slices.Contains(group, it) {
			group = append(group, it)
		}
	}
	for _, it := range group {
		if it.url == "" {
			return
		}
	}
	text := k.ps.para.text
	mark := k.ps.textOff(k.at)
	if mark+3 > first.start || (text[mark:mark+3] != "[S]" && text[mark:mark+3] != "[T]") ||
		strings.Trim(text[mark+3:first.start], " (") != "" {
		return
	}
	for i := range es {
		if es[i].item == first {
			es[i].marked, es[i].mark, es[i].lead, es[i].group = true, mark, strings.TrimLeft(text[mark+3:first.start], " "), group
			return
		}
	}
}

// listedAfter returns the citations listed after first in its paragraph,
// each after the one before with blanks, commas, "and" or "or" alone
// between them.
func listedAfter(ps *paraScan, first *goItem) []*goItem {
	var out []*goItem
	last, after := first, false
	for _, o := range ps.items {
		switch {
		case o == first:
			after = true
			continue
		case !after:
			continue
		case o.kind != linkItem && o.kind != fileItem && o.kind != contItem,
			!reListed.MatchString(ps.norm[last.pos+1 : o.pos]):
			return out
		}
		out = append(out, o)
		last = o
	}
	return out
}

// plain finds whether the plain citation ps.items[ii] of group gi's
// paragraph pi cites ceph and, when it does, returns why it fails: not a
// link, or under fix why it cannot be resolved. Under fix, one that resolves
// gets its link and fails nothing.
func (g *goChecker) plain(gi, pi, ii int) (string, error) {
	ps := g.doc.groups[gi][pi]
	it := ps.items[ii]
	off := ps.para.offset(it.start)
	it.ref = g.refOf(ps, ii, off)
	var cands []candidate
	switch {
	case it.prose:
		// A line reference in prose is a citation only of a file its own
		// paragraph names before it.
		if cands = paraFiles(ps, ii); len(cands) == 0 {
			return "", nil
		}
	case it.kind == contItem:
		cands = g.candidates(gi, pi, ii, off)
		// A port, "listens on :8080" or "(default :8080)", is no citation, as
		// needsShape and shaped say.
		if g.md == nil {
			if g.needsShape(ps, gi, pi, ii, off, it.paren) && !shaped(ps, it) {
				return "", nil
			}
			// Outside its parentheses the line would be none when, the text read
			// without them, it still needs a shape and has none but theirs.
			out := withoutParens(ps, it)
			it.parenOnly = it.paren && it.from == it.to && g.needsShape(out, gi, pi, ii, off, false) &&
				!shapedBy(out, it, false)
		}
		if len(cands) == 0 && g.md == nil && g.doc.headerBefore(off) == nil {
			if c, ok := g.earlier(gi); ok {
				cands, it.far = []candidate{c}, cmp.Or(c.path, c.name)
			}
		}
	}
	isCeph := g.isCeph
	if g.md != nil {
		isCeph = g.mdIsCeph
	}
	ceph, err := isCeph(ps, it, cands, off)
	if err != nil || !ceph {
		return "", err
	}
	it.ceph = true
	switch {
	case it.offList != "":
		return fmt.Sprintf("cited at %s, which the list of ceph's files was not made at: link it, or add the ref to "+
			"CITECHECK_FILES_REFS", it.offList), nil
	case g.m != fix:
		return g.notLink(it), nil
	case it.form != "":
		return "unresolved: its code span holds " + it.form + ", which --fix does not rewrite: link it by hand", nil
	case ps.para.code:
		return "unresolved: it is in a code block, which --fix does not rewrite and godoc does not link: link it by " +
			"hand, outside the code block", nil
	case it.prose:
		return "unresolved: it is a line reference in prose, which --fix does not rewrite: link it by hand", nil
	case it.far != "":
		return fmt.Sprintf("unresolved: its file, %s, is named only in a comment before its own, which --fix does not "+
			"read a continuation's file from: link it by hand", path.Base(it.far)), nil
	}
	why, err := g.resolve(ps, it, cands)
	if err != nil || why == "" {
		return "", err
	}
	return "unresolved: " + why, nil
}

// notGofmt is why a citation is not rewritten when every way of rewriting
// it leaves a gofmt-clean file one gofmt would change.
const notGofmt = "rewriting it would leave the file one gofmt changes: link it by hand"

// rewriteGo returns src with each paragraph's edits made, and why each edit
// it could not make was refused. A paragraph is rewrapped when it may be,
// and its edits are made in place otherwise. When the file was gofmt-clean
// and the rewrapping leaves it not, the edits are made in place instead; when
// that leaves it not clean either, none is made.
func rewriteGo(src []byte, touched []*commentPara, edits map[*commentPara][]textEdit) (out []byte, refused map[*goItem]string) {
	out, refused = rewriteParas(src, touched, edits, true)
	if !gofmtClean(src) || gofmtClean(out) {
		return out, refused
	}
	if out, refused = rewriteParas(src, touched, edits, false); gofmtClean(out) {
		return out, refused
	}
	refused = map[*goItem]string{}
	for _, p := range touched {
		for _, e := range edits[p] {
			refused[e.item] = notGofmt
		}
	}
	return src, refused
}

// rewriteParas returns src with each paragraph's edits made, rewrapping the
// paragraphs that may be when wrap is set, and why each edit it could not
// make was refused.
func rewriteParas(src []byte, touched []*commentPara, edits map[*commentPara][]textEdit, wrap bool) (
	out []byte, refused map[*goItem]string,
) {
	var es []edit
	var flat []*commentPara
	refused = map[*goItem]string{}
	for _, p := range touched {
		if wrap {
			marked := make([]textEdit, len(edits[p]))
			for i, te := range edits[p] {
				marked[i] = te.withName().withMarker()
			}
			if e, ok := rewrapped(src, p, marked); ok {
				es = append(es, e)
				continue
			}
		}
		flat = append(flat, p)
	}
	es = append(es, inPlace(flat, edits, refused, func(*goItem) string {
		return "it runs across a line break in a comment citecheck does not rewrap: put it on one line"
	})...)
	return rewrite(src, es), refused
}

// inPlace returns the edits that write each paragraph's links where its
// citations stand, each with the marker that gave it its ref when the marker
// is on its line and none of the citations the marker governs is refused,
// and with the name before it that goes with it: on its line, in the same
// edit, and ending the line before, by an edit of its own that drops it and
// the blanks before it, unless nothing else is on that line. It records in
// refused why each edit it cannot make is refused: one refused already, or,
// by broken's reason, one that runs across a line break.
func inPlace(paras []*commentPara, edits map[*commentPara][]textEdit, refused map[*goItem]string,
	broken func(*goItem) string,
) []edit {
	var es []edit
	for _, p := range paras {
		for _, te := range edits[p] {
			if _, ok := refused[te.item]; !ok && p.lineAt(te.end-1) != p.lineAt(te.start) {
				refused[te.item] = broken(te.item)
			}
		}
		for _, te := range edits[p] {
			if _, ok := refused[te.item]; ok {
				continue
			}
			if m := te.withMarker(); te.marked && p.lineAt(m.start) == p.lineAt(m.end-1) &&
				!slices.ContainsFunc(te.group, func(it *goItem) bool { _, no := refused[it]; return no }) {
				te = m
			}
			if te.named {
				if j := p.lineAt(te.nameAt); j == p.lineAt(te.start) {
					te = te.withName()
				} else if e, paren, ok := dropName(p, te.nameAt); ok {
					es = append(es, e)
					if paren {
						te.text = "(" + te.text
					}
				}
			}
			i := p.lineAt(te.start)
			s := p.lines[i].start + te.start - p.at[i]
			es = append(es, edit{start: s, end: s + te.end - te.start, text: te.text})
		}
	}
	return es
}

// dropName returns the edit that drops the name at the offset at in a
// paragraph's text, which ends its line, with the blanks before it and a "("
// right before it, which paren reports, for the link to open instead; and
// false when nothing but blanks comes before them on its line, which dropping
// them would leave empty.
func dropName(p *commentPara, at int) (e edit, paren, ok bool) {
	j := p.lineAt(at)
	s := at
	if s > p.at[j] && p.text[s-1] == '(' {
		s, paren = s-1, true
	}
	for s > p.at[j] && p.text[s-1] == ' ' {
		s--
	}
	if s == p.at[j] {
		return edit{}, false, false
	}
	l := p.lines[j]
	return edit{start: l.start + s - p.at[j], end: l.end, text: ""}, paren, true
}

// rewrapped returns the edit that makes a paragraph's edits and rewraps it
// to its widest line's width, in runes, its prefix included, a word wider
// than that, such as a link and the "at <tag>" after it, on a line of its
// own, and false when the paragraph may not be rewrapped: it is neither plain
// prose nor a list item's first paragraph, its lines' prefixes differ, a line
// holds a tab or two blanks running, or a line it would make reads as a list
// item, a heading or a link definition. A list item's first line keeps its
// prefix and its marker, and its other lines take the prefix of its second,
// or with none its first's and blanks as wide as its marker. The lines before
// the first edit are kept, and so are those after the last once the new lines
// end where an old one did.
func rewrapped(src []byte, p *commentPara, es []textEdit) (edit, bool) {
	if !p.wrap && !p.item || bytes.IndexByte(src, '\r') >= 0 {
		return edit{}, false
	}
	head, rest, ok := p.prefixes()
	if !ok {
		return edit{}, false
	}
	width := 0
	for i, l := range p.lines {
		t := string(src[l.start:l.end])
		pre := rest
		if i == 0 {
			pre = head
		}
		if l.prefix != pre || strings.Contains(t, "  ") || strings.ContainsRune(t, '\t') || strings.HasSuffix(t, " ") {
			return edit{}, false
		}
		width = max(width, utf8.RuneCountInString(pre)+utf8.RuneCountInString(t))
	}
	var b strings.Builder
	last := 0
	for _, e := range es {
		b.WriteString(p.text[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.WriteString(p.text[last:])
	words, wordAt := rewrapWords(b.String())
	// moved returns where an old line's start is in the new text, or -1 when
	// an edit swallowed it.
	moved := func(a int) int {
		shift := 0
		for _, e := range es {
			switch {
			case e.end <= a:
				shift += len(e.text) - (e.end - e.start)
			case e.start < a:
				return -1
			}
		}
		return a + shift
	}
	first, lastEdited := p.lineAt(es[0].start), p.lineAt(es[len(es)-1].end-1)
	// resume holds the old lines after the last edit by the word each starts.
	resume := map[int]int{}
	for i := lastEdited + 1; i < len(p.lines); i++ {
		if k, ok := wordAt[moved(p.at[i])]; ok {
			resume[k] = i
		}
	}
	end := len(p.lines)
	k0 := wordAt[moved(p.at[first])]
	// prefixOf is the prefix of the n-th line the rewrap makes.
	prefixOf := func(n int) string {
		if n == 0 && first == 0 {
			return head
		}
		return rest
	}
	var out []string
	cur := words[k0]
	for k := k0 + 1; k < len(words); k++ {
		if utf8.RuneCountInString(prefixOf(len(out)))+utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(words[k]) <= width {
			cur += " " + words[k]
			continue
		}
		out = append(out, cur)
		if i, ok := resume[k]; ok {
			end, cur = i, ""
			break
		}
		cur = words[k]
	}
	if cur != "" {
		out = append(out, cur)
	}
	for i, l := range out {
		marker := p.item && i == 0 && first == 0
		if listItem(l) != marker || heading(l) || strings.HasPrefix(l, "[") && strings.Contains(l, "]:") {
			return edit{}, false
		}
		out[i] = prefixOf(i) + l
	}
	return edit{start: p.lines[first].start - len(prefixOf(0)), end: p.lines[end-1].end, text: strings.Join(out, "\n")}, true
}

// prefixes returns the prefix of a paragraph's first line, and that of the
// lines after it: for plain prose its first line's, and for a list item its
// second line's, which must be wider than its first's, or with no second line
// its first's and blanks as wide as its marker and the blanks after it.
func (p *commentPara) prefixes() (head, rest string, ok bool) {
	head = p.lines[0].prefix
	switch {
	case !p.item:
		return head, head, true
	case len(p.lines) > 1:
		rest = p.lines[1].prefix
		return head, rest, len(rest) > len(head) && strings.HasPrefix(rest, strings.TrimRight(head, " "))
	}
	t := p.text
	n := strings.IndexAny(t, " \t")
	if n < 0 {
		return "", "", false
	}
	n += len(t[n:]) - len(strings.TrimLeft(t[n:], " \t"))
	return head, head + strings.Repeat(" ", utf8.RuneCountInString(t[:n])), true
}

// reLinkEnd is a word ending in a link's line anchor, or in it and the
// comma that closes a list; reTagWord is a word that is a tag, with any
// punctuation after it.
var (
	reLinkEnd = regexp.MustCompile(`https://\S+#L\d+(?:-L\d+)?,?$`)
	reTagWord = regexp.MustCompile(`^` + tagForm + `\b`)
)

// rewrapWords splits a paragraph's new text into the words a rewrap places,
// and returns the index of the word each offset of the text starts. A link
// and an "at <tag>", "in <tag>", "on <tag>" or "at both tags" right after it,
// or after the comma that follows it, are one word, so that the tag stays on
// the link's line.
func rewrapWords(text string) (words []string, wordAt map[int]int) {
	split := strings.Split(text, " ")
	wordAt = map[int]int{}
	off := 0
	for i := 0; i < len(split); i++ {
		w, at := split[i], off
		off += len(split[i]) + 1
		if reLinkEnd.MatchString(w) && i+2 < len(split) {
			n := 0
			switch next, after := split[i+1], split[i+2]; {
			case (next == "at" || next == "in" || next == "on") && reTagWord.MatchString(after):
				n = 2
			case next == "at" && after == "both" && i+3 < len(split) && reBothNoun.MatchString(split[i+3]):
				n = 3
			case next == "at" && strings.TrimRight(after, ").,;:") == "both":
				n = 2
			}
			w = strings.Join(split[i:i+n+1], " ")
			for range n {
				i++
				off += len(split[i]) + 1
			}
		}
		wordAt[at] = len(words)
		words = append(words, w)
	}
	return words, wordAt
}

// reBothNoun is the word after "at both" that "at both tags" ends with.
var reBothNoun = regexp.MustCompile(`^(?:tags|releases)\b`)

// gofmtClean reports whether src is Go that gofmt leaves as it is.
func gofmtClean(src []byte) bool {
	out, err := format.Source(src)
	return err == nil && bytes.Equal(out, src)
}
