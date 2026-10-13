// Package citecheck finds the Ceph source citations in markdown files and
// checks each against a clone of ceph/ceph at the release it names.
// hack/citecheck's package doc is the home of the citation forms, of what is
// checked and of what is not.
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

// Citation is one citation a markdown file holds.
type Citation struct {
	// Line is the markdown line the citation starts on, counting from 1.
	Line int
	// Text is the citation as written: a code span's content or a URL.
	Text string
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

// Scan returns the citations in a markdown document, in the order they
// appear. Fenced code blocks are skipped whole; outside them, every code span
// whose content is an inline citation and every GitHub link to a line of
// ceph/ceph is one.
func Scan(data []byte) []Citation {
	var cites []Citation
	var para paragraph
	flush := func() {
		cites = append(cites, para.spans()...)
		para = paragraph{}
	}
	var fence string
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
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
		cites = append(cites, links(line, n)...)
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if startsBlock(line) {
			flush()
		}
		para.add(line, n)
		if isHeading(line) {
			flush()
		}
	}
	flush()
	slices.SortStableFunc(cites, func(a, b Citation) int { return cmp.Compare(a.Line, b.Line) })
	return cites
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
	text   strings.Builder
	starts []int
	lines  []int
}

func (p *paragraph) add(line string, n int) {
	if p.text.Len() > 0 {
		p.text.WriteByte('\n')
	}
	p.starts = append(p.starts, p.text.Len())
	p.lines = append(p.lines, n)
	p.text.WriteString(line)
}

// lineAt returns the markdown line of a byte offset in the paragraph.
func (p *paragraph) lineAt(off int) int {
	i, found := slices.BinarySearch(p.starts, off)
	if found {
		return p.lines[i]
	}
	return p.lines[i-1]
}

// spans returns the inline citations among the paragraph's code spans. A
// run of backticks opens a span that the next run of as many closes; a run
// with no such closer is literal text, as is a backtick escaped by a
// backslash outside a span.
func (p *paragraph) spans() []Citation {
	s := p.text.String()
	var cites []Citation
	for i := 0; i < len(s); {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] != '`' {
			i++
			continue
		}
		run := backticks(s, i)
		end := closingRun(s, i+run, run)
		if end < 0 {
			i += run
			continue
		}
		content := strings.TrimSpace(strings.ReplaceAll(s[i+run:end], "\n", " "))
		if c, ok := parseInline(content); ok {
			c.Line = p.lineAt(i)
			cites = append(cites, c)
		}
		i = end + run
	}
	return cites
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
	c := Citation{Text: content, Path: m[1], Ref: m[4]}
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

// links returns the GitHub line citations one markdown line holds.
func links(line string, n int) []Citation {
	var cites []Citation
	for _, m := range reLink.FindAllStringSubmatch(line, -1) {
		c := Citation{Line: n, Text: m[0], Ref: m[1]}
		path, err := url.PathUnescape(m[2])
		if err != nil {
			c.Problem = fmt.Sprintf("path %q is not percent-encoded correctly", m[2])
		}
		c.Path = path
		a := reAnchor.FindStringSubmatch(m[3])
		switch {
		case c.Problem != "":
		case a == nil:
			c.Problem = fmt.Sprintf("anchor #%s is not #L<line> or #L<from>-L<to>", m[3])
		default:
			c.From, c.To, c.Problem = lineRange(a[1], a[2])
		}
		cites = append(cites, c)
	}
	return cites
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
