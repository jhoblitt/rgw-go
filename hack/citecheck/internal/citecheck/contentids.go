package citecheck

import (
	"regexp"
	"slices"
	"strings"
	"sync"
)

// linkIdents returns the identifiers a link's lines are checked for, whether
// they are its own text's, and the text they were read from: those its text
// names, past the file and lines it may write, or, when it names none, those
// named beside it, as besideRun finds them. Either is read as mdIdents reads
// a markdown citation's, with markdown's backslash escapes dropped, a code
// span's name with a "*" kept whole and, in a Go file, the names that are
// rgw-go's left out; and the words beside it with each file's name blanked.
func (lc *linkChecker) linkIdents(ps *paraScan, it *goItem) (ids []string, own bool, text string) {
	d := lc.d
	if it.label != "" {
		text = unescape(it.label)
		label := []byte(text)
		for _, o := range plainItems(text) {
			for i := o.start; i < o.end; i++ {
				label[i] = ' '
			}
		}
		plain, globs := spanGlobs(string(label))
		if ids = withGlobs(d.ownIdents(plain), globs); len(ids) > 0 {
			return ids, true, text
		}
	}
	text = lc.beside(ps, it)
	plain, globs := spanGlobs(text)
	return withGlobs(d.ownIdents(blankFiles(plain)), globs), false, text
}

// spanGlobs returns text with each code span that holds a name with a "*"
// blanked, and those names.
func spanGlobs(text string) (plain string, globs []string) {
	plain = reSpanGlob.ReplaceAllStringFunc(text, func(s string) string {
		globs = append(globs, reSpanGlob.FindStringSubmatch(s)[1])
		return strings.Repeat(" ", len(s))
	})
	return plain, globs
}

// withGlobs returns ids with each of globs it lacks appended.
func withGlobs(ids, globs []string) []string {
	for _, g := range globs {
		if !slices.Contains(ids, g) {
			ids = append(ids, g)
		}
	}
	return ids
}

// beside returns the words named beside a link, as besideRun finds them in
// its paragraph or, in a table, its cell, markdown's backslash escapes
// dropped.
func (lc *linkChecker) beside(ps *paraScan, it *goItem) string {
	lo, hi := 0, len(ps.norm)
	if lc.md != nil {
		if at := lc.md.at[ps]; lc.md.blocks[at[0]][at[1]].kind == mdRow {
			lo, hi = sentence(ps.norm, it.pos, true)
		}
	}
	return unescape(besideRun(ps.norm, it.pos, lo, hi))
}

// reSpanGlob is a code span that holds a name with a "*" that stands for any
// run of it, as `HTTP_X_AMZ_GRANT_*` and `is_*_op` do, which mdIdents would
// read as the pieces around the "*".
var reSpanGlob = regexp.MustCompile("`\\s*([A-Za-z_][\\w*]*\\*[\\w*]*)\\s*`")

// reRunGap is what may stand between two citations of one run: blanks,
// commas, semicolons and parentheses, "and" and "or", a marker, a tag, and an
// "at", "in" or "on" a tag or both tags.
var reRunGap = regexp.MustCompile(`^(?:[\s,;()]+|\b(?:and|or)\b|\b(?:at|in|on)\s+(?:` + tagForm +
	`|both(?:\s+(?:tags|releases))?)\b|\[[ST]\]|\b` + tagForm + `\b)*$`)

// besideRun returns the words named beside the citation whose placeholder is
// at pos in norm, within lo and hi, a table cell's bounds or the whole norm:
// those that lead to the run of citations it is listed in, back to its
// sentence's start, the citation before the run or 220 bytes, and those that
// follow the run, up to its sentence's end, the first ")" or ";" or 100
// bytes, unless they run on to the next citation, which they then lead to. A
// run is citations with nothing but blanks, commas, semicolons, parentheses,
// "and", "or", markers, tags and "at <tag>" between them. So a neighboring
// citation's names are not its, and a list's lead-in names are each of its
// citations'.
func besideRun(norm string, pos, lo, hi int) string {
	var marks []int
	for i := lo; i < hi; i++ {
		if norm[i] == citeMark {
			marks = append(marks, i)
		}
	}
	k, _ := slices.BinarySearch(marks, pos)
	first, last := k, k
	for first > 0 && reRunGap.MatchString(norm[marks[first-1]+1:marks[first]]) {
		first--
	}
	for last+1 < len(marks) && reRunGap.MatchString(norm[marks[last]+1:marks[last+1]]) {
		last++
	}
	start, end := marks[first], marks[last]
	from := max(lo, sentenceStart(norm, start))
	if first > 0 {
		from = max(from, marks[first-1]+1)
	}
	if start-from > 220 {
		from = start - 220 + strings.IndexByte(norm[start-220:start], ' ') + 1
	}
	_, to := sentence(norm, end, false)
	to = min(hi, to)
	if k := strings.IndexAny(norm[end+1:to], ");"); k >= 0 {
		to = end + 1 + k
	}
	if last+1 < len(marks) && marks[last+1] < to {
		to = end + 1
	}
	if to-end-1 > 100 {
		if k := strings.IndexByte(norm[end+1+100:to], ' '); k >= 0 {
			to = end + 1 + 100 + k
		}
	}
	return norm[from:start] + " " + norm[end+1:to]
}

// reEscape is a markdown backslash escape, a backslash before an ASCII
// punctuation character.
var reEscape = regexp.MustCompile(`\\([[:punct:]])`)

// unescape returns markdown text with its backslash escapes dropped, as it
// renders: \_\_init\_\_.py is __init__.py.
func unescape(s string) string { return reEscape.ReplaceAllString(s, "$1") }

// reFileName is a file's name as a citation writes one.
var reFileName = regexp.MustCompile(goPath)

// blankFiles returns text with every file's name blanked, a name not run on
// by a letter, digit or "_", since a file's name names no code its lines
// hold: rgw_common.h is no rgw_common.
func blankFiles(text string) string {
	b := []byte(text)
	for _, ix := range reFileName.FindAllStringIndex(text, -1) {
		s, e := ix[0], ix[1]
		if s > 0 && pathByte(text[s-1]) || e < len(text) && wordByte(text[e]) {
			continue
		}
		for i := s; i < e; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// ownIdents returns the identifiers mdIdents finds in text, less the names
// that are rgw-go's: an identifier the Go file's own code writes, and one a
// package it imports qualifies.
func (d *goDoc) ownIdents(text string) []string {
	return slices.DeleteFunc(mdIdents(d.blankPkgs(text)), func(t string) bool { return d.code[t] })
}

// looseIdents returns the names in text that may name code, as idents reads
// a plain citation's, less the names that are rgw-go's, as ownIdents leaves
// them out.
func (d *goDoc) looseIdents(text string) []string {
	return slices.DeleteFunc(idents(d.blankPkgs(text)), func(t string) bool { return d.code[t] })
}

// blankPkgs returns text with each selector a package the Go file imports
// qualifies blanked, as op.UserStore.
func (d *goDoc) blankPkgs(text string) string {
	return reSelector.ReplaceAllStringFunc(text, func(s string) string {
		if pkg, _, _ := strings.Cut(s, "."); d.pkgs[pkg] {
			return strings.Repeat(" ", len(s))
		}
		return s
	})
}

// linkHolds reports whether one of ids stands in lines from to to of f, or in
// the header of a block that encloses them, as a function's does its body; a
// qualified name, A::B, stands also where qualifiedIn finds it.
func linkHolds(f *file, from, to int, ids []string) bool {
	lo, hi := f.lineStart(from), f.lineEnd(to)
	for _, id := range ids {
		if standsIn(f.text[lo:hi], id) || f.qualifiedIn(f.text, id, lo, hi) {
			return true
		}
	}
	f.analyze()
	for _, b := range f.enclosing(from, to) {
		for _, id := range ids {
			if standsIn(f.code[b.head:b.open], id) || f.qualifiedIn(f.code, id, b.head, b.open) {
				return true
			}
		}
	}
	return false
}

// qualifiedIn reports whether the qualified name id, A::B, stands in s[lo:hi],
// s being the file's text or its code, as B with a qualifier that agrees with
// A's, as compatible says: one written before it, or the scopes it is
// declared in, as enum struct B is declared inside namespace A. A B with no
// qualifier, written or declared, such as one used in a function's body,
// does not count, so that a name standing bare in some other scope's lines
// is not taken for A's.
func (f *file) qualifiedIn(s, id string, lo, hi int) bool {
	parts := strings.Split(strings.TrimPrefix(id, "::"), "::")
	if len(parts) < 2 || strings.Contains(id, "*") {
		return false
	}
	name, qual := parts[len(parts)-1], parts[:len(parts)-1]
	f.analyze()
	for _, at := range occurrences(s, lo, hi, name) {
		if q := f.qualifier(s, at, lo); len(q) > 0 && compatible(qual, q) {
			return true
		}
	}
	return false
}

// has reports whether id stands anywhere in the file's text, asking once for
// each id.
func (f *file) has(id string) bool {
	if v, ok := f.holds[id]; ok {
		return v
	}
	if f.holds == nil {
		f.holds = map[string]bool{}
	}
	f.holds[id] = standsIn(f.text, id)
	return f.holds[id]
}

// reHeaderName is a header's name, words of letters and digits joined by
// "-", maybe ending in a "*" that stands for any rest.
var reHeaderName = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)+(?:-?\*)?$`)

// reSnake is a name of two words or more joined by "_".
var reSnake = regexp.MustCompile(`^[A-Za-z0-9]+(?:_[A-Za-z0-9]+)+$`)

// standsIn reports whether id stands in s: as a whole word; a name of words
// joined by "_" also as a run of the words of a longer one, write_meta in
// _do_write_meta; a name with a "*" as the pattern globOf gives; and a
// header's name also in its CGI form, as radosgw reads a request's headers,
// x-amz-acl as HTTP_X_AMZ_ACL.
func standsIn(s, id string) bool {
	forms := []string{id}
	if reHeaderName.MatchString(id) {
		forms = append(forms, "HTTP_"+strings.ToUpper(strings.ReplaceAll(id, "-", "_")))
	}
	for _, f := range forms {
		switch {
		case strings.Contains(f, "*"):
			if globOf(f).MatchString(s) {
				return true
			}
		case hasWord(s, f), reSnake.MatchString(f) && hasPart(s, f):
			return true
		}
	}
	return false
}

// hasPart reports whether the name w stands in s as a run of the words of a
// longer name joined by "_": no letter or digit on either side.
func hasPart(s, w string) bool {
	alnum := func(c byte) bool { return isIdent(c) && c != '_' }
	for i := 0; i <= len(s)-len(w); {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if (j == 0 || !alnum(s[j-1])) && (j+len(w) == len(s) || !alnum(s[j+len(w)])) {
			return true
		}
		i = j + 1
	}
	return false
}

// globs caches the pattern of each name with a "*" standsIn has matched.
var globs sync.Map

// globOf returns the pattern a name with a "*" stands for: each "*" any run
// of letters, digits, "_" and "-", the name not run on by a letter, digit or
// "_" before it nor, when it does not end in a "*", after it.
func globOf(name string) *regexp.Regexp {
	if v, ok := globs.Load(name); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re
		}
	}
	parts := strings.Split(name, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	expr := `(?:^|[^A-Za-z0-9_])` + strings.Join(parts, `[A-Za-z0-9_-]*`)
	if !strings.HasSuffix(name, "*") {
		expr += `(?:$|[^A-Za-z0-9_])`
	}
	re := regexp.MustCompile(expr)
	globs.Store(name, re)
	return re
}
