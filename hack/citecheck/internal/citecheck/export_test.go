package citecheck

import (
	"maps"
	"slices"
)

// ScopeOf exposes scopeOf to the specs.
var ScopeOf = scopeOf

// LinkFor exposes linkFor to the specs.
var LinkFor = linkFor

// CephFiles returns the embedded list of ceph's files: the refs it was
// listed at, the pattern its names were chosen by, and its paths.
func CephFiles() (refs []string, pattern string, paths []string) {
	l, err := cephFiles()
	if err != nil {
		panic(err)
	}
	return l.refs, l.pattern, l.paths
}

// CitableBase returns the pattern of a base name a plain citation can name.
func CitableBase() string { return reCitableBase.String() }

// MarkTags returns the releases the [S] and [T] markers name.
func MarkTags() []string { return slices.Sorted(maps.Values(markTags)) }
