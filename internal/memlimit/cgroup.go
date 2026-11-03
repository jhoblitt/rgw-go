package memlimit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Cgroup is where a process's cgroup v2 memory limit is read from: Proc, a
// proc filesystem whose self/cgroup names the process's cgroup, and Mount,
// the cgroup v2 hierarchy that cgroup's path is under.
type Cgroup struct {
	Proc  string
	Mount string
}

// Self is the running process's cgroup under the usual mounts.
func Self() Cgroup { return Cgroup{Proc: "/proc", Mount: "/sys/fs/cgroup"} }

// Limit is a cgroup's memory limit and the memory.max it was read from.
type Limit struct {
	Bytes int64
	File  string
}

// Read returns the memory limit of c's process: the smallest finite
// memory.max from its own cgroup up to the mount's root, which in a
// container with its own cgroup namespace is the container's cgroup. A
// cgroup with no memory.max, the hierarchy's root or one whose parent does
// not enable the memory controller, limits nothing and is passed over. Read
// looks for the cgroup v2 hierarchy at Mount alone, not wherever
// /proc/self/mountinfo puts one. It fails with ErrNoCgroupV2 when no cgroup
// v2 memory controller covers the process there, with ErrNoLimit when every
// memory.max is max, and otherwise with an error that names the file it
// could not read or parse.
func (c Cgroup) Read() (Limit, error) {
	mount := os.DirFS(c.Mount)
	self, err := fs.ReadFile(os.DirFS(c.Proc), "self/cgroup")
	if err != nil {
		return Limit{}, named(c.Proc, err)
	}
	dir, err := unifiedDir(self, filepath.Join(c.Proc, "self", "cgroup"))
	if err != nil {
		return Limit{}, err
	}
	controllers, err := fs.ReadFile(mount, "cgroup.controllers")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Limit{}, fmt.Errorf("no cgroup v2 mount at %s, which holds no cgroup.controllers: %w", c.Mount, ErrNoCgroupV2)
	case err != nil:
		return Limit{}, named(c.Mount, err)
	case !slices.Contains(strings.Fields(string(controllers)), "memory"):
		return Limit{}, fmt.Errorf("%s lists no memory controller: %w", filepath.Join(c.Mount, "cgroup.controllers"), ErrNoCgroupV2)
	}
	if _, err := fs.Stat(mount, dir); err != nil {
		return Limit{}, named(c.Mount, err)
	}
	var lim Limit
	for d := dir; ; d = path.Dir(d) {
		name := path.Join(d, "memory.max")
		b, err := fs.ReadFile(mount, name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// No memory controller here: the hierarchy's root, or a cgroup
			// whose parent does not enable it.
		case err != nil:
			return Limit{}, named(c.Mount, err)
		default:
			file := filepath.Join(c.Mount, filepath.FromSlash(name))
			n, finite, err := parseMax(b)
			if err != nil {
				return Limit{}, fmt.Errorf("parsing %s: %w", file, err)
			}
			if finite && (lim.File == "" || n < lim.Bytes) {
				lim = Limit{Bytes: n, File: file}
			}
		}
		if d == "." {
			break
		}
	}
	if lim.File == "" {
		return Limit{}, fmt.Errorf("%s and its parents up to %s: %w",
			filepath.Join(c.Mount, filepath.FromSlash(dir)), c.Mount, ErrNoLimit)
	}
	return lim, nil
}

// unifiedDir returns the process's cgroup v2 path from self, a
// /proc/<pid>/cgroup, relative to the hierarchy's root; file names self in
// errors. self holds a hierarchy-ID:controllers:path line for each
// hierarchy, the v2 one as 0::path (cgroups(7)); a v1 hierarchy that holds
// the memory controller leaves the v2 one without it.
func unifiedDir(self []byte, file string) (string, error) {
	var (
		unified string
		found   bool
		v1      string
	)
	for line := range strings.Lines(string(self)) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return "", fmt.Errorf("parsing %s: line %q is not hierarchy-ID:controllers:path", file, line)
		}
		id, controllers, p := parts[0], parts[1], parts[2]
		switch {
		case id == "0" && controllers == "":
			unified, found = p, true
		case v1 == "" && slices.Contains(strings.Split(controllers, ","), "memory"):
			v1 = id
		}
	}
	if !found {
		return "", fmt.Errorf("%s has no 0:: line: %w", file, ErrNoCgroupV2)
	}
	if v1 != "" {
		return "", fmt.Errorf("%s puts the memory controller on cgroup v1 hierarchy %s: %w", file, v1, ErrNoCgroupV2)
	}
	// A path that leaves the root, which the kernel writes with .. for a
	// cgroup outside the reader's cgroup namespace, is not under the mount.
	if !strings.HasPrefix(unified, "/") || path.Clean(unified) != unified {
		return "", fmt.Errorf("parsing %s: cgroup %q is not a path under the hierarchy's root", file, unified)
	}
	if unified == "/" {
		return ".", nil
	}
	return unified[1:], nil
}

// parseMax reads a memory.max, which holds max or a byte count.
func parseMax(b []byte) (n int64, finite bool, err error) {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "max" {
		return 0, false, nil
	}
	n, err = strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false, fmt.Errorf("%q is neither max nor a byte count", s)
	}
	return n, true, nil
}

// named is err, from an os.DirFS of root, with the path root gives it in
// place of the one under root.
func named(root string, err error) error {
	pe, ok := errors.AsType[*fs.PathError](err)
	if !ok {
		return err
	}
	return &fs.PathError{Op: pe.Op, Path: filepath.Join(root, filepath.FromSlash(pe.Path)), Err: pe.Err}
}
