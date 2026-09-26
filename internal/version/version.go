// Package version reports what the toolchain stamped into the binary.
//
// Managed by go-conventions (references/layout.md owns this API). There are
// no -X ldflags: go build records the module version from the git tag, plus
// vcs.revision, vcs.time, and vcs.modified, and go install records the
// module version it fetched.
package version

import (
	"runtime/debug"
	"strings"
)

// Info is the build identity of the running binary.
type Info struct {
	Version   string
	Revision  string
	Time      string
	Modified  bool
	GoVersion string
}

// Read returns the build identity, or zero values outside a module build.
func Read() Info {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Info{}
	}
	info := Info{Version: bi.Main.Version, GoVersion: bi.GoVersion}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.time":
			info.Time = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// String renders the running binary's identity the way --version prints it.
func String() string {
	return Read().String()
}

// String renders i as "<version> (<revision>[, dirty]) built <time> <go>".
func (i Info) String() string {
	var b strings.Builder
	b.WriteString(i.Version)
	if i.Revision != "" {
		b.WriteString(" (")
		b.WriteString(i.Revision[:min(12, len(i.Revision))])
		if i.Modified {
			b.WriteString(", dirty")
		}
		b.WriteString(")")
	}
	if i.Time != "" {
		b.WriteString(" built " + i.Time)
	}
	if i.GoVersion != "" {
		b.WriteString(" " + i.GoVersion)
	}
	return b.String()
}
