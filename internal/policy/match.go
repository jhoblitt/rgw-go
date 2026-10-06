package policy

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// MatchWildcards is match_wildcards (src/rgw/rgw_string.cc:7-22): fnmatch(3)
// with no flags, or FNM_CASEFOLD when caseInsensitive is set. Without
// FNM_PATHNAME and FNM_PERIOD, * matches any run of bytes, '/' and a leading
// '.' included; ? matches one byte; [...] is a class with ranges, a leading !
// or ^ negating it; and \ escapes the byte after it. match_wildcards hands
// fnmatch std::string::data(), so each string ends at its first NUL.
func MatchWildcards(pattern, input string, caseInsensitive bool) bool {
	m := fnmatcher{pattern: rgwtext.CString(pattern), input: rgwtext.CString(input), fold: caseInsensitive}
	ok, _, _ := m.match(0, 0, false)
	return ok
}

// fnmatcher is the single-byte internal_fnmatch of glibc 2.34
// (posix/fnmatch_loop.c), the fnmatch in radosgw's el9 container images, as
// it runs in radosgw: in the C locale, which radosgw never leaves, so a byte
// is a character, tolower(3) folds ASCII only, a range compares byte values
// and a collating symbol or equivalence class names a single byte; and
// without POSIXLY_CORRECT in the environment, so ^ negates a class as ! does.
type fnmatcher struct {
	pattern, input string
	fold           bool
}

// charClassMaxLength is glibc's CHARCLASS_NAME_MAX, which bounds a [:name:].
const charClassMaxLength = 2048

// at is the pattern byte at i, NUL at and past the end as in a C string.
func (m *fnmatcher) at(i int) byte {
	if i < len(m.pattern) {
		return m.pattern[i]
	}
	return 0
}

// lower is fnmatch's FOLD.
func (m *fnmatcher) lower(c byte) byte {
	if m.fold && 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// match is internal_fnmatch on pattern[p:] against input[n:]. stopAtStar
// stands for a non-NULL ends argument: at the next '*' the match succeeds at
// once, returning that star's index and the input offset reached, which the
// caller commits to. star is -1 when the match ran to the end of the pattern.
func (m *fnmatcher) match(p, n int, stopAtStar bool) (ok bool, star, starN int) {
	end := len(m.input)
next:
	for {
		c := m.at(p)
		if c == 0 {
			break
		}
		p++
		c = m.lower(c)
		switch c {
		case '?':
			if n == end {
				return false, -1, 0
			}
		case '\\':
			c = m.at(p)
			p++
			if c == 0 {
				return false, -1, 0
			}
			if n == end || m.lower(m.input[n]) != m.lower(c) {
				return false, -1, 0
			}
		case '*':
			if stopAtStar {
				return true, p - 1, n
			}
			c = m.at(p)
			p++
			for c == '?' || c == '*' {
				if c == '?' {
					if n == end {
						return false, -1, 0
					}
					n++
				}
				c = m.at(p)
				p++
			}
			if c == 0 {
				return true, -1, 0
			}
			first := c
			if first == '\\' {
				first = m.at(p)
			}
			first = m.lower(first)
			p--
			// Each start is tried only up to the pattern's next star: glibc
			// commits to the first start that gets there.
			for ; n < end; n++ {
				if c != '[' && m.lower(m.input[n]) != first {
					continue
				}
				hit, nextP, nextN := m.match(p, n, true)
				if !hit {
					continue
				}
				if nextP < 0 {
					return true, -1, 0
				}
				p, n = nextP, nextN
				continue next
			}
			return false, -1, 0
		case '[':
			var ok bool
			if p, ok = m.bracket(p, n); !ok {
				return false, -1, 0
			}
		default:
			if n == end || c != m.lower(m.input[n]) {
				return false, -1, 0
			}
		}
		n++
	}
	return n == end, -1, 0
}

// bracket is the '[' case of internal_fnmatch, with p just past the '['. It
// returns where the pattern goes on and whether input[n] matched; false is
// FNM_NOMATCH for the whole match.
func (m *fnmatcher) bracket(p, n int) (int, bool) {
	if n == len(m.input) {
		return 0, false
	}
	pInit := p
	not := m.at(p) == '!' || m.at(p) == '^'
	if not {
		p++
	}
	raw := m.input[n]
	fn := m.lower(raw)
	c := m.at(p)
	p++
	for {
		// A member that may open a range leaves its byte in cold; every
		// member leaves c the byte after it, with p past c.
		var cold byte
		opensRange, normal := false, false
		switch {
		case c == '\\':
			if m.at(p) == 0 {
				return 0, false
			}
			c = m.lower(m.at(p))
			p++
			normal = true
		case c == '[' && m.at(p) == ':':
			name, next, state := m.className(p)
			switch state {
			case classTooLong:
				return 0, false
			case classUnclosed:
				c = '['
				normal = true
			case classClosed:
				member, known := isCType(name, raw)
				if !known {
					return 0, false
				}
				if member {
					return m.skip(next, not)
				}
				p = next
				c = m.at(p)
				p++
			}
		case c == '[' && m.at(p) == '=':
			// The equivalence class [=x=] of the C locale is x alone.
			if x := m.at(p + 1); x != 0 && m.at(p+2) == '=' && m.at(p+3) == ']' {
				p += 4
				if raw == x {
					return m.skip(p, not)
				}
				c = m.at(p)
				p++
			} else {
				c = '['
				normal = true
			}
		case c == 0:
			// A class that never closes is an ordinary '['.
			if fn != '[' {
				return 0, false
			}
			return pInit, true
		case c == '[' && m.at(p) == '.':
			sym, next, ok := m.collatingSymbol(p)
			if !ok {
				return 0, false
			}
			p = next
			isRange := m.at(p) == '-' && m.at(p+1) != 0
			if !isRange && raw == sym {
				return m.skip(p, not)
			}
			cold = sym
			opensRange = true
			c = m.at(p)
			p++
		default:
			c = m.lower(c)
			normal = true
		}
		if normal {
			isRange := m.at(p) == '-' && m.at(p+1) != 0 && m.at(p+1) != ']'
			if !isRange && c == fn {
				return m.skip(p, not)
			}
			cold = c
			opensRange = true
			c = m.at(p)
			p++
		}
		if opensRange && c == '-' && m.at(p) != ']' {
			cend := m.at(p)
			p++
			if cend == '[' && m.at(p) == '.' {
				sym, next, ok := m.collatingSymbol(p)
				if !ok {
					return 0, false
				}
				cend, p = sym, next
			} else {
				if cend == '\\' {
					cend = m.at(p)
					p++
				}
				if cend == 0 {
					return 0, false
				}
				cend = m.lower(cend)
			}
			if cold <= fn && fn <= cend {
				return m.skip(p, not)
			}
			c = m.at(p)
			p++
		}
		if c == ']' {
			break
		}
	}
	return p, not
}

// The ways a [:name:] scan ends.
const (
	classClosed = iota
	classUnclosed
	classTooLong
)

// className scans the name of a [:name:], p at its ':'. A name glibc can
// close holds only 'a' to 'y'; on any other byte the '[' is an ordinary
// member, classUnclosed. next is past the closing ":]".
func (m *fnmatcher) className(p int) (name string, next, state int) {
	start := p + 1
	for c1 := 0; ; c1++ {
		if c1 == charClassMaxLength {
			return "", 0, classTooLong
		}
		p++
		c := m.at(p)
		if c == ':' && m.at(p+1) == ']' {
			return m.pattern[start:p], p + 2, classClosed
		}
		if c < 'a' || c >= 'z' {
			return "", 0, classUnclosed
		}
	}
}

// collatingSymbol reads a [.x.], p at its first '.'. The C locale defines no
// multi-byte symbols, so anything but one byte fails the match, as a
// symbol that never closes does. next is past the closing ".]".
func (m *fnmatcher) collatingSymbol(p int) (sym byte, next int, ok bool) {
	start := p
	for {
		p++
		c := m.at(p)
		if c == '.' && m.at(p+1) == ']' {
			break
		}
		if c == 0 {
			return 0, 0, false
		}
	}
	if p-start-1 != 1 {
		return 0, 0, false
	}
	return m.at(start + 1), p + 2, true
}

// skip passes over the rest of a class whose member matched, p past that
// member, and returns where the pattern goes on. A class that never closes
// fails the match: glibc 2.34 did not yet fall back to an ordinary '[' here.
func (m *fnmatcher) skip(p int, not bool) (int, bool) {
	for {
		c := m.at(p)
		p++
		switch {
		case c == ']':
			return p, !not
		case c == 0:
			return 0, false
		case c == '\\':
			if m.at(p) == 0 {
				return 0, false
			}
			p++
		case c == '[' && m.at(p) == ':':
			start := p
			for c1 := 1; ; c1++ {
				p++
				if c1 == charClassMaxLength {
					return 0, false
				}
				if m.at(p) == ':' && m.at(p+1) == ']' {
					p += 2
					break
				}
				if b := m.at(p); b < 'a' || b >= 'z' {
					p = start
					break
				}
			}
		case c == '[' && m.at(p) == '=':
			if m.at(p+1) == 0 || m.at(p+2) != '=' || m.at(p+3) != ']' {
				return 0, false
			}
			p += 4
		case c == '[' && m.at(p) == '.':
			for {
				p++
				b := m.at(p)
				if b == 0 {
					return 0, false
				}
				if b == '.' && m.at(p+1) == ']' {
					break
				}
			}
			p += 2
		}
	}
}

// isCType is _ISCTYPE for the classes wctype(3) knows in the C locale, which
// class only ASCII bytes; known is false for any other name.
func isCType(name string, b byte) (member, known bool) {
	alpha := 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	digit := '0' <= b && b <= '9'
	switch name {
	case "alnum":
		return alpha || digit, true
	case "alpha":
		return alpha, true
	case "blank":
		return b == ' ' || b == '\t', true
	case "cntrl":
		return b < 0x20 || b == 0x7f, true
	case "digit":
		return digit, true
	case "graph":
		return 0x21 <= b && b <= 0x7e, true
	case "lower":
		return 'a' <= b && b <= 'z', true
	case "print":
		return 0x20 <= b && b <= 0x7e, true
	case "punct":
		return 0x21 <= b && b <= 0x7e && !alpha && !digit, true
	case "space":
		return b == ' ' || '\t' <= b && b <= '\r', true
	case "upper":
		return 'A' <= b && b <= 'Z', true
	case "xdigit":
		return digit || 'a' <= b && b <= 'f' || 'A' <= b && b <= 'F', true
	}
	return false, false
}

// MatchPolicy matches input against pattern component by component: both are
// split at every ':', must have the same number of components, and each
// component of the input is matched against the pattern's with
// MatchWildcards. action folds case, as action names are matched; ARN
// components are matched case-sensitively.
//
// radosgw's match_policy instead hands substr the next colon's position as a
// length (src/rgw/rgw_common.cc:2178-2179 at v19.2.6, :2241-2242 at v20.2.4),
// comparing each component after the first together with the text after it.
// The two agree on action names, which have one colon; docs/exclusions.md
// records where they differ for ARNs.
func MatchPolicy(pattern, input string, action bool) bool {
	for {
		p, patRest, patMore := strings.Cut(pattern, ":")
		in, inRest, inMore := strings.Cut(input, ":")
		if patMore != inMore || !MatchWildcards(p, in, action) {
			return false
		}
		if !patMore {
			return true
		}
		pattern, input = patRest, inRest
	}
}

// serviceAlls are the services' All wildcards in action_t order. A service's
// actions run from the previous service's All to its own.
var serviceAlls = [...]Action{S3All, S3ObjectLambdaAll, IAMAll, STSAll, SNSAll, OrganizationsAll}

// MatchAction sets in dst every action of release r whose name matches
// pattern, as the parser's actpairs loop does for an Action or NotAction
// (src/rgw/rgw_iam_policy.cc:637-641 at v19.2.6, :650-654 at v20.2.4). Then,
// as :642-677 (v20.2.4 :655-690) do, it sets each service's All once every
// action of the service the release knows is in dst: radosgw tests its own
// release's range, which holds exactly those actions, where rgw-go's ranges
// also hold the actions a release lacks. It returns false when no action
// matched, which the parser reports as "`<pattern>` is not a valid action".
// radosgw's parser never matches a pattern that starts with '*': it takes any
// such pattern for "*" and sets allValue (:632-635, v20.2.4 :645-648).
func MatchAction(dst *ActionSet, pattern string, r denc.Release) bool {
	matched := false
	first := Action(0)
	for _, all := range serviceAlls {
		covered := true
		for a := first; a < all; a++ {
			if !Known(a, r) {
				continue
			}
			if MatchPolicy(pattern, actionNames[a], true) {
				dst.Set(a)
				matched = true
			}
			covered = covered && dst.Has(a)
		}
		if covered {
			dst.Set(all)
		}
		first = all + 1
	}
	return matched
}
