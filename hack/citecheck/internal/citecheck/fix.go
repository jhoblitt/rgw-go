package citecheck

import (
	"bytes"
	"cmp"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

// edit replaces the bytes [start, end) of a document with text.
type edit struct {
	start, end int
	text       string
}

// linkFor returns the markdown link that replaces a code span citation: the
// file's base name and the lines as its text, and the GitHub URL of those
// lines at the span's ref, with no title, since a link has no symbol. An
// error means the link would not read back as the span's citation, which is
// a bug, not a wrong citation.
func linkFor(c Citation) (string, error) {
	lines := strconv.Itoa(c.From)
	if c.To != c.From {
		lines += "-" + strconv.Itoa(c.To)
	}
	link := "[" + linkText(path.Base(c.Path)+":"+lines) + "](" + citationURL(c) + ")"
	if !readsBack(link, c) {
		return "", fmt.Errorf("the link %s written for %s reads back as another citation", link, c.Text)
	}
	return link, nil
}

// citationURL returns the GitHub URL of a citation's lines at its ref, each
// segment of its path percent-encoded, as GitHub needs a "%" or a "|" to be.
func citationURL(c Citation) string {
	anchor := "L" + strconv.Itoa(c.From)
	if c.To != c.From {
		anchor += "-L" + strconv.Itoa(c.To)
	}
	segments := strings.Split(c.Path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return "https://github.com/ceph/ceph/blob/" + c.Ref + "/" + strings.Join(segments, "/") + "#" + anchor
}

// readsBack reports whether text holds exactly one link, and it to c's
// lines at c's ref.
func readsBack(text string, c Citation) bool {
	back, _ := links(text, c.Line)
	return len(back) == 1 && back[0].Problem == "" && back[0].Ref == c.Ref && back[0].Path == c.Path &&
		back[0].From == c.From && back[0].To == c.To
}

// linkText escapes what markdown would read in a link's text as more than
// text: a backslash; a "|", which ends a table's cell; and "*", "~" and "_",
// which emphasize, though an "_" between two ASCII letters or digits cannot.
// It works on bytes, so that a path that is not UTF-8 keeps its bytes.
func linkText(s string) string {
	var b strings.Builder
	for i := range len(s) {
		switch s[i] {
		case '\\', '|', '*', '~':
			b.WriteByte('\\')
		case '_':
			if i == 0 || i == len(s)-1 || !alnum(s[i-1]) || !alnum(s[i+1]) {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func alnum(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' }

// rewrite returns data with each edit made and every other byte kept.
// Edits do not overlap.
func rewrite(data []byte, edits []edit) []byte {
	edits = slices.SortedFunc(slices.Values(edits), func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	var b bytes.Buffer
	last := 0
	for _, e := range edits {
		b.Write(data[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.Write(data[last:])
	return b.Bytes()
}
