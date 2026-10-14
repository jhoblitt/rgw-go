package citecheck

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"go/format"
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
// the citations the marker gives a ref, is not rewritten.
type textEdit struct {
	start, end int
	text       string
	item       *goItem
	marked     bool
	mark       int
	lead       string
	group      []*goItem
}

// withMarker returns the edit that also drops its marker, when it has one.
func (e textEdit) withMarker() textEdit {
	if e.marked {
		e.start, e.text = e.mark, e.lead+e.text
	}
	return e
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
	g := &goChecker{ctx: ctx, c: c, m: m, name: name, doc: readGo(src)}
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
					if reason = formProblem(it.link); m != formOnly && reason == "" {
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
					edits[ps.para] = append(edits[ps.para], textEdit{start: it.start, end: it.end, text: it.url, item: it})
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
	if it.kind == contItem {
		cands = g.candidates(gi, pi, ii, off)
	}
	ceph, err := g.isCeph(it, cands, off)
	if err != nil || !ceph {
		return "", err
	}
	it.ceph = true
	if g.m != fix {
		return notALink, nil
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
	refused = map[*goItem]string{}
	for _, p := range touched {
		if wrap {
			marked := make([]textEdit, len(edits[p]))
			for i, te := range edits[p] {
				marked[i] = te.withMarker()
			}
			if e, ok := rewrapped(src, p, marked); ok {
				es = append(es, e)
				continue
			}
		}
		for _, te := range edits[p] {
			if p.lineAt(te.end-1) != p.lineAt(te.start) {
				refused[te.item] = "it runs across a line break in a comment citecheck does not rewrap: put it on one line"
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
			i := p.lineAt(te.start)
			s := p.lines[i].start + te.start - p.at[i]
			es = append(es, edit{start: s, end: s + te.end - te.start, text: te.text})
		}
	}
	return rewrite(src, es), refused
}

// rewrapped returns the edit that makes a paragraph's edits and rewraps it
// to its widest line's width, in runes, a word wider than that, such as a
// link and the "at <tag>" after it, on a line of its own, and false when
// the paragraph may not be
// rewrapped: it is not plain prose, its lines' prefixes differ, a line holds
// a tab or two blanks running, or a line it would make reads as a list
// item, a heading or a link definition. The lines before the first edit are
// kept, and so are those after the last once the new lines end where an old
// one did.
func rewrapped(src []byte, p *commentPara, es []textEdit) (edit, bool) {
	if !p.wrap || bytes.IndexByte(src, '\r') >= 0 {
		return edit{}, false
	}
	prefix := p.lines[0].prefix
	width := 0
	for _, l := range p.lines {
		t := string(src[l.start:l.end])
		if l.prefix != prefix || strings.Contains(t, "  ") || strings.ContainsRune(t, '\t') || strings.HasSuffix(t, " ") {
			return edit{}, false
		}
		width = max(width, utf8.RuneCountInString(t))
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
	var out []string
	cur := words[k0]
	for k := k0 + 1; k < len(words); k++ {
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(words[k]) <= width {
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
		if listItem(l) || heading(l) || strings.HasPrefix(l, "[") && strings.Contains(l, "]:") {
			return edit{}, false
		}
		out[i] = prefix + l
	}
	return edit{start: p.lines[first].start - len(prefix), end: p.lines[end-1].end, text: strings.Join(out, "\n")}, true
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
