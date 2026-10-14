// Package citecheck finds the Ceph source citations in markdown files,
// checks each against a clone of ceph/ceph at the release it names, and
// rewrites the code spans that cite Ceph into links. hack/citecheck's package
// doc is the home of the citation form, of what is checked and of what is
// not.
package citecheck

import (
	"cmp"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Form is how a citation is written.
type Form int

const (
	// Link is a GitHub link to lines of ceph/ceph, the one citation form.
	Link Form = iota
	// CodeSpan is a code span naming a path, lines and a ref, which only
	// --fix accepts, as the input it rewrites into a link.
	CodeSpan
)

// Citation is one citation a markdown file holds.
type Citation struct {
	// Line is the markdown line the citation starts on, counting from 1.
	Line int
	// Text is the citation as written: a code span's content or a URL.
	Text string
	// Form is how the citation is written.
	Form Form
	// Ref is the git revision cited, a tag or a commit id.
	Ref string
	// Path is the file cited, relative to the repository's root.
	Path string
	// From and To are the first and last line cited, counting from 1.
	From, To int
	// Symbol is the C++ name the citation says the lines hold, or "".
	Symbol string
	// Problem says why a citation is malformed, or is "".
	Problem string
	// Start and End are the offsets of a code span's first byte and of the
	// byte after its last in the document, backticks included; both are 0
	// for a link.
	Start, End int
	// NoFix says why --fix cannot rewrite a code span into a link where it
	// stands, or is "".
	NoFix string
}

// stops are the characters no cited path or ref holds: blanks, control and
// format characters, and those that end a citation in markdown.
const stops = "\\s\\p{C}<>()\\[\\]\"'`"

var (
	// reInline is a code span's whole content as a citation:
	// <path>:<line>[-<line>]@<ref>[#<symbol>], the path empty in a malformed
	// one.
	reInline = regexp.MustCompile(`^([^:@#` + stops + `]*):(\d+)(?:-(\d+))?@([A-Za-z0-9][A-Za-z0-9._/+-]*)(?:#(.*))?$`)
	// reLink is a GitHub link into ceph/ceph's tree with a line anchor:
	// the ref is the URL's first segment after blob/, and the anchor is the
	// longest run of letters, digits and "-" after #L, judged by reAnchor.
	reLink = regexp.MustCompile(`https://github\.com/ceph/ceph/blob/([^/?#` + stops + `]+)/([^?#` + stops + `]+)` +
		`(?:\?[^#` + stops + `]*)?#(L[A-Za-z0-9-]*)`)
	// reAnchor is a line anchor GitHub highlights: #L<n> or #L<from>-L<to>,
	// each line optionally with the column GitHub's selection adds.
	reAnchor = regexp.MustCompile(`^L(\d+)(?:C\d+)?(?:-L(\d+)(?:C\d+)?)?$`)
	// reSymbol is a possibly qualified C++ name: identifiers, a destructor
	// or an operator, joined by "::".
	reSymbol = regexp.MustCompile(`^(?:::)?(?:~?[A-Za-z_][A-Za-z0-9_]*::)*(?:~?[A-Za-z_][A-Za-z0-9_]*|operator[^\sA-Za-z0-9_]+)$`)
)

// inCodeSpan is why a link a code span holds fails: it is not a link.
const inCodeSpan = notALink + ": a code span holds it, which renders it as code: take it out of the backticks"

// Scan returns the citations in a markdown document, in the order they
// appear. Fenced code blocks are skipped whole; outside them, every code span
// whose content is an inline citation and every GitHub link to a line of
// ceph/ceph is one. A link that a code span holds, unless the span is a
// link's text, is malformed: it renders as code.
func Scan(data []byte) []Citation {
	var cites []Citation
	var para paragraph
	// at holds each link's index in cites and its offset in the document,
	// and code the document's range of each code span not in a link's text,
	// in order.
	var at, code [][2]int
	flush := func() {
		c, r := para.spans()
		cites = append(cites, c...)
		code = append(code, r...)
		para = paragraph{}
	}
	var fence string
	next := 0
	for i, line := range strings.Split(string(data), "\n") {
		n, start := i+1, next
		next += len(line) + 1
		line = strings.TrimSuffix(line, "\r")
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if f := opensFence(line); f != "" {
			flush()
			fence = f
			continue
		}
		ls, offs := links(line, n)
		for k, off := range offs {
			at = append(at, [2]int{len(cites) + k, start + off})
		}
		cites = append(cites, ls...)
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if startsBlock(line) {
			flush()
		}
		para.add(line, n, start)
		if isHeading(line) {
			flush()
		}
	}
	flush()
	for _, a := range at {
		if within(code, a[1]) {
			cites[a[0]].Problem = inCodeSpan
		}
	}
	slices.SortStableFunc(cites, func(a, b Citation) int { return cmp.Compare(a.Line, b.Line) })
	return cites
}

// within reports whether off is in one of ranges, which are in order and do
// not overlap.
func within(ranges [][2]int, off int) bool {
	i, found := slices.BinarySearchFunc(ranges, off, func(r [2]int, off int) int { return cmp.Compare(r[0], off) })
	return found || i > 0 && off < ranges[i-1][1]
}

// opensFence returns the fence a line opens, three or more backticks or
// tildes after any indentation, or "" when it opens none. A backtick fence's
// info string holds no backtick, or the line is a code span instead.
func opensFence(line string) string {
	t := strings.TrimLeft(line, " \t")
	for _, c := range []string{"`", "~"} {
		run := len(t) - len(strings.TrimLeft(t, c))
		if run < 3 {
			continue
		}
		if c == "`" && strings.Contains(t[run:], "`") {
			return ""
		}
		return t[:run]
	}
	return ""
}

// closesFence reports whether a line closes fence: at least as many of its
// characters after any indentation, and nothing else but blanks.
func closesFence(line, fence string) bool {
	t := strings.TrimLeft(line, " \t")
	run := len(t) - len(strings.TrimLeft(t, fence[:1]))
	return run >= len(fence) && strings.TrimSpace(t[run:]) == ""
}

var reBlockStart = regexp.MustCompile(`^\s*(?:[-*+]\s|\d{1,9}[.)]\s|#{1,6}(?:\s|$)|\|)`)

// startsBlock reports whether a line starts a block a code span cannot run
// into from the line before: a list item, a heading or a table row.
func startsBlock(line string) bool { return reBlockStart.MatchString(line) }

var reHeading = regexp.MustCompile(`^\s*#{1,6}(?:\s|$)`)

func isHeading(line string) bool { return reHeading.MatchString(line) }

// paragraph is the run of lines code spans are paired within.
type paragraph struct {
	text strings.Builder
	// starts, lines and offsets are each line's offset in text, its number
	// and its offset in the document.
	starts, lines, offsets []int
}

func (p *paragraph) add(line string, n, offset int) {
	if p.text.Len() > 0 {
		p.text.WriteByte('\n')
	}
	p.starts = append(p.starts, p.text.Len())
	p.lines = append(p.lines, n)
	p.offsets = append(p.offsets, offset)
	p.text.WriteString(line)
}

// index returns the index of the line a byte offset in the paragraph is on.
func (p *paragraph) index(off int) int {
	i, found := slices.BinarySearch(p.starts, off)
	if found {
		return i
	}
	return i - 1
}

// offset returns the document's offset of a byte offset in the paragraph.
func (p *paragraph) offset(off int) int {
	i := p.index(off)
	return p.offsets[i] + off - p.starts[i]
}

// codeSpan is where a code span stands in a paragraph: its first and last
// backtick, and whether an unescaped "!" is right before it.
type codeSpan struct {
	first, last int
	bang        bool
}

// spans returns the inline citations among the paragraph's code spans, and
// the document's range of each code span, citation or not, that is not in a
// link's text. A run of backticks opens a span that the next run of as many
// closes; a run with no such closer is literal text, as is a backtick, a
// bracket or a "!" escaped by a backslash outside a span. A link's text is a
// bracketed run, the brackets paired outside spans, whose "]" a "(" or a "["
// follows.
func (p *paragraph) spans() (cites []Citation, code [][2]int) {
	s := p.text.String()
	// paired holds every code span, cited the index in paired of each
	// citation's, opens the "[" not yet closed, texts each link text's
	// brackets, and bang the last "!".
	var paired []codeSpan
	var cited, opens []int
	var texts [][2]int
	bang := -1
	for i := 0; i < len(s); {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '!':
			bang = i
		case '[':
			opens = append(opens, i)
		case ']':
			if len(opens) == 0 {
				break
			}
			open := opens[len(opens)-1]
			opens = opens[:len(opens)-1]
			if i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '[') {
				texts = append(texts, [2]int{open, i})
			}
		case '`':
			run := backticks(s, i)
			end := closingRun(s, i+run, run)
			if end < 0 {
				i += run
				continue
			}
			paired = append(paired, codeSpan{first: i, last: end + run - 1, bang: i > 0 && bang == i-1})
			content := strings.TrimSpace(strings.ReplaceAll(s[i+run:end], "\n", " "))
			if c, ok := parseInline(content); ok {
				c.Line = p.lines[p.index(i)]
				c.Start, c.End = p.offset(i), p.offset(end+run-1)+1
				cites = append(cites, c)
				cited = append(cited, len(paired)-1)
			}
			i = end + run
			continue
		}
		i++
	}
	inText := func(sp codeSpan) bool {
		return slices.ContainsFunc(texts, func(t [2]int) bool { return t[0] < sp.first && sp.first < t[1] })
	}
	for k, j := range cited {
		sp := paired[j]
		cites[k].NoFix = noFix(cites[k].Ref, p.index(sp.first) != p.index(sp.last), inText(sp), sp.bang)
	}
	for _, sp := range paired {
		if !inText(sp) {
			code = append(code, [2]int{p.offset(sp.first), p.offset(sp.last) + 1})
		}
	}
	return cites, code
}

// noFix returns why --fix cannot rewrite a code span citation into a link
// where it stands, or "": the span runs across a line break, or is in a
// link's text, or follows a "!", or its ref holds a "/", which reLink reads
// as the path's.
func noFix(ref string, broken, inLinkText, afterBang bool) string {
	switch {
	case broken:
		return "the code span runs across a line break, which rewriting it would rewrap: put it on one line"
	case inLinkText:
		return "the code span is in a link's text, which cannot hold another link: write the text without it"
	case afterBang:
		return `the code span follows a "!", which would make the link an image: escape it as "\!"`
	case strings.Contains(ref, "/"):
		return fmt.Sprintf("ref %s holds a \"/\", which a link cannot tell from the path after it: cite its commit id", ref)
	}
	return ""
}

// backticks returns the length of the run of backticks at s[i].
func backticks(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun returns the offset of the first run of exactly n backticks at
// or after from, or -1.
func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := backticks(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

// parseInline parses a code span's content as an inline citation.
func parseInline(content string) (Citation, bool) {
	m := reInline.FindStringSubmatch(content)
	if m == nil {
		return Citation{}, false
	}
	c := Citation{Text: content, Form: CodeSpan, Path: m[1], Ref: m[4]}
	if c.Path == "" {
		c.Problem = fmt.Sprintf("the citation has no path: write the file's path from ceph's repository root before %q", content)
		return c, true
	}
	c.From, c.To, c.Problem = lineRange(m[2], m[3])
	if c.Problem == "" && strings.Contains(content, "#") {
		c.Symbol = m[5]
		if !reSymbol.MatchString(c.Symbol) {
			c.Problem = fmt.Sprintf("symbol %q is not a C++ name", c.Symbol)
		}
	}
	return c, true
}

// links returns the GitHub line citations one markdown line holds, and the
// offset in the line each starts at.
func links(line string, n int) (cites []Citation, offs []int) {
	for _, ix := range reLink.FindAllStringSubmatchIndex(line, -1) {
		m := func(g int) string { return line[ix[2*g]:ix[2*g+1]] }
		c := Citation{Line: n, Text: m(0), Ref: m(1)}
		path, err := url.PathUnescape(m(2))
		if err != nil {
			c.Problem = fmt.Sprintf("path %q is not percent-encoded correctly", m(2))
		}
		c.Path = path
		a := reAnchor.FindStringSubmatch(m(3))
		switch {
		case c.Problem != "":
		case a == nil:
			c.Problem = fmt.Sprintf("anchor #%s is not #L<line> or #L<from>-L<to>", m(3))
		default:
			c.From, c.To, c.Problem = lineRange(a[1], a[2])
		}
		cites = append(cites, c)
		offs = append(offs, ix[0])
	}
	return cites, offs
}

// lineRange parses a citation's first and, when given, last line.
func lineRange(from, to string) (first, last int, problem string) {
	f, err := strconv.Atoi(from)
	if err != nil {
		return 0, 0, fmt.Sprintf("line %s is out of range", from)
	}
	if to == "" {
		return f, f, ""
	}
	t, err := strconv.Atoi(to)
	if err != nil {
		return 0, 0, fmt.Sprintf("line %s is out of range", to)
	}
	return f, t, ""
}
