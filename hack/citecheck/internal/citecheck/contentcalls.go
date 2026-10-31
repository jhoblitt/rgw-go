package citecheck

import (
	"regexp"
	"strings"
)

// maxCalls is how many calls reached follows from a function to another.
const maxCalls = 2

// reCall is a name and the "(" that calls it, in a file's code.
var reCall = regexp.MustCompile(`([A-Za-z_]\w*)\s*\(`)

// functions returns the blocks of the file's function definitions by the last
// part of their names, once.
func (f *file) functions() map[string][]block {
	if f.defs != nil {
		return f.defs
	}
	f.analyze()
	f.defs = map[string][]block{}
	for _, b := range f.blocks {
		if b.scope {
			continue
		}
		if name := definedName(f.code[b.head:b.open]); name != "" {
			f.defs[name] = append(f.defs[name], b)
		}
	}
	return f.defs
}

// definedName returns the name a function definition's header defines, the
// last part of the name before its first "(", or "" for a header that defines
// none: a control statement's; a lambda's; and a call's that a lambda passed
// to it opens, whose parenthesis the header leaves open, so that
// std::for_each(v.begin(), v.end(), [&](int& x) { defines no for_each.
func definedName(header string) string {
	name, _ := definedAt(header)
	return name
}

// definedAt returns the name definedName returns and its offset in header.
func definedAt(header string) (name string, at int) {
	before, _, found := strings.Cut(header, "(")
	if !found || strings.Count(header, "(") != strings.Count(header, ")") {
		return "", -1
	}
	before = strings.TrimRight(before, " \t\n")
	j := len(before)
	for j > 0 && isIdent(before[j-1]) {
		j--
	}
	if name = before[j:]; name != "" && !controlWords[name] && !cppKeywords[name] {
		return name, j
	}
	return "", -1
}

// definesAs reports whether the function definition b's name agrees with the
// qualifier qual a citation gives it, as compatible says, its own qualifier
// being the one written before it or the scopes it is defined in. An
// unqualified name agrees with any.
func (f *file) definesAs(b block, qual []string) bool {
	if len(qual) == 0 {
		return true
	}
	_, at := definedAt(f.code[b.head:b.open])
	if at < 0 {
		return false
	}
	q := f.qualifier(f.code, b.head+at, b.head)
	return len(q) > 0 && compatible(qual, q)
}

// callees returns the functions the code between lo and hi calls that the
// file defines once, in the order they are first called. A name the file
// defines more than once, as each op class's execute is, could be any of
// them, so it is no callee here.
func (f *file) callees(lo, hi int) []string {
	defs := f.functions()
	var out []string
	seen := map[string]bool{}
	for _, m := range reCall.FindAllStringSubmatch(f.code[lo:hi], -1) {
		if name := m[1]; !seen[name] && len(defs[name]) == 1 {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// reached returns how a function one of ids names, defined once in the file
// and, when the name is qualified, under a qualifier that agrees with it, and
// the code of lines from to to are joined by calls, maxCalls at most, each to
// a function the file defines once, or "": the named function's calls reach
// the function the lines are in, as read_line's reach index_of's body through
// read_with_boundary; or the lines' calls reach the named function. A name
// its text gives that the lines' code reaches only through a variable, a type
// or another file is not followed.
func reached(f *file, from, to int, ids []string) string {
	defs := f.functions()
	var in string
	for _, b := range f.enclosing(from, to) {
		if name := definedName(f.code[b.head:b.open]); name != "" && !b.scope {
			in = name
			break
		}
	}
	for _, id := range ids {
		parts := strings.Split(strings.TrimPrefix(id, "::"), "::")
		name, qual := parts[len(parts)-1], parts[:len(parts)-1]
		if len(defs[name]) != 1 || !f.definesAs(defs[name][0], qual) {
			continue
		}
		if in != "" && len(defs[in]) == 1 {
			b := defs[name][0]
			if path := f.callPath(b.open, b.close, in); path != nil {
				if len(path) == 1 {
					return "they lie in " + in + ", which " + name + " calls"
				}
				return "they lie in " + in + ", which " + name + " reaches through " +
					strings.Join(path[:len(path)-1], ", ")
			}
		}
		if path := f.callPath(f.lineStart(from), f.lineEnd(to), name); len(path) > 1 {
			return "they reach " + name + " through " + strings.Join(path[:len(path)-1], ", ")
		}
	}
	return ""
}

// callPath returns the functions through which the code between lo and hi
// calls target, maxCalls calls at most, target last, or nil.
func (f *file) callPath(lo, hi int, target string) []string {
	defs := f.functions()
	type step struct {
		name string
		path []string
	}
	frontier := []step{{path: nil}}
	seen := map[string]bool{}
	for range maxCalls {
		var next []step
		for _, s := range frontier {
			from, to := lo, hi
			if s.name != "" {
				b := defs[s.name][0]
				from, to = b.open, b.close
			}
			for _, c := range f.callees(from, to) {
				path := append(append([]string(nil), s.path...), c)
				if c == target {
					return path
				}
				if !seen[c] {
					seen[c] = true
					next = append(next, step{name: c, path: path})
				}
			}
		}
		frontier = next
	}
	return nil
}
