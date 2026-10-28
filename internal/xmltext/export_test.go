package xmltext

import "encoding/xml"

// CountAttrComparisons is how many times Scan compares two attribute names
// while f runs.
func CountAttrComparisons(f func()) int {
	n := 0
	old := sameAttrName
	sameAttrName = func(a, b xml.Name) bool {
		n++
		return old(a, b)
	}
	defer func() { sameAttrName = old }()
	f()
	return n
}
