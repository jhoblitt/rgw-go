package denc

import (
	"slices"
	"strings"
)

// Release selects which struct versions an encoder emits. Decoders accept every version.
type Release uint8

// The supported releases, oldest first; a later release compares greater.
const (
	Squid Release = iota
	Tentacle
)

// releaseNames lists Ceph releases oldest first, from the last one below the
// supported floor through the newest name known to exist. Squid sits at
// squidIndex, and each later name is the next Release until Tentacle.
var releaseNames = []string{"reef", "squid", "tentacle", "umbrella"}

const squidIndex = 1

// ParseRelease maps a Ceph release name ("squid", "tentacle", "umbrella", ...) to a Release.
// A name newer than Tentacle maps to Tentacle with ok true; an unknown or older name
// returns ok false.
func ParseRelease(name string) (r Release, ok bool) {
	i := slices.Index(releaseNames, strings.ToLower(name))
	if i < squidIndex {
		return Squid, false
	}
	return min(Release(i-squidIndex), Tentacle), true //nolint:gosec // i-squidIndex is a small non-negative table index
}
