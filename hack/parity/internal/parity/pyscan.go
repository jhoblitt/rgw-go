package parity

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// pyLine is a line of a Python module as scanPython reads it.
type pyLine struct {
	text string
	// inside is set for a line that starts inside brackets or a string, or
	// after a line ending in a backslash: it continues what came before.
	inside bool
	// code is set for a line that holds more than blanks and a comment.
	code bool
	// crosses is set for a line that starts inside a string or after a
	// backslash continuation, or ends inside a string, a one-quote one
	// included, or in a continuation: one whose text runs across lines.
	crosses bool
}

// start reports whether the line starts a top-level statement: code at
// column 0 that continues nothing.
func (l pyLine) start() bool {
	return l.code && !l.inside && l.text[0] != ' ' && l.text[0] != '\t'
}

// scanPython splits text into lines and marks which continue a bracket, a
// string or a backslash. It reads quotes, triple quotes, escapes, comments
// and brackets, which is all that decides where a top-level statement
// starts; it does not check the syntax.
func scanPython(text string) []pyLine {
	text = strings.TrimSuffix(text, "\n")
	var lines []pyLine
	depth, quote, continued := 0, "", false
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		inside := depth > 0 || quote != "" || continued
		crosses := quote != "" || continued
		trimmed := strings.TrimSpace(line)
		lines = append(lines, pyLine{text: line, inside: inside, code: trimmed != "" && (inside || !strings.HasPrefix(trimmed, "#"))})
		continued = false
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quote != "" {
				switch {
				case c == '\\':
					i++
				case strings.HasPrefix(line[i:], quote):
					i += len(quote) - 1
					quote = ""
				}
				continue
			}
			switch c {
			case '#':
				i = len(line)
			case '\'', '"':
				quote = string(c)
				if triple := strings.Repeat(quote, 3); strings.HasPrefix(line[i:], triple) {
					quote = triple
					i += 2
				}
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth = max(depth-1, 0)
			case '\\':
				continued = i == len(line)-1
			}
		}
		lines[len(lines)-1].crosses = crosses || quote != "" || continued
		// A one-quote string ends with its line unless a backslash carries
		// it on; anything else is a syntax error the scan need not follow,
		// or, since PEP 701, an f-string's replacement field that runs on.
		if len(quote) == 1 && !strings.HasSuffix(line, "\\") {
			quote = ""
		}
	}
	return lines
}

// pyStmt is a top-level statement: its lines run from its first to the line
// before the next statement's, decorators joined to what they decorate and
// else, elif, except and finally clauses to their statement.
type pyStmt struct {
	first, end int
}

var (
	reClause    = regexp.MustCompile(`^(?:else|elif|except|finally)\b`)
	reDecorator = regexp.MustCompile(`^@`)
	// reMarkLine and reTestDef start the only top-level lines an additive
	// change may add to a test module.
	reMarkLine = regexp.MustCompile(`^@pytest\.mark\.`)
	reTestDef  = regexp.MustCompile(`^def (test_\w*)\(`)
	reName     = regexp.MustCompile(`[A-Za-z_]\w*`)
	// reMarkOnly is the whole of a mark line an additive change may add:
	// the mark's name, then spaces and a comment at most, printable ASCII
	// with no backslash in the comment.
	reMarkOnly = regexp.MustCompile(`^@pytest\.mark\.[A-Za-z_][A-Za-z0-9_]*[ ]*(?:#[\x20-\x5b\x5d-\x7e]*)?$`)
	// reDefOnly is the whole of a def line an additive change may add: the
	// test's name, its parameters as bare names, and a colon with nothing
	// after it but a comment, so that its body starts on the next line.
	reDefOnly = regexp.MustCompile(`^def test_[A-Za-z0-9_]*\([ ]*(?:[A-Za-z_][A-Za-z0-9_]*(?:[ ]*,[ ]*[A-Za-z_][A-Za-z0-9_]*)*[ ]*,?[ ]*)?\):[ ]*(?:#[\x20-\x5b\x5d-\x7e]*)?$`)
)

// markLine reports whether line is a mark line an additive change may add:
// a bare @pytest.mark.<name>, which Python reads as an attribute of pytest's
// mark generator and nothing more. A mark's arguments are refused, literal
// or not: Python evaluates them when it imports the module, and pytest
// judges them when it collects it, where a parametrize whose names and
// values disagree fails the module and every test in it.
func markLine(line string) bool {
	return reMarkOnly.MatchString(line)
}

// markName returns the marker a mark line names.
func markName(line string) string {
	return reName.FindString(strings.TrimPrefix(line, "@pytest.mark."))
}

// hiddenBreak reports whether line, an added line of a test module or of
// pytest.ini, is not UTF-8 or holds a control, format or line or paragraph
// separator character other than a tab. Python reads a lone carriage
// return as a line break, and pytest's ini reader every break
// str.splitlines knows, a vertical tab, a form feed and U+2028 among them,
// where the scan, splitting on line feeds alone, sees one line.
func hiddenBreak(line string) bool {
	return !utf8.ValidString(line) || strings.ContainsFunc(line, func(r rune) bool { return r != '\t' && unsafeRune(r) })
}

// hiddenBreakProblem is the problem hiddenBreak's line i makes.
func hiddenBreakProblem(i int, line string) string {
	return fmt.Sprintf("line %d holds a character Python or pytest may read as a line break, or that hides text: %q", i+1, line)
}

// addedCodeProblem returns why line i, an added line of a test module as
// its text holds it, line ending and all, is not one the scan reads as
// Python does, or "": it must be printable ASCII ending at a line feed,
// with no tab, which Python's indentation reads apart from spaces, no ";",
// which puts a second statement on a line, and no backslash at its end,
// which continues it onto the next.
func addedCodeProblem(i int, line string) string {
	switch {
	case hiddenBreak(line):
		return hiddenBreakProblem(i, line)
	case strings.Contains(line, "\t"):
		return fmt.Sprintf("line %d holds a tab; an added line indents with spaces: %q", i+1, line)
	case strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 || r > 0x7e }):
		return fmt.Sprintf("line %d holds a character other than printable ASCII: %q", i+1, line)
	case strings.Contains(line, ";"):
		return fmt.Sprintf("line %d holds a \";\", which can put a second statement on a line: %q", i+1, line)
	case strings.HasSuffix(line, "\\"):
		return fmt.Sprintf("line %d ends in a backslash, which continues it onto the next: %q", i+1, line)
	}
	return ""
}

// pyStatements groups lines into top-level statements.
func pyStatements(lines []pyLine) []pyStmt {
	var stmts []pyStmt
	for i, l := range lines {
		if !l.start() {
			continue
		}
		if n := len(stmts); n > 0 {
			prev := lines[stmts[n-1].first].text
			if reClause.MatchString(l.text) || (reDecorator.MatchString(prev) && lastStart(lines, stmts[n-1], i)) {
				continue
			}
			stmts[n-1].end = i
		}
		stmts = append(stmts, pyStmt{first: i, end: len(lines)})
	}
	return stmts
}

// lastStart reports whether every statement start in s before line i is a
// decorator, so that the start at i is what they decorate.
func lastStart(lines []pyLine, s pyStmt, i int) bool {
	for j := s.first; j < i; j++ {
		if lines[j].start() && !reDecorator.MatchString(lines[j].text) {
			return false
		}
	}
	return true
}

// headerAt returns the index of the statement's first line that is not a
// decorator, or its first line when all of it is decorators.
func (s pyStmt) headerAt(lines []pyLine) int {
	for j := s.first; j < s.end; j++ {
		if lines[j].start() && !reDecorator.MatchString(lines[j].text) {
			return j
		}
	}
	return s.first
}

// additiveTestModule judges a test module whose new text only inserts the
// lines inserted marks, by index into the new text's lines. It returns the
// problems that keep the change from being additive, each naming its line,
// and the names of the tests it adds. The change is additive when every
// inserted line is a blank line between statements or belongs to a new
// statement wholly inserted between existing ones, and each new statement
// is bare @pytest.mark decorators, as markLine admits them, each naming one
// of markers, those pytest.ini registers at the new pin, on a def
// test_<name>(...) whose name the module did not hold, on one line with
// bare names for parameters, and its body, every line of it indented. Any
// other top-level line, any line added inside an existing statement, any
// added line addedCodeProblem refuses, and any added line of a string or
// continuation that runs across lines, is a problem.
//
// The scan does not parse Python. These rules keep its reading of an added
// line from mattering: Python runs a module's top-level statements when it
// imports it, and the only added lines at column 0, the marks and the def,
// are fixed one-line forms whose text holds nothing but names and a
// comment; every other added line is indented, so Python reads it in the
// body of the test it follows, where it runs only when the test does.
func additiveTestModule(text string, inserted map[int]bool, markers map[string]bool) (problems, added []string) {
	raw := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines := scanPython(text)
	stmts := pyStatements(lines)
	owner := make([]int, len(lines))
	for i := range owner {
		owner[i] = -1
	}
	isNew := make([]bool, len(stmts))
	for si, s := range stmts {
		isNew[si] = inserted[s.first]
		for j := s.first; j < s.end; j++ {
			owner[j] = si
		}
	}
	var old strings.Builder
	for i, l := range lines {
		if !inserted[i] {
			old.WriteString(l.text)
			old.WriteByte('\n')
		}
	}
	held := map[string]bool{}
	for _, name := range reName.FindAllString(old.String(), -1) {
		held[name] = true
	}

	for i, l := range lines {
		if !inserted[i] {
			continue
		}
		if p := addedCodeProblem(i, raw[i]); p != "" {
			problems = append(problems, p)
			continue
		}
		if strings.TrimSpace(l.text) == "" && !l.inside {
			continue
		}
		topLevel := !l.inside && l.text[0] != ' '
		switch si := owner[i]; {
		case topLevel && !reMarkLine.MatchString(l.text) && !reTestDef.MatchString(l.text):
			problems = append(problems, fmt.Sprintf("line %d is a top-level line other than a @pytest.mark decorator or a def test_: %s",
				i+1, l.text))
		case topLevel && reMarkLine.MatchString(l.text) && !markLine(l.text):
			problems = append(problems, fmt.Sprintf("line %d is a @pytest.mark line with more than its name and a comment: %s",
				i+1, l.text))
		case topLevel && reMarkLine.MatchString(l.text) && !markers[markName(l.text)]:
			problems = append(problems, fmt.Sprintf("line %d is a @pytest.mark line whose marker %s pytest.ini does not register "+
				"at the new pin: %s", i+1, markName(l.text), l.text))
		case si < 0:
			problems = append(problems, fmt.Sprintf("line %d is added before the module's first statement: %s", i+1, l.text))
		case !isNew[si]:
			problems = append(problems, fmt.Sprintf("line %d is added inside the existing statement at line %d: %s",
				i+1, stmts[si].first+1, l.text))
		case l.crosses:
			problems = append(problems, fmt.Sprintf("line %d starts or ends inside a string that runs across lines or a backslash "+
				"continuation, which the proof does not follow: %s", i+1, l.text))
		}
	}

	for si, s := range stmts {
		if !isNew[si] {
			continue
		}
		for j := s.first; j < s.end; j++ {
			if !inserted[j] && (lines[j].code || lines[j].inside) {
				problems = append(problems, fmt.Sprintf("line %d, existing code, would join the statement added at line %d", j+1, s.first+1))
			}
		}
		hi := s.headerAt(lines)
		m := reTestDef.FindStringSubmatch(lines[hi].text)
		if m == nil {
			problems = append(problems, fmt.Sprintf("line %d adds a statement that is not a def test_: %s", hi+1, lines[hi].text))
			continue
		}
		name := m[1]
		body := 0
		for j := hi + 1; j < s.end; j++ {
			switch t := raw[j]; {
			case strings.TrimLeft(t, " ") == "":
			case t[0] != ' ':
				problems = append(problems, fmt.Sprintf("line %d is at column 0 inside the test added at line %d, "+
					"whose body must be indented to its end: %s", j+1, hi+1, t))
			default:
				body++
			}
		}
		switch {
		case held[name]:
			problems = append(problems, fmt.Sprintf("line %d adds %s, a name the module already holds", hi+1, name))
		case !reDefOnly.MatchString(raw[hi]):
			problems = append(problems, fmt.Sprintf("line %d adds %s with a header other than def %s(<bare names>): on one line: %s",
				hi+1, name, name, raw[hi]))
		case body == 0:
			problems = append(problems, fmt.Sprintf("line %d adds %s with no indented body", hi+1, name))
		default:
			held[name] = true
			added = append(added, name)
		}
	}
	return problems, added
}

var (
	reIniSection = regexp.MustCompile(`^\[(.+)\]\s*$`)
	reIniKey     = regexp.MustCompile(`^([^\s=:#;][^=:]*?)\s*[=:]`)
	reIniMarker  = regexp.MustCompile(`^[ \t]+[A-Za-z_]\w*[ \t]*(?::.*)?$`)
)

// additiveIni judges a pytest.ini whose new text only inserts the lines
// inserted marks: each must register a marker, indented under [pytest]'s
// markers, and hiddenBreak must not refuse it.
func additiveIni(text string, inserted map[int]bool) []string {
	var problems []string
	section, key := "", ""
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if inserted[i] && hiddenBreak(line) {
			problems = append(problems, hiddenBreakProblem(i, line))
		} else if inserted[i] && (!reIniMarker.MatchString(line) || section != "pytest" || key != "markers") {
			problems = append(problems, fmt.Sprintf("line %d adds more than a marker to [pytest] markers: %s", i+1, line))
		}
		if m := reIniSection.FindStringSubmatch(line); m != nil {
			section, key = m[1], ""
		} else if m := reIniKey.FindStringSubmatch(line); m != nil {
			key = strings.TrimSpace(m[1])
		}
	}
	return problems
}

// iniMarkers returns the markers a pytest.ini registers in [pytest]'s
// markers, on the key's line or the indented lines after it, each named as
// pytest names a marker line, by the text before any ":" or "(", and only
// when that text is a name.
func iniMarkers(text string) map[string]bool {
	markers := map[string]bool{}
	section, key := "", ""
	for line := range strings.SplitSeq(text, "\n") {
		value := line
		if m := reIniSection.FindStringSubmatch(line); m != nil {
			section, key = m[1], ""
			continue
		} else if m := reIniKey.FindStringSubmatch(line); m != nil {
			key, value = strings.TrimSpace(m[1]), line[len(m[0]):]
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			continue
		}
		if section != "pytest" || key != "markers" {
			continue
		}
		name, _, _ := strings.Cut(value, ":")
		name, _, _ = strings.Cut(name, "(")
		if name = strings.TrimSpace(name); name != "" && reName.FindString(name) == name {
			markers[name] = true
		}
	}
	return markers
}
