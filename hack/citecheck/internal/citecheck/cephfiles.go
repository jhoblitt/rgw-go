package citecheck

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// cephFilesText is the generated list of ceph's citable files, by which
// --form-only, which has no clone, tells a plain markdown citation of ceph's
// code from one of rgw-go's.
//
//go:embed cephfiles.txt
var cephFilesText string

// reCitableBase is a base name a plain citation can name: goPath's last
// segment.
var reCitableBase = regexp.MustCompile(`^[\w.\-]*[A-Za-z_][\w\-]*\.` + goExt + `$`)

// The headers of the list: the refs it was listed at, and the pattern its
// base names were chosen by, so that a list made before the pattern changed
// reads as stale.
const (
	refsHeader    = "# refs: "
	patternHeader = "# names: "
)

// cephFileList is ceph's citable files at the refs it was listed at.
type cephFileList struct {
	refs    []string
	pattern string
	paths   []string
	byBase  map[string][]string
}

// cephFiles returns the embedded list, parsed once.
var cephFiles = sync.OnceValues(func() (*cephFileList, error) { return parseCephFiles(cephFilesText) })

// parseCephFiles reads a list: comment lines, two of which are its headers,
// then one path per line.
func parseCephFiles(text string) (*cephFileList, error) {
	l := &cephFileList{byBase: map[string][]string{}}
	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, refsHeader):
			l.refs = strings.Fields(strings.TrimPrefix(line, refsHeader))
		case strings.HasPrefix(line, patternHeader):
			l.pattern = strings.TrimPrefix(line, patternHeader)
		case strings.HasPrefix(line, "#"), line == "":
		default:
			l.paths = append(l.paths, line)
			l.byBase[path.Base(line)] = append(l.byBase[path.Base(line)], line)
		}
	}
	if len(l.refs) == 0 || l.pattern == "" {
		return nil, errors.New("the list of ceph's files names no refs or no pattern in its header")
	}
	return l, nil
}

// named returns the listed paths that name, a path or a base name, names:
// name itself, and every path that ends in "/" and name.
func (l *cephFileList) named(name string) []string {
	var paths []string
	for _, p := range l.byBase[path.Base(name)] {
		if p == name || strings.HasSuffix(p, "/"+name) {
			paths = append(paths, p)
		}
	}
	return paths
}

// listCephFiles returns the list of the citable files the clone holds at
// refs, each a tag or a commit id: its headers, then every such path at any
// of them, sorted; and the number of paths.
func listCephFiles(ctx context.Context, c *Checker, refs []string) (list []byte, n int, err error) {
	if len(refs) == 0 {
		return nil, 0, errors.New("no ref given")
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		r, err := c.resolve(ctx, ref)
		switch {
		case err != nil:
			return nil, 0, err
		case r.problem != "":
			return nil, 0, errors.New(r.problem)
		}
		byBase, err := c.tree(ctx, r.commit)
		if err != nil {
			return nil, 0, err
		}
		for base, paths := range byBase {
			if reCitableBase.MatchString(base) {
				for _, p := range paths {
					seen[p] = true
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("# The paths of ceph/ceph whose base name a plain citation can name, at each ref below.\n")
	b.WriteString("# make citecheck-files writes it from a clone, and make citecheck-files-check finds it stale.\n")
	b.WriteString(refsHeader + strings.Join(refs, " ") + "\n")
	b.WriteString(patternHeader + reCitableBase.String() + "\n")
	for _, p := range slices.Sorted(maps.Keys(seen)) {
		b.WriteString(p + "\n")
	}
	return []byte(b.String()), len(seen), nil
}

// staleness returns how have, a list, differs from want, one made afresh, or
// "" when they are the same.
func staleness(have, want []byte) (string, error) {
	h, err := parseCephFiles(string(have))
	if err != nil {
		return "", err
	}
	w, err := parseCephFiles(string(want))
	if err != nil {
		return "", err
	}
	var why []string
	if !slices.Equal(h.refs, w.refs) {
		why = append(why, fmt.Sprintf("it was listed at %s, not %s", strings.Join(h.refs, " "), strings.Join(w.refs, " ")))
	}
	if h.pattern != w.pattern {
		why = append(why, fmt.Sprintf("its names were chosen by %s, not %s", h.pattern, w.pattern))
	}
	for _, d := range []struct {
		paths      []string
		has, lacks string
	}{{missingFrom(w.paths, h.paths), "lacks", "has"}, {missingFrom(h.paths, w.paths), "has", "lacks"}} {
		if n := len(d.paths); n > 0 {
			why = append(why, fmt.Sprintf("it %s %s the clone %s, such as %s", d.has, pathCount(n), d.lacks,
				strings.Join(d.paths[:min(3, n)], ", ")))
		}
	}
	if len(why) == 0 && !bytes.Equal(have, want) {
		why = append(why, "its comments or its order differ")
	}
	return strings.Join(why, "; "), nil
}

// pathCount returns "1 path" or "<n> paths".
func pathCount(n int) string {
	if n == 1 {
		return "1 path"
	}
	return fmt.Sprintf("%d paths", n)
}

// missingFrom returns the strings of a that b lacks.
func missingFrom(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}
