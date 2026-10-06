// Package rgwtext transcribes the helpers radosgw reads request text
// through, which several rgw-go packages need to read text exactly as
// radosgw does. It imports nothing from rgw-go, so any package may use it.
package rgwtext

import "strings"

// CString is s as C reads it through a char pointer, c_str() included: up to
// its first NUL.
func CString(s string) string {
	s, _, _ = strings.Cut(s, "\x00")
	return s
}
