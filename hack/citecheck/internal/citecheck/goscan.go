package citecheck

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The forms of a plain citation in a Go comment, and of a file named
// without lines. A file is a path whose last segment ends in one of the
// extensions, and neither it nor a continuation may touch a path or
// identifier byte on either side. RE2 has no lookaround, so the bytes on
// either side are checked by hand, and an extension is listed before any
// that it starts with, since RE2 takes the first alternative that matches.
const (
	goExt   = `(?:gperf|yaml\.in|yaml|yml|hpp|cmake|proto|json|txt|cc|py|go|sh|h|c|in)`
	goPath  = `(?:[\w.\-]+/)*[\w.\-]*[A-Za-z_][\w\-]*\.` + goExt
	tagForm = `v\d+\.\d+\.\d+`
)

var (
	// rePlainFile is <path>[ [S|T]]:<from>[-<to>].
	rePlainFile = regexp.MustCompile(`(` + goPath + `)(?:\s*\[([ST])\])?:(\d+)(?:-(\d+))?`)
	// rePlainCont is a continuation, [[S|T]]:<from>[-<to>].
	rePlainCont = regexp.MustCompile(`(?:\[([ST])\])?:(\d+)(?:-(\d+))?`)
	// reMention is a file named without lines.
	reMention = regexp.MustCompile(goPath)
	// reURL is any URL, which ends at a blank.
	reURL = regexp.MustCompile("https?://[^\\s<>\"'`]+")
)

// The placeholders a paragraph's norm holds in place of its items.
const (
	citeMark    = '\x01'
	urlMark     = '\x02'
	mentionMark = '\x03'
)

type itemKind int

const (
	// linkItem is a GitHub link to lines of ceph/ceph.
	linkItem itemKind = iota
	// fileItem is a plain citation that names its file.
	fileItem
	// contItem is a plain citation that names only lines, :<from>[-<to>].
	contItem
	// mentionItem is a file named without lines.
	mentionItem
	// urlItem is any other URL.
	urlItem
)

// goItem is a citation, a file mention or a URL in a comment paragraph.
type goItem struct {
	kind itemKind
	// start and end are the item's offsets in the paragraph's text, and pos
	// the offset of its placeholder in the paragraph's norm.
	start, end, pos int
	// text is the item as written.
	text string
	// name is the path a fileItem or a mentionItem writes.
	name     string
	mark     string
	from, to int
	// link is a linkItem's citation, and label a markdown link's text, the
	// brackets left out.
	link  Citation
	label string

	// ref and path are what the citation resolves to: a link's own, or
	// those a plain citation is found to mean; either may be "".
	ref, path string
	// fromHeader reports whether only the header convention gives a plain
	// citation its ref, and scope is the file the header names, whose
	// citations alone the ref is for, or "".
	fromHeader bool
	scope      string
	// marked reports whether a plain citation's ref comes from an [S] or [T]
	// written before it, which marker is the norm's byte at marker; the
	// marker is dropped with the citations it gives a ref once all of them
	// are links.
	marked bool
	marker int
	// both reports whether a plain citation is cited "at both tags", and so
	// must cite the same lines at each marker's release.
	both bool
	// rule names the rule that gave a plain citation its ref, and releases
	// are the releases its clause names when the clause rule read it, which
	// leave it with no ref when there are several and none is right before
	// it.
	rule     string
	releases []string
	// why says why a plain citation has no ref, when a rule that reads
	// markdown found it names one citecheck cannot use, or "".
	why string
	// ceph reports whether a plain citation cites ceph's code, and so is
	// checked, and cephPaths are the paths of ceph's that tell so in markdown.
	ceph      bool
	cephPaths []string
	// url is the link --fix writes for a plain citation, or "", and ids the
	// identifiers its cited lines were checked for.
	url string
	ids []string
	// atRef is the commit id an "at <commit>" right after a markdown
	// citation names, or the branch an "on main", "at main" or
	// "at origin/main" there names; form says which form a markdown code span holds a
	// citation in that --fix does not rewrite; and offList is the ref, one
	// the list of ceph's files was not made at, that a citation of a file
	// the list lacks is tied to, which --form-only cannot tell is ceph's.
	atRef, form, offList string
}

// paraScan is what a comment paragraph cites.
type paraScan struct {
	para  *commentPara
	items []*goItem
	// masked is the paragraph's text with its URLs blanked, and norm the
	// text with each item replaced by its placeholder, so that the rules
	// read the same text before and after --fix writes the links.
	masked, norm string
	// hint is the repository other than ceph the paragraph names, or "".
	hint string
}

// hints are the repositories other than ceph whose files a paragraph naming
// one may cite.
var hints = []struct {
	re   *regexp.Regexp
	repo string
}{
	{regexp.MustCompile(`(?i)\brook\b|pkg/operator|tests/integration`), "rook"},
	{regexp.MustCompile(`(?i)go-ceph|\bgoceph\b`), "go-ceph"},
	{regexp.MustCompile(`(?i)s3-tests|s3tests`), "s3-tests"},
}

func wordByte(c byte) bool { return isIdent(c) }

func pathByte(c byte) bool { return isIdent(c) || c == '/' || c == '.' || c == '-' }

// scanPara finds a paragraph's links, other URLs, plain citations and file
// mentions.
func scanPara(p *commentPara) *paraScan {
	text := p.text
	ps := &paraScan{para: p}
	masked := []byte(text)
	for _, ix := range reURL.FindAllStringIndex(text, -1) {
		s, e := ix[0], trimURL(text, ix[0], ix[1])
		it := &goItem{kind: urlItem, start: s, end: e, text: text[s:e]}
		if cs, offs := links(text[s:e], 0); len(cs) > 0 && offs[0] == 0 {
			it.kind, it.link = linkItem, cs[0]
			it.text, it.ref, it.path, it.from, it.to = cs[0].Text, cs[0].Ref, cs[0].Path, cs[0].From, cs[0].To
		}
		for i := s; i < e; i++ {
			masked[i] = ' '
		}
		ps.items = append(ps.items, it)
	}
	ps.masked = string(masked)
	ps.items = append(ps.items, plainItems(ps.masked)...)
	ps.finish(text, ps.masked)
	return ps
}

// finish orders a paragraph's items, reads which repository other than ceph
// hint, its text with the URLs blanked, names, and writes its norm.
func (ps *paraScan) finish(text, hint string) {
	slices.SortFunc(ps.items, func(a, b *goItem) int { return a.start - b.start })
	for _, h := range hints {
		if h.re.MatchString(hint) {
			ps.hint = h.repo
			break
		}
	}
	var b strings.Builder
	last := 0
	for _, it := range ps.items {
		b.WriteString(text[last:it.start])
		it.pos = b.Len()
		switch it.kind {
		case urlItem:
			b.WriteByte(urlMark)
		case mentionItem:
			b.WriteByte(mentionMark)
		default:
			b.WriteByte(citeMark)
		}
		last = it.end
	}
	b.WriteString(text[last:])
	ps.norm = b.String()
}

// textOff returns the offset in the paragraph's text of the offset pos in
// its norm, which is not inside an item.
func (ps *paraScan) textOff(pos int) int {
	off := pos
	for _, it := range ps.items {
		if it.pos >= pos {
			break
		}
		off += it.end - it.start - 1
	}
	return off
}

// trimURL returns where a URL found in prose ends, the punctuation after
// it left out.
func trimURL(text string, s, e int) int {
	for e > s && strings.IndexByte(".,;:!?)]}", text[e-1]) >= 0 {
		e--
	}
	return e
}

// plainItems returns the plain citations and file mentions in a
// paragraph's masked text.
func plainItems(ms string) []*goItem {
	var items []*goItem
	var taken [][2]int
	for _, m := range rePlainFile.FindAllStringSubmatchIndex(ms, -1) {
		s, e, ok := bounded(ms, m, pathByte, wordByte)
		if !ok {
			continue
		}
		it := &goItem{kind: fileItem, start: s, end: e, text: ms[s:e], name: ms[m[2]:m[3]]}
		if m[4] >= 0 {
			it.mark = ms[m[4]:m[5]]
		}
		if it.from, it.to, ok = lines(ms, m, e); !ok {
			continue
		}
		items = append(items, it)
		taken = append(taken, [2]int{s, e})
	}
	overlaps := func(s, e int) bool {
		return slices.ContainsFunc(taken, func(t [2]int) bool { return s < t[1] && t[0] < e })
	}
	contBefore := func(c byte) bool { return pathByte(c) || c == ':' || c == ']' || c == '[' }
	contAfter := func(c byte) bool { return wordByte(c) || c == ':' || c == ']' }
	for _, m := range rePlainCont.FindAllStringSubmatchIndex(ms, -1) {
		s, e, ok := bounded(ms, m, contBefore, contAfter)
		if !ok || overlaps(s, e) {
			continue
		}
		it := &goItem{kind: contItem, start: s, end: e, text: ms[s:e]}
		if m[2] >= 0 {
			it.mark = ms[m[2]:m[3]]
		}
		if it.from, it.to, ok = lines(ms, m, e); !ok {
			continue
		}
		items = append(items, it)
	}
	for _, ix := range reMention.FindAllStringIndex(ms, -1) {
		s, e := ix[0], ix[1]
		if overlaps(s, e) || s > 0 && pathByte(ms[s-1]) || e < len(ms) && (wordByte(ms[e]) || ms[e] == ':') {
			continue
		}
		items = append(items, &goItem{kind: mentionItem, start: s, end: e, text: ms[s:e], name: ms[s:e]})
	}
	return items
}

// bounded applies a citation match's lookbehind and lookahead: the byte
// before it must not be one before rejects, and the byte after it not one
// after rejects, or else, when it names a last line, the byte after its
// first line, which is the "-" before the last. It returns the match's
// bounds and whether it stands.
func bounded(ms string, m []int, before, after func(byte) bool) (start, end int, ok bool) {
	s, e := m[0], m[1]
	if s > 0 && before(ms[s-1]) {
		return 0, 0, false
	}
	n := len(m)
	if e < len(ms) && after(ms[e]) {
		if m[n-2] < 0 {
			return 0, 0, false
		}
		e = m[n-3]
	}
	return s, e, true
}

// lines returns a match's first and last line, the last the first's when
// the match, ending at e, names none, and false when a number is too long to
// be a line.
func lines(ms string, m []int, e int) (from, to int, ok bool) {
	n := len(m)
	from, err := strconv.Atoi(ms[m[n-4]:m[n-3]])
	if err != nil {
		return 0, 0, false
	}
	to = from
	if m[n-2] >= 0 && m[n-1] <= e {
		if to, err = strconv.Atoi(ms[m[n-2]:m[n-1]]); err != nil {
			return 0, 0, false
		}
	}
	return from, to, true
}
