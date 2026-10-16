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
	// checked, and cephPaths are the paths of ceph's that tell so.
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
	// tag is the tag a Go comment's continuation is written after, as
	// v19.2.6:3706-3720 is; prose reports whether a continuation is a line
	// reference in prose, "line 89" or "lines 130-172"; far is the file a
	// continuation whose own comment names none is of, named in an earlier
	// comment; paren reports whether a Go comment's citation stands inside a
	// "(" and the ")" that closes it, bare whether it stands there alone, as
	// in "(:101)" or "(Parse7, :751)", and parenOnly whether only its
	// parentheses make it a citation, so that outside them it is none.
	tag       string
	prose     bool
	far       string
	paren     bool
	bare      bool
	parenOnly bool
	// dir is the path a file's citation that starts a comment line is split
	// from by the line break before it, as "src/rgw/driver/" ends a line
	// before "rados/rgw_tools.cc:97", which starts at dirAt in the paragraph's
	// text.
	dir   string
	dirAt int
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
	ps.items = append(ps.items, goForms(plainItems(ps.masked), ps.masked, p)...)
	ps.finish(text, ps.masked)
	return ps
}

var (
	// reTagLines is a continuation written after its tag,
	// <tag>:<from>[-<to>].
	reTagLines = regexp.MustCompile(`(` + tagForm + `):(\d+)(?:-(\d+))?`)
	// reFileEnd is a file that ends a text, and reLinesStart a line number
	// or a range that starts one, and reRangeStart a range that does.
	reFileEnd    = regexp.MustCompile(goPath + `$`)
	reLinesStart = regexp.MustCompile(`^(\d+)(?:-(\d+))?`)
	reRangeStart = regexp.MustCompile(`^(\d+)-(\d+)`)
	// reProseLines is a line reference in prose, "line <n>" or
	// "lines <from>-<to>", and reMoreLines another line or range listed
	// after one.
	reProseLines = regexp.MustCompile(`[Ll]ines? (\d+)(?:-(\d+))?`)
	reMoreLines  = regexp.MustCompile(`^(?:,? and |,? or |, )(\d+)(?:-(\d+))?`)
)

// goForms returns a Go comment paragraph's plain citations, those plainItems
// found in its masked text ms and the forms only a Go comment is read for: a
// continuation written after its tag, v19.2.6:3706-3720; a file whose lines a
// line break splits from it, as "(rgw_op.cc:" ending a line and "3033-3045)"
// starting the next, or "(version.cc" ending one and "26-34)" starting the
// next; a line reference in prose inside parentheses, "(line 89)",
// "(lines 130-172)" or "(lines 72-78 and 89)", each of its lines a citation;
// and a file named before a ":" that no line follows, as a code block's
// lead-in "in rgw_rados.cc:" names one. A split citation with a ":" must be a
// range or inside parentheses, and one without must be inside them, so that
// "rgw_op.cc:" and a status code, or a file and "2-3 times", is none. In a
// code block a continuation must be a range with a blank, a tab or the line's
// start before it, so that a key's value, "runs":2 or "listen: :7480", is
// none.
func goForms(items []*goItem, ms string, p *commentPara) []*goItem {
	if p.code {
		items = slices.DeleteFunc(items, func(it *goItem) bool {
			return it.kind == contItem && (it.from == it.to || it.start > 0 && ms[it.start-1] != ' ' && ms[it.start-1] != '\t')
		})
	}
	taken := func(s, e int) bool {
		return slices.ContainsFunc(items, func(o *goItem) bool { return s < o.end && o.start < e })
	}
	contBefore := func(c byte) bool { return pathByte(c) || c == ':' || c == ']' || c == '[' }
	contAfter := func(c byte) bool { return wordByte(c) || c == ':' || c == ']' }
	var add []*goItem
	for _, m := range reTagLines.FindAllStringSubmatchIndex(ms, -1) {
		s, e, ok := bounded(ms, m, contBefore, contAfter)
		if !ok || taken(s, e) {
			continue
		}
		it := &goItem{kind: contItem, start: s, end: e, text: ms[s:e], tag: ms[m[2]:m[3]]}
		if it.from, it.to, ok = lines(ms, m, e); ok {
			add = append(add, it)
		}
	}
	for i := 1; i < len(p.at); i++ {
		if it := splitCite(ms, p.at[i]-1); it != nil && !taken(it.start+len(it.name)+1, it.end) {
			items = slices.DeleteFunc(items, func(o *goItem) bool { return o.kind == mentionItem && o.start == it.start })
			add = append(add, it)
		}
	}
	for _, it := range proseLines(ms) {
		if !taken(it.start, it.end) && parenthesized(ms, it.start, it.end) {
			it.parenOnly = true
			add = append(add, it)
		}
	}
	items = append(items, add...)
	for _, ix := range reMention.FindAllStringIndex(ms, -1) {
		s, e := ix[0], ix[1]
		if e == len(ms) || ms[e] != ':' || s > 0 && pathByte(ms[s-1]) || taken(s, e) {
			continue
		}
		if rest := strings.TrimLeft(ms[e+1:], " "); rest != "" && isDigit(rest[0]) {
			continue
		}
		items = append(items, &goItem{kind: mentionItem, start: s, end: e, text: ms[s:e], name: ms[s:e]})
	}
	for _, it := range items {
		it.paren = parenthesized(ms, it.start, it.end)
		it.bare = it.paren && standsBare(ms, it.start, it.end)
	}
	for _, it := range items {
		if it.kind != fileItem || !slices.Contains(p.at[1:], it.start) {
			continue
		}
		if loc := reDirEnd.FindStringIndex(ms[:it.start-1]); len(loc) > 0 && (loc[0] == 0 || !pathByte(ms[loc[0]-1])) {
			it.dir, it.dirAt = ms[loc[0]:loc[1]], loc[0]
		}
	}
	return items
}

// reTagBlank is a tag, or an [S] or [T] marker, and a blank that end a text.
var reTagBlank = regexp.MustCompile(`(?:(?:^|[^\w.\-])` + tagForm + `|\[[ST]\]) $`)

// standsBare reports whether the citation from s to e in ms stands alone
// among the words of its parentheses: after the "(", a ", " or "; ", or a tag
// or a marker and a blank, and before the ")", a "," or ";", or " and " or
// " or ", as "(:101)", "(Parse7, :751)" and "(at v19.2.6 :5067, at v20.2.4
// :5338)" do, and "(default :8080)" does not.
func standsBare(ms string, s, e int) bool {
	before, after := ms[:s], ms[e:]
	if !strings.HasSuffix(before, "(") && !strings.HasSuffix(before, ", ") && !strings.HasSuffix(before, "; ") &&
		!reTagBlank.MatchString(before) {
		return false
	}
	for _, w := range []string{")", ",", ";", " and ", " or "} {
		if strings.HasPrefix(after, w) {
			return true
		}
	}
	return false
}

// parenthesized reports whether the text from s to e in ms stands inside a
// "(" before it and the ")" that closes it after it.
func parenthesized(ms string, s, e int) bool {
	o, _ := parenSpan(ms, s, e)
	return o >= 0
}

// parenSpan returns the "(" before the text from s to e in ms and the ")"
// that closes it after it, or -1 and -1 when it stands inside none.
func parenSpan(ms string, s, e int) (open, closing int) {
	depth := 0
	open = -1
	for i := s - 1; i >= 0; i-- {
		switch ms[i] {
		case ')':
			depth++
		case '(':
			if depth == 0 {
				open = i
			}
			depth--
		}
		if open >= 0 {
			break
		}
	}
	if open < 0 {
		return -1, -1
	}
	depth = 0
	for i := e; i < len(ms); i++ {
		switch ms[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return open, i
			}
			depth--
		}
	}
	return -1, -1
}

// reDirEnd is a path of directories that ends a text, its last byte a "/".
var reDirEnd = regexp.MustCompile(`(?:[\w.\-]+/)+$`)

// splitCite returns the citation of a file whose lines a line break, the
// blank at j in ms that joins two lines, splits from it: a file and a ":"
// ending the first line and a line or a range starting the next, a single
// line only inside parentheses and with ")" or " at <tag>" right after it, or
// a file ending the first and a range starting the next, inside parentheses;
// or nil.
func splitCite(ms string, j int) *goItem {
	before, after := ms[:j], ms[j+1:]
	colon := strings.HasSuffix(before, ":")
	loc := reFileEnd.FindStringIndex(strings.TrimSuffix(before, ":"))
	if len(loc) == 0 || loc[0] > 0 && pathByte(ms[loc[0]-1]) {
		return nil
	}
	re := reRangeStart
	if colon {
		re = reLinesStart
	}
	m := re.FindStringSubmatchIndex(after)
	if len(m) == 0 || m[1] < len(after) && (wordByte(after[m[1]]) || after[m[1]] == ':' || after[m[1]] == '-') {
		return nil
	}
	s, e := loc[0], j+1+m[1]
	it := &goItem{kind: fileItem, start: s, end: e, text: ms[s:e], name: ms[loc[0]:loc[1]]}
	var err error
	if it.from, err = strconv.Atoi(after[m[2]:m[3]]); err != nil {
		return nil
	}
	it.to = it.from
	if m[4] >= 0 {
		if it.to, err = strconv.Atoi(after[m[4]:m[5]]); err != nil {
			return nil
		}
	}
	if (!colon || it.from == it.to) && !parenthesized(ms, s, e) {
		return nil
	}
	if it.from == it.to && !strings.HasPrefix(ms[e:], ")") && !reAtTagStart.MatchString(ms[e:]) {
		return nil
	}
	// Outside parentheses a single line split from its file, or any lines
	// split from a file with no ":", are none.
	it.parenOnly = it.from == it.to || !colon
	return it
}

// reAtTagStart is " at <tag>" starting a text.
var reAtTagStart = regexp.MustCompile(`^ at ` + tagForm + `\b`)

// proseLines returns the line references in prose in ms: "line" or "lines"
// and a line or a range, and each line or range listed after it, when a ")"
// closes them right after their last, as "(lines 72-78 and 89)" does and
// "(line 1 of the response)" does not.
func proseLines(ms string) []*goItem {
	var out []*goItem
	// ends reports whether a line or a range ends at e: no word byte, ":",
	// "-" or a "." and a digit after it.
	ends := func(e int) bool {
		return e == len(ms) || !wordByte(ms[e]) && ms[e] != ':' && ms[e] != '-' &&
			(ms[e] != '.' || e+1 == len(ms) || !isDigit(ms[e+1]))
	}
	number := func(s string, m []int, lo, hi int) (int, bool) {
		if m[lo] < 0 {
			return 0, false
		}
		n, err := strconv.Atoi(s[m[lo]:m[hi]])
		return n, err == nil
	}
	for _, m := range reProseLines.FindAllStringSubmatchIndex(ms, -1) {
		if m[0] > 0 && pathByte(ms[m[0]-1]) || !ends(m[1]) {
			continue
		}
		s, e := m[0], m[1]
		var chain []*goItem
		for {
			it := &goItem{kind: contItem, start: s, end: e, text: ms[s:e], prose: true}
			var ok bool
			if it.from, ok = number(ms, m, 2, 3); !ok {
				break
			}
			if it.to, ok = number(ms, m, 4, 5); !ok {
				it.to = it.from
			}
			chain = append(chain, it)
			next := reMoreLines.FindStringSubmatchIndex(ms[e:])
			if len(next) == 0 || !ends(e+next[1]) {
				break
			}
			for k := range next {
				if next[k] >= 0 {
					next[k] += e
				}
			}
			m, s, e = next, next[2], next[1]
		}
		if len(chain) > 0 && strings.HasPrefix(ms[chain[len(chain)-1].end:], ")") {
			out = append(out, chain...)
		}
	}
	return out
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

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
