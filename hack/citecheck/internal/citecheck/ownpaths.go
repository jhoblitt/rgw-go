package citecheck

import (
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// rgwgoModule is the module rgw-go's go.mod declares.
const rgwgoModule = "github.com/jhoblitt/rgw-go"

// rgwgoRoot returns the root of the rgw-go tree that holds the file name:
// the nearest directory above it whose go.mod declares rgw-go's module, or
// "" when there is none. A checkout and a copy of its files both have one.
func rgwgoRoot(name string) string {
	abs, err := filepath.Abs(name)
	if err != nil {
		return ""
	}
	for dir := filepath.Dir(abs); ; {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // go.mod above a file citecheck was given
		if err == nil && modfile.ModulePath(data) == rgwgoModule {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ownPath reports whether name, as a markdown citation writes it, is the
// path from rgw-go's root of a file of rgw-go's, which stays plain.
func (d *mdDoc) ownPath(name string) bool { return ownFile(d.root, name) }

// ownFile reports whether name, as a citation writes it, is the path from
// root, rgw-go's root or "" when there is none, of a file of rgw-go's.
func ownFile(root, name string) bool {
	if root == "" || !filepath.IsLocal(name) {
		return false
	}
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil && fi.Mode().IsRegular()
}
