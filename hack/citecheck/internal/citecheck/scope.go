package citecheck

import (
	"regexp"
	"strings"
)

var (
	// reQualified is a possibly qualified name, blanks allowed around "::".
	reQualified = regexp.MustCompile(`[A-Za-z_]\w*(?:\s*::\s*[A-Za-z_]\w*)*`)
	// reNamespaceName is what follows "namespace" in a named one's header.
	reNamespaceName = regexp.MustCompile(`^[A-Za-z_]\w*(?:\s*::\s*(?:inline\s+)?[A-Za-z_]\w*)*$`)
	// reAttribute is an attribute a class head may carry before its name.
	reAttribute = regexp.MustCompile(`\[\[.*?\]\]|alignas\s*\([^)]*\)|__attribute__\s*\(\(.*?\)\)`)
)

// scopeOf reports whether a block whose header, in code, is header is a
// scope, and the names it gives the names declared in it. A namespace gives
// its name, split at "::"; an anonymous or inline one, and an extern "C"
// block, give none. A class, struct or union gives its name without template
// arguments, read from the last of those keywords in the header, so that a
// template head or a macro line before it does not hide it. One whose head,
// template arguments and attributes aside, holds a parenthesis or "=", or,
// before its base clause, "*", "&" or ",", is a function, a parameter or a
// variable, not a scope.
func scopeOf(header string) (scope bool, names []string) {
	if fields := strings.Fields(header); len(fields) > 0 && fields[len(fields)-1] == "extern" {
		return true, nil
	}
	if k := lastWord(header, "namespace"); k >= 0 {
		rest := strings.TrimSpace(header[k+len("namespace"):])
		if previousWord(header, k) == "inline" || !reNamespaceName.MatchString(rest) {
			return true, nil
		}
		return true, splitQualified(strings.ReplaceAll(rest, "inline ", ""))
	}
	k, kw := -1, ""
	for _, w := range []string{"class", "struct", "union"} {
		if i := lastWord(header, w); i > k {
			k, kw = i, w
		}
	}
	if k < 0 || previousWord(header, k) == "enum" {
		return false, nil
	}
	rest := withoutTemplateArgs(reAttribute.ReplaceAllString(header[k+len(kw):], " "))
	if i := singleColon(rest); i >= 0 {
		if strings.ContainsAny(rest[i:], "(=") {
			return false, nil
		}
		rest = rest[:i]
	}
	if strings.ContainsAny(rest, "()=*&,") {
		return false, nil
	}
	var name string
	for _, n := range reQualified.FindAllString(rest, -1) {
		if n != "final" {
			name = n
		}
	}
	if name == "" {
		return true, nil
	}
	return true, splitQualified(name)
}

// lastWord returns the offset of the last occurrence of word in s that is not
// part of a longer identifier, or -1.
func lastWord(s, word string) int {
	for end := len(s); ; {
		i := strings.LastIndex(s[:end], word)
		if i < 0 {
			return -1
		}
		e := i + len(word)
		if (i == 0 || !isIdent(s[i-1])) && (e == len(s) || !isIdent(s[e])) {
			return i
		}
		end = i
	}
}

// previousWord returns the identifier just before offset i in s, blanks
// skipped, or "".
func previousWord(s string, i int) string {
	e := skipBlanksBack(s, i, 0)
	k := e
	for k > 0 && isIdent(s[k-1]) {
		k--
	}
	return s[k:e]
}

// singleColon returns the offset of the first ":" in s that is not half of a
// "::", or -1.
func singleColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}
		if i+1 < len(s) && s[i+1] == ':' {
			i++
			continue
		}
		return i
	}
	return -1
}

// withoutTemplateArgs returns s with every balanced <...> removed.
func withoutTemplateArgs(s string) string {
	var b strings.Builder
	depth := 0
	for i := range len(s) {
		switch {
		case s[i] == '<':
			depth++
		case s[i] == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// splitQualified splits a qualified name at "::", blanks dropped.
func splitQualified(name string) []string {
	parts := strings.Split(name, "::")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
