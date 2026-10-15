package citecheck

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// mdKind is the kind of block a markdown paragraph is.
type mdKind int

const (
	// mdText is a paragraph, or a list item's paragraph after its first.
	mdText mdKind = iota
	// mdItem is a list item's first paragraph.
	mdItem
	mdHeading
	// mdRow is a table's row.
	mdRow
)

// mdBlock is how a markdown paragraph stands in its document: its kind, and
// the column its text, or a list item's marker, starts at.
type mdBlock struct {
	kind   mdKind
	indent int
}

// mdDoc is what a markdown document's plain citations are read in: its
// paragraphs by section, as a Go file's are by comment, and how each stands.
type mdDoc struct {
	*goDoc
	blocks [][]mdBlock
	// at holds each paragraph's section and index in it.
	at map[*paraScan][2]int
	// noFix holds why a citation that resolves cannot be written as a link
	// where it stands, and spans the citations a code span holds.
	noFix map[*goItem]string
	spans map[*goItem]bool
	// itemPara holds the paragraph each item is in, and pairOf the pair list
	// each plain citation under a lead-in that states the pair convention
	// is in.
	itemPara map[*goItem]*paraScan
	pairOf   map[*goItem]*pairList
	// root is the root of the rgw-go tree that holds the document, or "".
	root string
}

var reListMarker = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s`)

// readMarkdown reads a markdown document's paragraphs as Scan pairs code
// spans in them, the lines of each joined by single blanks: a blank line, a
// fence, a heading, a list item or a table row ends one, and a heading and a
// table row are each one of their own. Fenced code blocks are skipped. A
// heading starts a section.
func readMarkdown(src []byte) *mdDoc {
	d := &mdDoc{
		goDoc: &goDoc{code: map[string]bool{}, pkgs: map[string]bool{}}, at: map[*paraScan][2]int{},
		noFix: map[*goItem]string{}, spans: map[*goItem]bool{}, itemPara: map[*goItem]*paraScan{},
	}
	var paras [][]*commentPara
	var cur *commentPara
	section := func() {
		paras = append(paras, nil)
		d.blocks = append(d.blocks, nil)
	}
	start := func(kind mdKind, indent int) {
		cur = &commentPara{}
		gi := len(paras) - 1
		paras[gi] = append(paras[gi], cur)
		d.blocks[gi] = append(d.blocks[gi], mdBlock{kind: kind, indent: indent})
	}
	section()
	var fence string
	next := 0
	for i, line := range strings.Split(string(src), "\n") {
		n, off := i+1, next
		next += len(line) + 1
		line = strings.TrimSuffix(line, "\r")
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if f := opensFence(line); f != "" {
			cur, fence = nil, f
			continue
		}
		t := strings.TrimLeft(line, " \t")
		if t == "" {
			cur = nil
			continue
		}
		indent := len(line) - len(t)
		cl := commentLine{n: n, start: off + indent, end: off + len(line), prefix: line[:indent]}
		switch {
		case isHeading(line):
			section()
			start(mdHeading, indent)
		case strings.HasPrefix(t, "|"):
			start(mdRow, indent)
		case reListMarker.MatchString(line):
			start(mdItem, indent)
		case cur == nil:
			start(mdText, indent)
		}
		cur.add(cl, t)
		if isHeading(line) || strings.HasPrefix(t, "|") {
			cur = nil
		}
	}
	for gi, g := range paras {
		var scans []*paraScan
		for pi, p := range g {
			ps := d.scanPara(p, d.blocks[gi][pi].kind)
			d.at[ps] = [2]int{gi, pi}
			for _, it := range ps.items {
				d.itemPara[it] = ps
			}
			scans = append(scans, ps)
			d.headers = append(d.headers, headersIn(ps)...)
		}
		d.groups = append(d.groups, scans)
	}
	d.findPairLists()
	return d
}

var (
	// reSpanFile, reSpanCont and reSpanMention are a code span's whole
	// content as a plain citation that names its file, as one that names
	// only lines, and as a file named without lines.
	reSpanFile    = regexp.MustCompile(`^(` + goPath + `)(?:\s*\[([ST])\])?:(\d+)(?:-(\d+))?$`)
	reSpanCont    = regexp.MustCompile(`^(?:\[([ST])\])?:(\d+)(?:-(\d+))?$`)
	reSpanMention = regexp.MustCompile(`^` + goPath + `$`)
)

// spanForms are the forms a code span may hold a plain citation in that
// --fix does not rewrite, each the file it names, if any, a marker, and the
// first line: a marker before its file, `[T] rgw_op.cc:10`; a list of line
// ranges, `rgw_op.cc:10,20-30`; a symbol after its lines,
// `:10-20#RGWOp::execute`; and an open range, `:10-`, `:10-...` or `:10+`.
var spanForms = []struct {
	re   *regexp.Regexp
	form string
}{
	{regexp.MustCompile(`^\[([ST])\]\s+(` + goPath + `)():(\d+)(?:-\d+)?$`), "a marker before its file"},
	{
		regexp.MustCompile(`^()(` + goPath + `)?(?:\s*\[([ST])\])?:(\d+)(?:-\d+)?(?:,\s*\d+(?:-\d+)?)+$`),
		"a list of line ranges",
	},
	{regexp.MustCompile(`^()(` + goPath + `)?(?:\s*\[([ST])\])?:(\d+)(?:-\d+)?#[\w:~]+$`), "a symbol after its lines"},
	{regexp.MustCompile(`^()(` + goPath + `)?(?:\s*\[([ST])\])?:(\d+)(?:-|\+)(?:\.\.\.|…)?$`), "an open line range"},
}

// scanPara finds a markdown paragraph's links, other URLs, plain citations
// and file mentions: a link, [text](destination), is one item, its text
// read as no citation; a code span whose whole content is a plain citation
// or a file name is one, and any other is code, read for identifiers only;
// outside them, prose is read as a Go comment's is.
func (d *mdDoc) scanPara(p *commentPara, kind mdKind) *paraScan {
	text := p.text
	ps := &paraScan{para: p}
	masked, hint := []byte(text), []byte(text)
	blank := func(b []byte, s, e int) {
		for i := s; i < e; i++ {
			b[i] = ' '
		}
	}
	var spans []*goItem
	var spanAt, linkAt [][2]int
	var opens []int
	for i := 0; i < len(text); {
		switch text[i] {
		case '\\':
			i += 2
			continue
		case '[':
			opens = append(opens, i)
		case ']':
			if len(opens) == 0 {
				break
			}
			open := opens[len(opens)-1]
			opens = opens[:len(opens)-1]
			end := linkEnd(text, i+1)
			if end < 0 {
				break
			}
			label := text[open+1 : i]
			if open > 0 && text[open-1] == '!' {
				open--
			}
			it := &goItem{kind: urlItem, start: open, end: end, text: text[open:end], label: label}
			if cs, _ := links(text[i+1:end], 0); len(cs) > 0 && text[open] == '[' {
				it.kind, it.link = linkItem, cs[0]
				it.text, it.ref, it.path, it.from, it.to = cs[0].Text, cs[0].Ref, cs[0].Path, cs[0].From, cs[0].To
			}
			// A link in this one's text, which markdown renders as text, is
			// part of it.
			ps.items = slices.DeleteFunc(ps.items, func(o *goItem) bool { return open <= o.start && o.end <= end })
			ps.items = append(ps.items, it)
			linkAt = append(linkAt, [2]int{open, end})
			blank(masked, open, end)
			blank(hint, i+1, end)
			i = end
			continue
		case '`':
			run := backticks(text, i)
			end := closingRun(text, i+run, run)
			if end < 0 {
				i += run
				continue
			}
			if it := spanItem(strings.TrimSpace(text[i+run:end]), i, end+run); it != nil {
				spans = append(spans, it)
			}
			spanAt = append(spanAt, [2]int{i, end + run})
			i = end + run
			continue
		}
		i++
	}
	for _, it := range spans {
		if !slices.ContainsFunc(linkAt, func(l [2]int) bool { return l[0] <= it.start && it.end <= l[1] }) {
			ps.items = append(ps.items, it)
			d.spans[it] = true
		}
	}
	for _, s := range spanAt {
		blank(masked, s[0], s[1])
	}
	for _, ix := range reURL.FindAllStringIndex(string(masked), -1) {
		s, e := ix[0], trimURL(text, ix[0], ix[1])
		it := &goItem{kind: urlItem, start: s, end: e, text: text[s:e]}
		if cs, offs := links(text[s:e], 0); len(cs) > 0 && offs[0] == 0 {
			it.kind, it.link = linkItem, cs[0]
			it.text, it.ref, it.path, it.from, it.to = cs[0].Text, cs[0].Ref, cs[0].Path, cs[0].From, cs[0].To
		}
		blank(masked, s, e)
		blank(hint, s, e)
		ps.items = append(ps.items, it)
	}
	ps.masked = string(masked)
	ps.items = append(ps.items, plainItems(ps.masked)...)
	for _, it := range ps.items {
		switch {
		case it.kind != fileItem && it.kind != contItem:
		case kind == mdHeading:
			d.noFix[it] = "it is in a heading, whose anchor a link could change: cite it in the text below the heading"
		case it.start > 0 && text[it.start-1] == '!' && (it.start < 2 || text[it.start-2] != '\\'):
			d.noFix[it] = `it follows a "!", which would make the link an image: escape it as "\!"`
		}
	}
	ps.finish(text, string(hint))
	return ps
}

// linkEnd returns the offset after the destination of a link whose text's
// "]" is right before off, a "(" there opening it, or after the label of a
// reference link, a "[" there opening it; or -1 when neither follows.
func linkEnd(text string, off int) int {
	if off >= len(text) {
		return -1
	}
	closer := map[byte]byte{'(': ')', '[': ']'}[text[off]]
	if closer == 0 {
		return -1
	}
	depth := 0
	for i := off; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case text[off]:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// spanItem returns the plain citation or file mention a code span's whole
// content is, the span from start to end, or nil.
func spanItem(content string, start, end int) *goItem {
	it := &goItem{start: start, end: end, text: content}
	var problem string
	switch m := reSpanFile.FindStringSubmatch(content); {
	case m != nil:
		it.kind, it.name, it.mark = fileItem, m[1], m[2]
		it.from, it.to, problem = lineRange(m[3], m[4])
	default:
		m = reSpanCont.FindStringSubmatch(content)
		switch {
		case m != nil:
			it.kind, it.mark = contItem, m[1]
			it.from, it.to, problem = lineRange(m[2], m[3])
		case reSpanMention.MatchString(content):
			it.kind, it.name = mentionItem, content
		default:
			return oddSpan(it, content)
		}
	}
	if problem != "" {
		return nil
	}
	return it
}

// oddSpan returns it as the plain citation a code span's content holds in
// one of spanForms, or nil.
func oddSpan(it *goItem, content string) *goItem {
	for _, f := range spanForms {
		m := f.re.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		it.kind, it.name, it.mark, it.form = contItem, m[2], cmp.Or(m[1], m[3]), f.form
		if it.name != "" {
			it.kind = fileItem
		}
		var problem string
		if it.from, it.to, problem = lineRange(m[4], ""); problem != "" {
			return nil
		}
		return it
	}
	return nil
}

// broken returns why a markdown citation that resolves but runs across a
// line break is not written as a link: the link would rejoin the lines.
func (d *mdDoc) broken(it *goItem) string {
	if d.spans[it] {
		return "the code span runs across a line break, which rewriting it would rewrap: put it on one line"
	}
	return "it runs across a line break, which rewriting it would rewrap: put it on one line"
}

// mdResult is what checking a markdown document's plain citations found: the
// citations that fail, the number checked, and under --fix the edits that
// write the links and the number of them.
type mdResult struct {
	failures           []goFailure
	checked, rewritten int
	edits              []edit
}

// checkMarkdownPlain checks the plain citations of a markdown document in
// mode m, as checkGo does a Go file's: each of ceph's fails as not a link, or
// under fix is resolved and its edit returned.
func checkMarkdownPlain(ctx context.Context, c *Checker, m mode, name string, src []byte) (mdResult, error) {
	files, err := cephFiles()
	if err != nil {
		return mdResult{}, err
	}
	d := readMarkdown(src)
	d.root = rgwgoRoot(name)
	g := &goChecker{ctx: ctx, c: c, m: m, name: name, doc: d.goDoc, md: d, files: files}
	var res mdResult
	lineOf := func(ps *paraScan, it *goItem) int { return ps.para.lines[ps.para.lineAt(it.start)].n }
	edits := map[*commentPara][]textEdit{}
	var touched []*commentPara
	marks := map[markKey][]*goItem{}
	var markOrder []markKey
	add := func(ps *paraScan, it *goItem, reason string) error {
		res.checked++
		if it.marked {
			k := markKey{ps: ps, at: it.marker}
			if _, ok := marks[k]; !ok {
				markOrder = append(markOrder, k)
			}
			marks[k] = append(marks[k], it)
		}
		if reason != "" {
			res.failures = append(res.failures, goFailure{line: lineOf(ps, it), text: it.text, reason: reason})
			return nil
		}
		if it.url == "" {
			return nil
		}
		link, err := linkFor(Citation{Text: it.text, Ref: it.ref, Path: it.path, From: it.from, To: it.to})
		if err != nil {
			return fmt.Errorf("%d: %w", lineOf(ps, it), err)
		}
		if _, ok := edits[ps.para]; !ok {
			touched = append(touched, ps.para)
		}
		edits[ps.para] = append(edits[ps.para], textEdit{start: it.start, end: it.end, text: link, item: it})
		return nil
	}
	// A regular pair list's citations wait until all of them are resolved,
	// for the convention holds for all of them or for none.
	type held struct {
		ps     *paraScan
		it     *goItem
		reason string
	}
	waiting := map[*pairList][]held{}
	var lists []*pairList
	for gi, scans := range d.groups {
		for pi, ps := range scans {
			for ii, it := range ps.items {
				if it.kind != fileItem && it.kind != contItem {
					continue
				}
				reason, err := g.plain(gi, pi, ii)
				if err != nil {
					return mdResult{}, fmt.Errorf("%d: %s: %w", lineOf(ps, it), it.text, err)
				}
				if pl := d.pairOf[it]; pl != nil && pl.why == "" && m == fix {
					if _, ok := waiting[pl]; !ok {
						lists = append(lists, pl)
					}
					waiting[pl] = append(waiting[pl], held{ps: ps, it: it, reason: reason})
					continue
				}
				if !it.ceph {
					continue
				}
				if err := add(ps, it, reason); err != nil {
					return mdResult{}, err
				}
			}
		}
	}
	for _, pl := range lists {
		failed := map[*goItem]string{}
		for _, h := range waiting[pl] {
			failed[h.it] = h.reason
		}
		why := d.settle(pl, failed)
		for _, h := range waiting[pl] {
			if !h.it.ceph {
				continue
			}
			if why != "" && h.reason == "" {
				h.it.url, h.reason = "", cmp.Or(d.refusal(h.it), "unresolved: "+pl.decline(why))
			}
			if err := add(h.ps, h.it, h.reason); err != nil {
				return mdResult{}, err
			}
		}
	}
	for _, k := range markOrder {
		dropMarker(k, marks[k], edits[k.ps.para])
	}
	refused := maps.Clone(d.noFix)
	res.edits = inPlace(touched, edits, refused, d.broken)
	for _, p := range touched {
		for _, e := range edits[p] {
			if why, ok := refused[e.item]; ok {
				res.failures = append(res.failures, goFailure{line: p.lines[p.lineAt(e.start)].n, text: e.item.text, reason: why})
			} else {
				res.rewritten++
			}
		}
	}
	slices.SortStableFunc(res.failures, func(a, b goFailure) int { return a.line - b.line })
	return res, nil
}
