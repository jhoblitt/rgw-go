package citecheck

import (
	_ "embed"
	"regexp"
	"strings"
	"sync"
)

//go:embed english.txt
var englishTxt string

var (
	// reQuoted is a quoted string literal.
	reQuoted = regexp.MustCompile(`"[^"\s][^"]*"`)
	// reLiteral is a literal in a code span that is no identifier: words
	// joined by "-" or "/", with no blank, such as a header's name or a path.
	reLiteral = regexp.MustCompile(`[\w.*]+(?:[-/][\w.*]+)+/?`)
	// reQualifiedName is a qualified C++ name, A::B.
	reQualifiedName = regexp.MustCompile(`[A-Za-z_]\w*(?:::~?[A-Za-z_]\w*)+`)
	// reCodeShape is a name no English word has: one with an "_" or a
	// digit, a lower-case letter before a capital, or two capitals first.
	reCodeShape = regexp.MustCompile(`_|\d|[a-z][A-Z]|^[A-Z]{2}`)
)

// genericWords are names that no markdown citation's identifier is taken
// for though no English word: those of the projects, the protocols and the
// namespaces the docs cite, which stand beside any line of ceph's.
var genericWords = map[string]bool{
	"ceph": true, "rgw": true, "rados": true, "librados": true, "std": true, "boost": true, "http": true,
	"https": true, "amz": true, "aws": true,
}

// englishWords returns the English words embedded from english.txt, which
// no name beside a markdown citation is taken for, whatever the files a run
// is given.
var englishWords = sync.OnceValue(func() map[string]bool {
	words := map[string]bool{}
	for line := range strings.Lines(englishTxt) {
		if w := strings.TrimSpace(line); w != "" && !strings.HasPrefix(w, "#") {
			words[w] = true
		}
	}
	return words
})

// mdIdents returns the identifiers named beside a markdown citation that its
// cited lines are checked for, each a whole code token, so that no common
// word stands in for one. A quoted string literal in a code span, and a
// literal there of words joined by "-" or "/", such as a header's name or
// a path, count whole, never by their words; a qualified name, A::B, counts
// whole, and each of its parts of three letters or more as any other name
// where it stands; and any other name in a code span counts when it reads
// as code, with an "_" or a digit, a lower-case letter before a capital, or
// two capitals first, or is no English word, and is none of genericWords,
// which a namespace such as ceph or std is. In prose a name counts only as
// idents reads one.
func mdIdents(text string) []string {
	var all []string
	add := func(s string) {
		if t := strings.TrimSpace(s); t != "" {
			all = append(all, t)
		}
	}
	tokens := func(s string, counts func(string) bool) {
		for _, q := range reQualifiedName.FindAllString(s, -1) {
			add(q)
			for part := range strings.SplitSeq(strings.ReplaceAll(q, "~", ""), "::") {
				if len(part) >= 3 && counts(part) {
					add(part)
				}
			}
		}
		for _, t := range reIdent.FindAllString(reQualifiedName.ReplaceAllString(s, " "), -1) {
			if counts(t) {
				add(t)
			}
		}
	}
	english := englishWords()
	inSpan := func(t string) bool {
		lt := strings.ToLower(t)
		return !genericWords[lt] && (reCodeShape.MatchString(t) || !english[lt])
	}
	for _, span := range reSpan.FindAllString(text, -1) {
		content := strings.TrimSpace(span[1 : len(span)-1])
		if reQuoted.FindString(content) == content {
			add(content)
			continue
		}
		for _, q := range reQuoted.FindAllString(content, -1) {
			add(q)
		}
		content = reQuoted.ReplaceAllString(content, " ")
		for _, l := range reLiteral.FindAllString(content, -1) {
			add(l)
		}
		tokens(reLiteral.ReplaceAllString(content, " "), inSpan)
	}
	tokens(reSpan.ReplaceAllString(text, " "), func(t string) bool {
		return strings.Contains(t, "_") || reCamel.MatchString(t) || reAcronym.MatchString(t)
	})
	seen := map[string]bool{}
	var out []string
	for _, t := range all {
		if stopWords[strings.ToLower(t)] || seen[t] || reNumber.MatchString(t) {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
