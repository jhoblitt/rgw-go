package citecheck

import (
	"bytes"
	"go/scanner"
	"go/token"
	"slices"
	"strings"
	"unicode/utf8"
)

// commentLine is one line of a // comment that holds prose: its number,
// the offsets in the file of its text, after the "//" and the blanks that
// follow it, and the bytes before the text on its line.
type commentLine struct {
	n          int
	start, end int
	prefix     string
}

// commentPara is a run of comment lines read as one text, its lines joined
// by single blanks: a paragraph, a list item, a comment after code, the
// explanation of a //nolint directive, or one line of a code block.
type commentPara struct {
	lines []commentLine
	// wrap reports whether the paragraph is plain prose that --fix may
	// rewrap: unindented lines, not a list item or a heading.
	wrap bool
	// code reports whether the paragraph is a line of a code block, which
	// --fix does not rewrite.
	code bool
	text string
	// at holds the offset in text of each line's first byte.
	at []int
}

// commentGroup is a run of // comments on consecutive lines with nothing
// else on them, or a single comment after code: the "comment" a
// continuation looks back through for its file.
type commentGroup struct {
	paras []*commentPara
}

// goComments returns the text of a Go file's // comments, group by group.
// A cgo preamble is C, not prose, so neither a block comment nor the //
// lines right above an import of "C" are read; nor are directives, such as
// //go:build, or a line with no blank after its "//", but for the
// explanation after the " // " of a //nolint directive. A code block, a
// span of indented lines that does not start with a list marker, which
// go/doc/comment would print as code, is read a line at a time.
func goComments(src []byte) []*commentGroup {
	var groups []*commentGroup
	starts := lineStarts(src)
	comments, cgo := lineComments(src)
	var run []int // offsets of the "//" of a run of full-line comments
	lastLine := -1
	flush := func() {
		if len(run) > 0 && !cgo[lastLine+1] {
			groups = append(groups, groupOf(src, starts, run))
		}
		run = nil
	}
	for _, off := range comments {
		n := lineNumber(starts, off)
		if !fullLine(src, off) {
			flush()
			if p := trailingPara(src, off, n); p != nil {
				groups = append(groups, &commentGroup{paras: []*commentPara{p}})
			}
			lastLine = -1
			continue
		}
		if n != lastLine+1 {
			flush()
		}
		run = append(run, off)
		lastLine = n
	}
	flush()
	return groups
}

// lineComments returns the offset of the "//" of every line comment in
// src, as go/scanner finds them, so that one inside a string is not, and the
// lines an import of "C" starts on: "import" before a lone "C", or the "C"
// of an import list.
func lineComments(src []byte) (offs []int, cgo map[int]bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) {}, scanner.ScanComments)
	cgo = map[int]bool{}
	importAt, inImports := token.NoPos, false
	for {
		pos, tok, lit := s.Scan()
		switch {
		case tok == token.EOF:
			return offs, cgo
		case tok == token.COMMENT:
			if strings.HasPrefix(lit, "//") {
				offs = append(offs, file.Offset(pos))
			}
			continue
		case tok == token.IMPORT:
			importAt = pos
			continue
		case tok == token.LPAREN && importAt.IsValid():
			inImports = true
		case tok == token.RPAREN:
			inImports = false
		case tok == token.STRING && lit == `"C"` && importAt.IsValid():
			cgo[file.Line(importAt)] = true
		case tok == token.STRING && lit == `"C"` && inImports:
			cgo[file.Line(pos)] = true
		}
		importAt = token.NoPos
	}
}

// lineStarts returns the offset of each line's first byte in src.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' && i+1 < len(src) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineNumber returns the line, counting from 1, that the offset off is on.
func lineNumber(starts []int, off int) int {
	n, found := slices.BinarySearch(starts, off)
	if found {
		return n + 1
	}
	return n
}

func lineStartOf(src []byte, off int) int { return bytes.LastIndexByte(src[:off], '\n') + 1 }

// lineEndOf returns the offset of the end of the line holding off: its
// newline, or a carriage return before it, or the end of src.
func lineEndOf(src []byte, off int) int {
	end := len(src)
	if k := bytes.IndexByte(src[off:], '\n'); k >= 0 {
		end = off + k
	}
	if end > off && src[end-1] == '\r' {
		end--
	}
	return end
}

// fullLine reports whether only blanks come before the comment at off on
// its line.
func fullLine(src []byte, off int) bool {
	return len(bytes.TrimLeft(src[lineStartOf(src, off):off], " \t")) == 0
}

// lineKind is how a full-line comment reads.
type lineKind int

const (
	blankLine lineKind = iota
	// rawLine is a directive, or text with no blank after the "//".
	rawLine
	textLine
	indentedLine
)

// classify returns how the comment at off reads and where its text starts:
// after "// " for an unindented line, and after every blank for an
// indented one.
func classify(src []byte, off int) (kind lineKind, text int) {
	end := lineEndOf(src, off)
	body := src[off+2 : end]
	switch {
	case len(bytes.TrimSpace(body)) == 0:
		return blankLine, end
	case body[0] != ' ' && body[0] != '\t':
		return rawLine, end
	case body[0] == ' ' && body[1] != ' ' && body[1] != '\t':
		return textLine, off + 3
	}
	return indentedLine, off + 2 + len(body) - len(bytes.TrimLeft(body, " \t"))
}

// groupOf reads a run of full-line comments into paragraphs: plain
// paragraphs, which a blank comment line, a directive, a list item or an
// indented line ends; list items, an indented or unindented list marker's
// line and the lines that continue it; headings; a //nolint directive's
// explanation; and each line of a code block.
func groupOf(src []byte, starts, run []int) *commentGroup {
	g := &commentGroup{}
	var cur *commentPara
	inCode, inList := false, false
	start := func(wrap bool) {
		cur = &commentPara{wrap: wrap}
		g.paras = append(g.paras, cur)
	}
	for _, off := range run {
		kind, text := classify(src, off)
		end := lineEndOf(src, off)
		n := lineNumber(starts, off)
		line := commentLine{n: n, start: text, end: end, prefix: string(src[lineStartOf(src, off):text])}
		body := string(src[text:end])
		// alone is the line as a paragraph of its own: a code block's line,
		// or a directive's explanation.
		alone := func(code bool) {
			cur = &commentPara{code: code}
			cur.add(line, body)
			g.paras = append(g.paras, cur)
			cur = nil
		}
		switch kind {
		case blankLine:
			cur = nil
			continue
		case rawLine:
			cur, inCode, inList = nil, false, false
			if p := lintExplanation(src, off, n); p != nil {
				g.paras = append(g.paras, p)
			}
			continue
		case textLine:
			inCode, inList = false, false
			switch {
			case listItem(body):
				start(false)
			case heading(body):
				start(false)
				cur.add(line, body)
				cur = nil
				continue
			case cur == nil:
				start(true)
			}
		case indentedLine:
			switch {
			case inCode:
				alone(true)
				continue
			case listItem(body):
				inList = true
				start(false)
			case inList || cur != nil && !cur.wrap:
				if cur == nil {
					start(false)
				}
			default:
				inCode = true
				alone(true)
				continue
			}
		}
		cur.add(line, body)
	}
	return g
}

// trailingPara returns the prose of a comment after code, the explanation of
// a //nolint directive after code, or nil for another directive.
func trailingPara(src []byte, off, n int) *commentPara {
	kind, text := classify(src, off)
	if kind == rawLine {
		return lintExplanation(src, off, n)
	}
	if kind != textLine && kind != indentedLine {
		return nil
	}
	end := lineEndOf(src, off)
	p := &commentPara{}
	p.add(commentLine{n: n, start: text, end: end, prefix: string(src[lineStartOf(src, off):text])}, string(src[text:end]))
	return p
}

// lintExplanation returns the explanation of the //nolint directive at off, on
// line n, the text after the first " // " in it, as a paragraph of its own,
// or nil when the comment is no //nolint directive or explains nothing.
func lintExplanation(src []byte, off, n int) *commentPara {
	end := lineEndOf(src, off)
	body := src[off+2 : end]
	name := body
	if i := bytes.IndexAny(body, ": "); i >= 0 {
		name = body[:i]
	}
	k := bytes.Index(body, []byte(" // "))
	if string(name) != "nolint" || k < 0 {
		return nil
	}
	rest := body[k+4:]
	text := off + 2 + k + 4 + len(rest) - len(bytes.TrimLeft(rest, " \t"))
	if text == end {
		return nil
	}
	p := &commentPara{}
	p.add(commentLine{n: n, start: text, end: end, prefix: string(src[lineStartOf(src, off):text])}, string(src[text:end]))
	return p
}

func (p *commentPara) add(line commentLine, body string) {
	if len(p.lines) > 0 {
		p.text += " "
	}
	p.at = append(p.at, len(p.text))
	p.text += body
	p.lines = append(p.lines, line)
}

// lineAt returns the index of the line the offset off in the text is on;
// the blank that joins two lines is the first's.
func (p *commentPara) lineAt(off int) int {
	i := len(p.at) - 1
	for i > 0 && p.at[i] > off {
		i--
	}
	return i
}

// listItem reports whether a comment line's text starts with a list
// marker, as go/doc/comment reads one: a bullet, "-", "*", "+" or "•", or a
// number and "." or ")", then a blank and more text.
func listItem(text string) bool {
	t := strings.TrimLeft(text, " \t")
	var rest string
	if r, n := utf8.DecodeRuneInString(t); r == '•' || r == '*' || r == '+' || r == '-' {
		rest = t[n:]
	} else {
		n := 0
		for n < len(t) && '0' <= t[n] && t[n] <= '9' {
			n++
		}
		if n == 0 || n >= len(t) || t[n] != '.' && t[n] != ')' {
			return false
		}
		rest = t[n+1:]
	}
	return rest != "" && (rest[0] == ' ' || rest[0] == '\t') && strings.TrimSpace(rest) != ""
}

// heading reports whether a comment line's text is a doc comment heading.
func heading(text string) bool {
	return len(text) >= 2 && text[0] == '#' && (text[1] == ' ' || text[1] == '\t') && strings.TrimSpace(text) != "#"
}
