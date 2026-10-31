package citecheck

import (
	"fmt"
	"slices"
	"strings"
)

// file is one cited file at one commit: its text and where its lines
// start, and, from the first symbol check that needs them, its code and its
// brace blocks.
type file struct {
	text   string
	starts []int

	code   string
	blocks []block
	opens  map[int]int
	// The content check's caches: holds whether each identifier it asked
	// for stands in the text; and defs the function definitions by name,
	// once functions reads them.
	holds map[string]bool
	defs  map[string][]block
}

// block is a pair of braces in a file's code: the offsets of its header's
// first byte, of "{", and of the "}" that closes it, or of the end of the
// file when none does. A scope is a class, struct, union or namespace body,
// or an extern "C" block, and names holds the names its header gives it.
type block struct {
	head, open, close int
	scope             bool
	names             []string
}

func newFile(text string) *file {
	f := &file{text: text}
	if text == "" {
		return f
	}
	f.starts = []int{0}
	for i := range len(text) - 1 {
		if text[i] == '\n' {
			f.starts = append(f.starts, i+1)
		}
	}
	return f
}

// lines returns how many lines the file has; a last line with no newline
// counts.
func (f *file) lines() int { return len(f.starts) }

// lineStart returns the offset of line n's first byte.
func (f *file) lineStart(n int) int { return f.starts[n-1] }

// lineEnd returns the offset just past line n's last byte, its newline
// excluded.
func (f *file) lineEnd(n int) int {
	if n == len(f.starts) {
		return len(strings.TrimSuffix(f.text, "\n"))
	}
	return f.starts[n] - 1
}

// lineOf returns the line an offset is on.
func (f *file) lineOf(off int) int {
	n, found := slices.BinarySearch(f.starts, off)
	if found {
		return n + 1
	}
	return n
}

// symbolProblem returns why lines from to to of the file hold neither sym
// nor sit in a block, its header or its body, whose header names it, or ""
// when they do. Only sym's last component is searched for; its qualifier is
// compared with each match's own (see qualifier).
func (f *file) symbolProblem(sym string, from, to int) string {
	parts := strings.Split(strings.TrimPrefix(sym, "::"), "::")
	name, qual := parts[len(parts)-1], parts[:len(parts)-1]

	f.analyze()
	lo, hi := f.lineStart(from), f.lineEnd(to)
	var other string
	for _, at := range occurrences(f.text, lo, hi, name) {
		q := f.qualifier(f.text, at, lo)
		if compatible(qual, q) {
			return ""
		}
		if other == "" {
			other = strings.Join(append(q, name), "::")
		}
	}

	nearest := ""
	for _, b := range f.enclosing(from, to) {
		for _, at := range occurrences(f.code, b.head, b.open, name) {
			q := f.qualifier(f.code, at, b.head)
			if compatible(qual, q) {
				return ""
			}
			if nearest == "" {
				nearest = fmt.Sprintf("%s at line %d", strings.Join(append(q, name), "::"), f.lineOf(at))
			}
		}
	}
	if nearest != "" {
		return fmt.Sprintf("the nearest block enclosing the cited lines that names %s is %s, not %s", name, nearest, sym)
	}
	if other != "" {
		return fmt.Sprintf("the cited lines name only %s, and no block enclosing them is headed by %s", other, name)
	}
	return fmt.Sprintf("the cited lines do not hold %s, and no block enclosing them is headed by it", name)
}

// compatible reports whether a match's qualifier agrees with a symbol's: an
// unqualified symbol or match agrees with any, and otherwise the shorter must
// be the longer's trailing components, so that a citation may leave out a
// namespace and a definition may be written inside one.
func compatible(sym, match []string) bool {
	n := min(len(sym), len(match))
	return slices.Equal(sym[len(sym)-n:], match[len(match)-n:])
}

// qualifier returns the qualifier of the name at offset at in s, which is the
// file's text or its code, reading no further back than lo: the one written
// before it, and, when the name is declared where it stands, the names of the
// scopes it is declared in, outermost first. So a method defined inside its
// class is qualified by the class, as one defined outside it is by what is
// written. A name in a comment, a literal or a directive, which the code
// blanks, declares nothing.
func (f *file) qualifier(s string, at, lo int) []string {
	written, start := writtenQualifier(s, at, lo)
	if f.code[at] != f.text[at] {
		return written
	}
	scopes, ok := f.scopesAt(at)
	if !ok || !f.declarator(start) {
		return written
	}
	return append(scopes, written...)
}

// declarator reports whether the qualified name starting at offset start is
// one a declaration or definition gives: not after "->", so not a trailing
// return type; outside every parenthesis of its statement or header, so not a
// parameter or an argument; after no "=" there, so not in an initializer; and
// before any ":" there that neither halves a "::" nor ends an access label,
// so not a base class in a base clause or a member-initializer list. An "="
// inside a template's angle brackets, a default template argument's, does not
// count.
func (f *file) declarator(start int) bool {
	if k := skipBlanksBack(f.code, start, 0); k > 1 && f.code[k-2:k] == "->" {
		return false
	}
	from := f.boundary(start)
	depth, angle := 0, 0
	for i := from; i < start; i++ {
		switch f.code[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '<':
			angle++
		case '>':
			angle = max(angle-1, 0)
		case '=':
			if depth == 0 && angle == 0 {
				return false
			}
		case ':':
			if i+1 < len(f.code) && f.code[i+1] == ':' {
				i++
				continue
			}
			if depth == 0 && !slices.Contains([]string{"public", "protected", "private"}, previousWord(f.code, i)) {
				return false
			}
		}
	}
	return depth == 0
}

// scopesAt returns the names of the scopes enclosing offset at, outermost
// first, and false when a block that is not a scope, such as a function's
// body, encloses it.
func (f *file) scopesAt(at int) ([]string, bool) {
	var names []string
	for _, b := range f.blocks {
		if b.open >= at {
			break
		}
		if b.close <= at {
			continue
		}
		if !b.scope {
			return nil, false
		}
		names = append(names, b.names...)
	}
	return names, true
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// occurrences returns the offsets of name in s[lo:hi] where it stands as a
// whole name: not inside a longer identifier, not a destructor's name when
// name is not one, and not a qualifier, as X is in X::y and X<T>::y.
func occurrences(s string, lo, hi int, name string) []int {
	var at []int
	for i := lo; i <= hi-len(name); {
		j := strings.Index(s[i:hi], name)
		if j < 0 {
			break
		}
		j += i
		i = j + 1
		if isIdent(name[0]) && j > 0 && (isIdent(s[j-1]) || s[j-1] == '~') {
			continue
		}
		if e := j + len(name); isIdent(name[len(name)-1]) && (e < len(s) && isIdent(s[e]) || qualifies(s, e)) {
			continue
		}
		at = append(at, j)
	}
	return at
}

// qualifies reports whether the name ending at e is followed by "::",
// after any blanks and template arguments on its line.
func qualifies(s string, e int) bool {
	e = skipBlanks(s, e)
	if e < len(s) && s[e] == '<' {
		if e = templateEnd(s, e); e < 0 {
			return false
		}
		e = skipBlanks(s, e+1)
	}
	return strings.HasPrefix(s[e:], "::")
}

// templateEnd returns the offset of the ">" that closes the "<" at lt on its
// line, or -1.
func templateEnd(s string, lt int) int {
	depth := 0
	for i := lt; i < len(s) && s[i] != '\n'; i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// skipBlanks returns the offset of the first byte at or after i that is not
// blank.
func skipBlanks(s string, i int) int {
	for i < len(s) && isBlank(s[i]) {
		i++
	}
	return i
}

// writtenQualifier returns the components of the qualifier written before
// the name at offset at, none of them before lo, and the offset the qualified
// name starts at: for "Read::prepare" or "Read<T>::prepare", ["Read"].
// Template arguments are dropped, and blanks around "::" are allowed.
func writtenQualifier(s string, at, lo int) (quals []string, start int) {
	start = at
	for j := at; ; {
		k := skipBlanksBack(s, j, lo)
		if k-2 < lo || s[k-2:k] != "::" {
			break
		}
		k = skipBlanksBack(s, k-2, lo)
		if k > lo && s[k-1] == '>' {
			if k = templateStart(s, k-1, lo); k < 0 {
				break
			}
			k = skipBlanksBack(s, k, lo)
		}
		e := k
		for k > lo && isIdent(s[k-1]) {
			k--
		}
		if k == e {
			break
		}
		quals = append([]string{s[k:e]}, quals...)
		j, start = k, k
	}
	return quals, start
}

// skipBlanksBack returns the offset just past the last non-blank byte
// before i, no further back than lo.
func skipBlanksBack(s string, i, lo int) int {
	for i > lo && isBlank(s[i-1]) {
		i--
	}
	return i
}

// templateStart returns the offset of the "<" that the ">" at gt closes, no
// further back than lo, or -1.
func templateStart(s string, gt, lo int) int {
	depth := 0
	for i := gt; i >= lo; i-- {
		switch s[i] {
		case '>':
			depth++
		case '<':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// enclosing returns the blocks whose header starts on line from or before
// it and whose "}" is on line to or after it, the innermost first.
func (f *file) enclosing(from, to int) []block {
	first, last := f.lineEnd(from), f.lineStart(to)
	var out []block
	for _, b := range f.blocks {
		if b.head < first && b.close >= last {
			out = append(out, b)
		}
	}
	slices.Reverse(out)
	return out
}

// header returns the offset of the first byte of the header of the block
// opening at open: the code after the ";", "{" or "}" before it, blanks left
// out.
func (f *file) header(open int) int { return skipBlanks(f.code, f.boundary(open)) }

// boundary returns the offset just past the ";", "{" or "}" before offset at
// in the code, or 0: where the statement or block header holding at starts.
// A brace pair followed by ",", "{" or ")" is part of it, as a constructor's
// member initializer "x{a}," or an argument "T{}" is.
func (f *file) boundary(at int) int {
	for i := at - 1; i >= 0; i-- {
		switch f.code[i] {
		case ';', '{':
			return i + 1
		case '}':
			j := skipBlanks(f.code, i+1)
			o, ok := f.opens[i]
			if !ok || j == len(f.code) || strings.IndexByte(",{)", f.code[j]) < 0 {
				return i + 1
			}
			i = o
		}
	}
	return 0
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// analyze computes the file's code and its blocks, once.
func (f *file) analyze() {
	if f.opens != nil {
		return
	}
	f.code = codeOf(f.text)
	f.opens = map[int]int{}
	var stack []int
	for i := range len(f.code) {
		switch f.code[i] {
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) == 0 {
				continue
			}
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f.blocks = append(f.blocks, block{open: o, close: i})
			f.opens[i] = o
		}
	}
	for _, o := range stack {
		f.blocks = append(f.blocks, block{open: o, close: len(f.code)})
	}
	slices.SortFunc(f.blocks, func(a, b block) int { return a.open - b.open })
	for i := range f.blocks {
		b := &f.blocks[i]
		b.head = f.header(b.open)
		b.scope, b.names = scopeOf(f.code[b.head:b.open])
	}
}

// codeOf returns text with every byte that is not code blanked, newlines
// kept, so that offsets and lines carry over: comments, the contents of
// string and character literals, preprocessor directives, and every branch
// of a preprocessor conditional after its first, whose braces would
// otherwise pair with the first branch's.
func codeOf(text string) string {
	b := []byte(text)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	depth, skipFrom := 0, 0
	atLineStart := true
	for i := 0; i < len(text); {
		if atLineStart {
			atLineStart = false
			j := i
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			end := lineEndAt(text, i)
			switch {
			case j < len(text) && text[j] == '#':
				end = directiveEnd(text, j)
				depth, skipFrom = conditional(directiveName(text, j), depth, skipFrom)
			case skipFrom == 0:
				i = j
				continue
			}
			blank(i, end)
			i = end
			continue
		}
		var end int
		switch c := text[i]; {
		case c == '\n':
			atLineStart = true
			end = i + 1
		case strings.HasPrefix(text[i:], "//"):
			end = lineEndAt(text, i)
		case strings.HasPrefix(text[i:], "/*"):
			end = len(text)
			if k := strings.Index(text[i+2:], "*/"); k >= 0 {
				end = i + 2 + k + 2
			}
		case c == '"':
			end = stringEnd(text, i)
		case c == '\'' && !digitSeparator(text, i):
			end = charEnd(text, i)
		default:
			i++
			continue
		}
		blank(i, end)
		i = end
	}
	return string(b)
}

// lineEndAt returns the offset of the newline that ends the line holding i,
// or the end of text.
func lineEndAt(text string, i int) int {
	if k := strings.IndexByte(text[i:], '\n'); k >= 0 {
		return i + k
	}
	return len(text)
}

// directiveEnd returns where the directive starting at i ends: the newline
// of its last line, after any lines a trailing backslash joins.
func directiveEnd(text string, i int) int {
	for {
		end := lineEndAt(text, i)
		if end == len(text) || !strings.HasSuffix(strings.TrimRight(text[i:end], "\r"), "\\") {
			return end
		}
		i = end + 1
	}
}

// directiveName returns the name of the directive whose "#" is at i.
func directiveName(text string, i int) string {
	s := strings.TrimLeft(text[i+1:], " \t")
	n := 0
	for n < len(s) && isIdent(s[n]) {
		n++
	}
	return s[:n]
}

// conditional applies a directive to the conditional nesting: depth is how
// many conditionals are open, and skipFrom the depth whose later branches
// are being skipped, or 0.
func conditional(name string, depth, skipFrom int) (newDepth, newSkipFrom int) {
	switch name {
	case "if", "ifdef", "ifndef":
		return depth + 1, skipFrom
	case "elif", "elifdef", "elifndef", "else":
		if skipFrom == 0 && depth > 0 {
			return depth, depth
		}
	case "endif":
		if skipFrom == depth {
			skipFrom = 0
		}
		return max(depth-1, 0), skipFrom
	}
	return depth, skipFrom
}

// stringEnd returns the offset just past the string literal whose opening
// quote is at i: a raw string's closing delimiter, or the next unescaped
// quote on the line. An unterminated literal ends at the line's end.
func stringEnd(text string, i int) int {
	if d, ok := rawDelimiter(text, i); ok {
		if k := strings.Index(text[i+len(d)+2:], ")"+d+"\""); k >= 0 {
			return i + len(d) + 2 + k + len(d) + 2
		}
	}
	for j := i + 1; j < len(text); j++ {
		switch text[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		case '\n':
			return j
		}
	}
	return len(text)
}

// rawDelimiter returns the delimiter of the raw string literal whose quote
// is at i, R"delim( ... )delim", with any encoding prefix before the R.
func rawDelimiter(text string, i int) (string, bool) {
	if i == 0 || text[i-1] != 'R' {
		return "", false
	}
	prefix := i - 1
	for prefix > 0 && isIdent(text[prefix-1]) {
		prefix--
	}
	switch text[prefix : i-1] {
	case "", "u8", "u", "U", "L":
	default:
		return "", false
	}
	k := strings.IndexByte(text[i+1:], '(')
	if k < 0 || k > 16 || strings.ContainsAny(text[i+1:i+1+k], " ()\\\t\n") {
		return "", false
	}
	return text[i+1 : i+1+k], true
}

// charEnd returns the offset just past the character literal whose opening
// quote is at i, or i+1 when no quote closes it on the line, so that a
// stray quote blanks nothing but itself.
func charEnd(text string, i int) int {
	for j := i + 1; j < len(text); j++ {
		switch text[j] {
		case '\\':
			j++
		case '\'':
			return j + 1
		case '\n':
			return i + 1
		}
	}
	return i + 1
}

// digitSeparator reports whether the quote at i separates the digits of a
// number, as in 1'000'000, rather than opening a character literal.
func digitSeparator(text string, i int) bool {
	j := i
	for j > 0 && isIdent(text[j-1]) {
		j--
	}
	return j < i && text[j] >= '0' && text[j] <= '9'
}
