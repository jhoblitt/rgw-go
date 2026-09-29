package denc

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
)

// Release selects which struct versions an encoder emits. Decoders accept every version.
type Release uint8

// The supported releases, oldest first; a later release compares greater.
const (
	Squid Release = iota
	Tentacle
)

// releaseNames lists Ceph's releases oldest first, from the first through the
// newest name known to exist. Squid sits at squidIndex, and each later name
// is the next Release until Tentacle.
var releaseNames = []string{
	"argonaut", "bobtail", "cuttlefish", "dumpling", "emperor", "firefly", "giant",
	"hammer", "infernalis", "jewel", "kraken", "luminous", "mimic", "nautilus",
	"octopus", "pacific", "quincy", "reef", "squid", "tentacle", "umbrella",
}

const squidIndex = 18

// String returns the release's name as Ceph writes it: "squid", "tentacle".
func (r Release) String() string {
	if r > Tentacle {
		return "Release(" + strconv.Itoa(int(r)) + ")"
	}
	return releaseNames[squidIndex+int(r)]
}

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

// ClusterRelease maps a cluster's require_osd_release name to the release an
// encoder emits for it, with ok false for a release below the Squid floor.
// denc knows every release name from argonaut through umbrella, so it takes a
// name it does not know for a newer release: like umbrella, such a name maps
// to Tentacle, with a warning.
func ClusterRelease(ctx context.Context, name string) (r Release, ok bool) {
	lower := strings.ToLower(name)
	if i := slices.Index(releaseNames, lower); i >= 0 && i < squidIndex {
		return Squid, false
	}
	r, ok = ParseRelease(lower)
	if !ok {
		r = Tentacle
	}
	if r.String() != lower {
		slog.WarnContext(ctx, "require_osd_release is newer than every known release; encoding for the newest",
			slog.String("require_osd_release", name), slog.String("encoding_for", r.String()))
	}
	return r, true
}
