package parity

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// unsafeRune reports whether r could forge or hide text where a line is
// shown: a control character, a format character such as a bidi override
// or a zero-width space, or a line or paragraph separator.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// SafeLine returns s escaped to be shown as one line of a job's log, so
// that a reader reads back exactly s and the Actions runner reads no
// workflow command in it: "\" becomes "\\", every unsafe rune a \uXXXX
// escape and a byte that is not UTF-8 a \xXX one; every "##[", which the
// runner's older command syntax finds anywhere in a line, becomes
// "#\x23[", "#" being escaped nowhere else; and a line the runner would
// read as a "::" command, after any blanks, starts with "\" instead, which
// no escape begins with. Distinct strings stay distinct.
func SafeLine(s string) string {
	return escapeLine(s, true)
}

// guardLine is SafeLine without escaping "\": it leaves a line SafeLine
// wrote as it is, and keeps any other from forging a line or a command.
func guardLine(s string) string {
	return escapeLine(s, false)
}

func escapeLine(s string, backslash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\' && backslash:
			b.WriteString(`\\`)
		case !unsafeRune(r):
			b.WriteString(s[i : i+size])
		case r > 0xffff:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	out := strings.ReplaceAll(b.String(), "##[", `#\x23[`)
	if t := strings.TrimLeftFunc(out, unicode.IsSpace); strings.HasPrefix(t, "::") {
		out = `\` + out
	}
	return out
}

// SafeText guards each line of s, an error message, as guardLine does.
func SafeText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = guardLine(l)
	}
	return strings.Join(lines, "\n")
}

// lineGuard writes what it is given a line at a time, each through
// guardLine, so that no text a ledger holds can start a line or a workflow
// command of its own in a log.
type lineGuard struct {
	w   io.Writer
	buf []byte
}

func (g *lineGuard) Write(p []byte) (int, error) {
	g.buf = append(g.buf, p...)
	for {
		i := bytes.IndexByte(g.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := io.WriteString(g.w, guardLine(string(g.buf[:i]))+"\n"); err != nil {
			return 0, err
		}
		g.buf = g.buf[i+1:]
	}
}

// flush writes what is left of a last line without its newline.
func (g *lineGuard) flush() error {
	if len(g.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(g.w, guardLine(string(g.buf)))
	g.buf = nil
	return err
}

// codeSpan returns s as a Markdown code span that CommonMark reads back as
// SafeLine(s): between backtick runs longer than any run in it, and padded
// with a space each side when it starts or ends with a backtick or a space,
// since CommonMark strips one space from each side of a span that starts
// and ends with one.
func codeSpan(s string) string {
	s = SafeLine(s)
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	if s == "" || strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// codeBlock returns lines as a fenced Markdown code block, each SafeLine'd,
// its fence longer than any backtick run in them, so no line closes it.
func codeBlock(lines []string) string {
	n := 2
	safe := make([]string, len(lines))
	for i, l := range lines {
		safe[i] = SafeLine(l)
		n = max(n, longestRun(safe[i], '`'))
	}
	fence := strings.Repeat("`", n+1)
	return fence + "\n" + strings.Join(append(safe, ""), "\n") + fence + "\n"
}

// longestRun returns the length of the longest run of c in s.
func longestRun(s string, c byte) int {
	longest, run := 0, 0
	for i := range len(s) {
		if s[i] == c {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}
